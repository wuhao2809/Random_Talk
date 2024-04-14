package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	golog "github.com/ipfs/go-log/v2"
	libp2p "github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p/core/crypto"
	host "github.com/libp2p/go-libp2p/core/host"
	network "github.com/libp2p/go-libp2p/core/network"
	peer "github.com/libp2p/go-libp2p/core/peer"
	peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	multiaddr "github.com/multiformats/go-multiaddr"
)

const echoProtocolID = "/echo/1.0.0"

var selfHost host.Host
var selfAddr string

var knownPeers = struct {
	sync.RWMutex
	list map[string]struct{}
}{list: make(map[string]struct{})}

var seenMessages = struct {
	sync.RWMutex
	ids map[string]struct{}
}{ids: make(map[string]struct{})}

type Message struct {
	ID    string   `json:"id"`
	Text  string   `json:"text,omitempty"`
	Peers []string `json:"peers,omitempty"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	golog.SetAllLoggers(golog.LevelInfo)

	listenFlag := flag.Int("l", 0, "Port on which the node will listen")
	flag.Parse()

	if *listenFlag == 0 {
		log.Fatal("Please provide a port to bind on with -l")
	}

	var err error
	selfHost, err = makeBasicHost(*listenFlag)
	if err != nil {
		log.Fatal(err)
	}
	selfAddr = getHostAddress(selfHost)

	fmt.Println("This node's multiaddress:", selfAddr)
	addPeer(selfAddr) // Add self to known peers

	startListener(ctx)

	go readInput(ctx)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			broadcastHeartbeat(ctx)
		}
	}
}

func makeBasicHost(listenPort int) (host.Host, error) {
	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, rand.Reader)
	if err != nil {
		return nil, err
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)),
		libp2p.Identity(priv),
	}

	return libp2p.New(opts...)
}

func getHostAddress(h host.Host) string {
	hostAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/p2p/%s", h.ID().String()))
	addr := h.Addrs()[0]
	return addr.Encapsulate(hostAddr).String()
}

func startListener(ctx context.Context) {
	selfHost.SetStreamHandler(echoProtocolID, func(s network.Stream) {
		defer s.Close()

		var msg Message
		if err := json.NewDecoder(s).Decode(&msg); err != nil {
			log.Println("Failed to decode message:", err)
			return
		}

		handleMessage(s, msg)
	})
}

// Helper function to extract the sender's full multiaddress from the stream
func getFullAddressFromStream(s network.Stream) string {
	remotePeer := s.Conn().RemotePeer().String()
	remoteAddr := s.Conn().RemoteMultiaddr().String()
	return fmt.Sprintf("%s/p2p/%s", remoteAddr, remotePeer)
}

func handleMessage(s network.Stream, msg Message) {
	// Extract the sender's full multiaddress
	senderAddr := getFullAddressFromStream(s)
	addPeer(senderAddr)

	// If this is a heartbeat message, update peers and return without rebroadcasting
	if msg.Text == "Heartbeat" {
		for _, p := range msg.Peers {
			addPeer(p)
		}
		return // Do not rebroadcast heartbeat messages
	}

	// For non-heartbeat messages, check if we've seen this message before
	seenMessages.Lock()
	if _, seen := seenMessages.ids[msg.ID]; seen {
		seenMessages.Unlock()
		return // If message has been seen, do nothing
	}
	// Mark this message as seen
	seenMessages.ids[msg.ID] = struct{}{}
	seenMessages.Unlock()

	// Proceed to log the message and rebroadcast if it's not a heartbeat
	if msg.Text != "" {
		fmt.Printf("Message from %s: %s\n", s.Conn().RemotePeer(), msg.Text)
		// Rebroadcast message to other peers
		broadcastMessage(msg)
	}
}

func broadcastMessage(msg Message) {
	peers := getPeers()
	for _, pAddr := range peers {
		pInfo, err := peer.AddrInfoFromP2pAddr(multiaddr.StringCast(pAddr))
		if err != nil {
			log.Println("Failed to parse multiaddr:", err)
			continue
		}

		if pInfo.ID == selfHost.ID() {
			continue
		}

		selfHost.Peerstore().AddAddrs(pInfo.ID, pInfo.Addrs, peerstore.PermanentAddrTTL)
		s, err := selfHost.NewStream(context.Background(), pInfo.ID, echoProtocolID)
		if err != nil {
			log.Println("Failed to open stream:", err)
			continue
		}
		defer s.Close()

		if err := json.NewEncoder(s).Encode(&msg); err != nil {
			log.Println("Failed to send message:", err)
		}
	}
}

func printNumberOfPeers() {
	knownPeers.RLock()
	defer knownPeers.RUnlock()

	numPeers := len(knownPeers.list)
	fmt.Printf("Number of known peers: %d\n", numPeers)
}

func broadcastHeartbeat(ctx context.Context) {
	// log.Printf("Sending heartBeat")
	peers := getPeers()
	msg := Message{
		ID:    uuid.New().String(),
		Text:  "Heartbeat",
		Peers: peers,
	}

	for _, pAddr := range peers {
		if pAddr == selfAddr {
			continue
		}

		pInfo, err := peer.AddrInfoFromP2pAddr(multiaddr.StringCast(pAddr))
		if err != nil {
			log.Println("Failed to parse multiaddr:", err)
			continue
		}

		selfHost.Peerstore().AddAddrs(pInfo.ID, pInfo.Addrs, peerstore.PermanentAddrTTL)
		s, err := selfHost.NewStream(ctx, pInfo.ID, echoProtocolID)
		if err != nil {
			log.Printf("Heartbeat failed to %s, removing peer: %s\n", pInfo.ID.String(), pAddr)
			removePeer(pAddr)
			continue
		}
		defer s.Close()

		if err := json.NewEncoder(s).Encode(&msg); err != nil {
			log.Printf("Failed to send heartbeat to %s: %s\n", pInfo.ID.String(), pAddr)
		}
	}
	// printNumberOfPeers()
}

func readInput(ctx context.Context) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter a message or multiaddress (or 'exit' to quit): \n")
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "exit" {
			return
		}

		// Check if input is a multiaddress
		if ma, err := multiaddr.NewMultiaddr(input); err == nil {
			peerInfo, err := peer.AddrInfoFromP2pAddr(ma)
			if err != nil {
				fmt.Println("Error extracting peer info:", err)
				continue
			}
			selfHost.Connect(ctx, *peerInfo)
			addPeer(input)
			fmt.Println("Connected to:", input)
		} else if input == "peers" {
			printNumberOfPeers()
		} else { // Broadcast input as a message
			msg := Message{
				ID:   uuid.New().String(),
				Text: input,
			}
			broadcastMessage(msg)
		}
	}
}

func addPeer(peerAddr string) bool {
	knownPeers.Lock()
	defer knownPeers.Unlock()

	if _, exists := knownPeers.list[peerAddr]; !exists && peerAddr != selfAddr {
		knownPeers.list[peerAddr] = struct{}{}
		return true
	}
	return false
}

func removePeer(peerAddr string) {
	knownPeers.Lock()
	defer knownPeers.Unlock()
	if _, exists := knownPeers.list[peerAddr]; exists {
		delete(knownPeers.list, peerAddr)
	}
}

func getPeers() []string {
	knownPeers.RLock()
	defer knownPeers.RUnlock()

	var peers []string
	for p := range knownPeers.list {
		peers = append(peers, p)
	}
	return peers
}
