package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	golog "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

const heartbeatProtocolID = "/heartbeat/1.0.0"

// global host address
var selfAddr string

// global peer list
var knownPeers = struct {
	sync.RWMutex
	list map[string]struct{}
}{list: make(map[string]struct{})}

// Message includes the text and a slice of known peer addresses
type Message struct {
	Text  string   `json:"text,omitempty"`
	Peers []string `json:"peers"`
}

func isPeerKnown(peerAddr string) bool {
	knownPeers.RLock()
	defer knownPeers.RUnlock()
	_, exists := knownPeers.list[peerAddr]
	return exists
}

func addPeer(peerAddr string) bool {
	knownPeers.Lock()
	defer knownPeers.Unlock()

	if peerAddr == selfAddr {
		return false
	}

	if _, ok := knownPeers.list[peerAddr]; !ok {
		knownPeers.list[peerAddr] = struct{}{}
		fmt.Println("Known peers:")
		for addr := range knownPeers.list {
			fmt.Printf(" - %s\n", addr)
		}
		return true // New peer added
	}
	return false // Peer was already known
}

func getPeers() []string {
	knownPeers.RLock()
	defer knownPeers.RUnlock()

	peers := make([]string, 0, len(knownPeers.list))
	for addr := range knownPeers.list {
		peers = append(peers, addr)
	}
	return peers
}

func removePeer(peerAddr string) {
	knownPeers.Lock()
	defer knownPeers.Unlock()

	if _, exists := knownPeers.list[peerAddr]; exists {
		delete(knownPeers.list, peerAddr)
		fmt.Println("Removed peer:", peerAddr)
		fmt.Println("Updated peers list:")
		for addr := range knownPeers.list {
			fmt.Println(" - ", addr)
		}
	}
}

func printNumberOfPeers() {
	knownPeers.RLock()
	defer knownPeers.RUnlock()

	numPeers := len(knownPeers.list)
	fmt.Printf("Number of known peers: %d\n", numPeers)
}

func sendHeartbeat(ctx context.Context, ha host.Host) {
	log.Println("Sending heartbeat")
	peers := getPeers()

	for _, pAddr := range peers {
		if pAddr == selfAddr {
			continue // Skip self
		}

		pInfo, err := peer.AddrInfoFromP2pAddr(ma.StringCast(pAddr))
		if err != nil {
			log.Printf("Failed to get AddrInfo from P2pAddr: %v", err)
			continue
		}

		ha.Peerstore().AddAddrs(pInfo.ID, pInfo.Addrs, peerstore.PermanentAddrTTL)
		s, err := ha.NewStream(ctx, pInfo.ID, heartbeatProtocolID)
		if err != nil {
			log.Printf("Heartbeat failed to %s, removing peer: %v", pInfo.ID.String(), err)
			removePeer(pAddr) // Removing the peer if the heartbeat fails
			continue
		}

		msg := Message{
			Peers: getPeers(), // Include the current list of known peers in the heartbeat
		}

		if err := json.NewEncoder(s).Encode(&msg); err != nil {
			log.Printf("Failed to send heartbeat to %s: %v", pInfo.ID.String(), err)
		}
		s.Close()
	}

	printNumberOfPeers()
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	golog.SetAllLoggers(golog.LevelInfo)

	listenF := flag.Int("l", 4001, "wait for incoming connections")
	flag.Parse()

	ha, err := makeBasicHost(*listenF, false, 0)
	if err != nil {
		log.Fatal(err)
	}

	selfAddr = getHostAddress(ha)
	fmt.Println("Host created. Multiaddress:", selfAddr)

	startListener(ctx, ha)

	// Separate goroutine for reading input and handling user commands
	go func() {
		readInput(ctx, ha)
	}()

	// Start the heartbeat goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendHeartbeat(ctx, ha)
			}
		}
	}()
	// Wait for the context to be done.
	<-ctx.Done()
}

func makeBasicHost(listenPort int, insecure bool, randseed int64) (host.Host, error) {
	var r io.Reader = rand.Reader

	priv, _, err := crypto.GenerateKeyPairWithReader(crypto.RSA, 2048, r)
	if err != nil {
		return nil, err
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)),
		libp2p.Identity(priv),
	}

	return libp2p.New(opts...)
}

func getHostAddress(ha host.Host) string {
	hostAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s", ha.ID()))
	addr := ha.Addrs()[0]
	return addr.Encapsulate(hostAddr).String()
}

