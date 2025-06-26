# Random_Talk

A random talk project prototype using p2p

## Overview

Random_Talk is a peer-to-peer (P2P) prototype application that demonstrates decentralized message broadcasting using the [libp2p](https://libp2p.io/) networking stack in Go. Each node can join the network, discover peers, and broadcast messages in real time, with all communication occurring directly between nodes without a central server.

## Features

- Decentralized P2P network using libp2p
- Peer discovery and management
- Real-time message broadcasting and rebroadcasting
- Heartbeat mechanism to maintain peer lists
- Message deduplication to avoid rebroadcast loops
- Command-line interface for sending messages

## Installation

### Prerequisites

- Go 1.21 or later

### Clone the repository

```bash
git clone <repo-url>
cd Random_talk_CS6650
```

### Install dependencies

```bash
go mod download
```

### Build the project

```bash
go build -o random_walk main.go
```

## Usage

Start a node by specifying a listening port and (optionally) a public IP or DNS name:

```bash
./random_walk -l <port> [--public-ip <your-public-ip-or-dns>]
```

- `-l <port>`: The port to listen on (required)
- `--public-ip <ip-or-dns>`: (Optional) The public IP or DNS name to advertise to peers

### Example

Start two nodes on the same machine (in separate terminals):

```bash
./random_walk -l 9001
./random_walk -l 9002
```

Copy the multiaddress printed by the first node and paste it into the second node's input to connect them.

Once connected, type messages and press Enter to broadcast them to all peers.

## Dependencies

- [libp2p/go-libp2p](https://github.com/libp2p/go-libp2p)
- [ipfs/go-log](https://github.com/ipfs/go-log)
- [google/uuid](https://github.com/google/uuid)
- [multiformats/go-multiaddr](https://github.com/multiformats/go-multiaddr)

(See `go.mod` for the full list of dependencies.)
