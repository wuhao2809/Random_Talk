package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	golog "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"
)

// global peer list
var knownPeers = struct {
	sync.RWMutex
	list map[string]struct{}
}{list: make(map[string]struct{})}

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

	startListener(ctx, ha)

	go readInput(ctx, ha)

	<-ctx.Done()
}

func makeBasicHost(listenPort int, insecure bool, randseed int64) (host.Host, error) {
	var r io.Reader
	r = rand.Reader

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
	// Build host multiaddress
	hostAddr, _ := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s", ha.ID()))

	// Now we can build a full multiaddress to reach this host
	// by encapsulating both addresses:
	addr := ha.Addrs()[0]
	return addr.Encapsulate(hostAddr).String()
}

func startListener(ctx context.Context, ha host.Host) {
	fullAddr := getHostAddress(ha)
	log.Printf("I am %s\n", fullAddr)

	ha.SetStreamHandler("/echo/1.0.0", func(s network.Stream) {
		log.Println("Received new stream")
		go func() {
			defer s.Close()
			if err := doEcho(s); err != nil {
				log.Println("Error handling echo:", err)
				s.Reset()
			}
		}()
	})
}

func doEcho(s network.Stream) error {
	buf := bufio.NewReader(s)
	str, err := buf.ReadString('\n')
	if err != nil {
		return err
	}

	// Add the peer to the known peers list
	remotePeer := s.Conn().RemoteMultiaddr().String() + "/p2p/" + s.Conn().RemotePeer().String()
	addPeer(remotePeer)

	log.Printf("Echoing back: %s", str)
	_, err = s.Write([]byte(str))
	return err
}

func readInput(ctx context.Context, ha host.Host) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter the multiaddress of the peer to send an echo message to: ")
		addrStr, err := reader.ReadString('\n')
		if err != nil {
			log.Println("Error reading input:", err)
			continue
		}
		addrStr = strings.TrimSpace(addrStr)
		if addrStr == "" {
			continue
		}

		err = sendEcho(ctx, ha, addrStr)
		if err != nil {
			log.Println("Error sending echo:", err)
		}
	}
}

func sendEcho(ctx context.Context, ha host.Host, addrStr string) error {
	addr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		return err
	}

	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return err
	}

	ha.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)
	s, err := ha.NewStream(ctx, info.ID, "/echo/1.0.0")
	if err != nil {
		return err
	}
	defer s.Close()

	log.Println("Sending echo to:", info.ID)
	_, err = s.Write([]byte("Hello, world!\n"))
	if err != nil {
		return err
	}

	out, err := io.ReadAll(s)
	if err != nil {
		return err
	}

	log.Printf("Received echo reply: %q\n", out)

	// Add the peer to the known peers list
	addPeer(addrStr)
	return nil
}

// Add a peer to the global known peers list and print the list
func addPeer(peerAddr string) {
	knownPeers.Lock()
	defer knownPeers.Unlock()

	knownPeers.list[peerAddr] = struct{}{}
	log.Println("Known peers:")
	for addr := range knownPeers.list {
		log.Printf(" - %s\n", addr)
	}
}