func startListener(ctx context.Context, ha host.Host) {
	// Heartbeat message handler
	ha.SetStreamHandler(heartbeatProtocolID, func(s network.Stream) {
		defer s.Close()
		var msg Message
		if err := json.NewDecoder(s).Decode(&msg); err != nil {
			log.Printf("Failed to decode heartbeat message: %v", err)
			return
		}
		log.Println("Heartbeat received")

		// Update known peers list based on the received heartbeat message
		for _, peerAddr := range msg.Peers {
			if addPeer(peerAddr) {
				log.Printf("New peer from heartbeat added: %s", peerAddr)
			}
		}
	})

	ha.SetStreamHandler("/echo/1.0.0", func(s network.Stream) {
		defer s.Close()
		var msg Message
		if err := json.NewDecoder(s).Decode(&msg); err != nil {
			log.Printf("Failed to decode message: %v", err)
			return
		}

		// Extract the sender's full multiaddress
		senderAddr := s.Conn().RemoteMultiaddr().String() + "/p2p/" + s.Conn().RemotePeer().String()

		// Add the sender to the known peers if it's new
		addPeer(senderAddr)

		// Merge received peers list with known peers, and broadcast if new peers are discovered
		// for _, peerAddr := range msg.Peers {
		// 	addPeer(peerAddr)
		// }

		// // If a new peer was discovered (either the sender or from the received peers list), broadcast the updated peers list
		// if newPeerAdded || newPeersDiscovered {
		// 	log.Printf("Going to broadcast")
		// 	broadcastPeers(ctx, ha)
		// 	log.Printf("Broadcast ends")
		// }

		// Prepare and send an echo response if the message contains text
		if msg.Text != "" {
			log.Printf("Message received from %s: %s", senderAddr, msg.Text)
			response := Message{Text: "Echo: " + msg.Text, Peers: getPeers()}
			if err := json.NewEncoder(s).Encode(&response); err != nil {
				log.Printf("Failed to send echo response: %v", err)
			}
		}
	})
}

// func broadcastPeers(ctx context.Context, ha host.Host) {
// 	peers := getPeers()
// 	msg := Message{Peers: peers}

// 	for _, pAddr := range peers {
// 		pInfo, err := peer.AddrInfoFromP2pAddr(ma.StringCast(pAddr))
// 		if err != nil {
// 			log.Printf("Failed to get AddrInfo from P2pAddr: %v", err)
// 			continue
// 		}

// 		if pInfo.ID == ha.ID() {
// 			continue // Skip self
// 		}

// 		ha.Peerstore().AddAddrs(pInfo.ID, pInfo.Addrs, peerstore.PermanentAddrTTL)

// 		s, err := ha.NewStream(ctx, pInfo.ID, "/echo/1.0.0")
// 		if err != nil {
// 			log.Printf("Failed to open stream to %s: %v", pInfo.ID, err)
// 			continue
// 		}

// 		if err := json.NewEncoder(s).Encode(&msg); err != nil {
// 			log.Printf("Failed to broadcast peers to %s: %v", pInfo.ID, err)
// 		}
// 		s.Close()
// 	}
// }

func readInput(ctx context.Context, ha host.Host) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter the multiaddress of the peer to send an echo message to: ")
		addrStr, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Error reading input: %v", err)
			continue
		}
		addrStr = strings.TrimSpace(addrStr)
		if addrStr == "" {
			continue
		}

		// Directly send the echo without waiting for user text input
		sendEcho(ctx, ha, addrStr, "Hello, world!")
	}
}

func sendEcho(ctx context.Context, ha host.Host, addrStr, text string) {
	addr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		log.Printf("Error parsing multiaddr: %v", err)
		return
	}

	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		log.Printf("Error extracting peer info: %v", err)
		return
	}

	ha.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	s, err := ha.NewStream(ctx, info.ID, "/echo/1.0.0")
	if err != nil {
		log.Printf("Error opening stream: %v", err)
		return
	}
	defer s.Close()

	msg := Message{Text: text}
	if err := json.NewEncoder(s).Encode(&msg); err != nil {
		log.Printf("Error sending message: %v", err)
	}

	// Add the peer to the known peers list
	if !isPeerKnown(addrStr) {
		addPeer(addrStr)
	}
}
