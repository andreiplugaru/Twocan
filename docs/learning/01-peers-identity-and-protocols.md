# 1. Peers, identity and protocols

This note explains the ideas behind the first runnable Twocan (the CLI). There is no server: every copy of the app is a *peer* that talks straight to other peers.

## A peer ID is a public key

When Twocan first runs it creates an Ed25519 key pair and saves the private half in `identity.key` ([internal/identity/key.go](../../internal/identity/key.go)). Your **peer ID** is a hash-based encoding of the public key (the `12D3KooW...` string). Nobody assigns it and nobody can take it; it is simply who you are.

**Why it can't be spoofed.** Every connection starts with a *Noise* handshake ([internal/p2p/node.go](../../internal/p2p/node.go) builds the libp2p host). Both sides prove they hold the private key behind the peer ID they claim, and then all traffic is encrypted with keys derived from that exchange. When you dial a peer ID, you either reach the holder of that key or the handshake fails. So a *contact card* ([internal/identity/card.go](../../internal/identity/card.go)) only needs to carry a peer ID plus a few addresses: addresses are hints for finding someone, the peer ID is the authentication.

## The peerstore

libp2p keeps an address book called the **peerstore**: for each peer ID, the addresses where it was seen. Dialing "peer X" means "look X up in the peerstore and try those addresses". On startup `core` copies each stored contact's addresses into it ([internal/core/node.go](../../internal/core/node.go), `startWith`), and the card you scan or paste seeds it for a new contact.

## Finding each other: mDNS

On the same network, peers can announce themselves with **mDNS** (multicast DNS, the same trick printers use). With `--mdns` on, the node broadcasts the service name `twocan` and listens for others ([internal/p2p/node.go](../../internal/p2p/node.go)). Found peers go into the peerstore. Across the internet that does not work, which is what the later milestone (DHT, relays) is for.

## Stream protocols and frames

A connection between two peers carries many independent **streams**. Each stream starts by naming a **protocol ID** (`/twocan/contact/1.0.0`, `/twocan/msg/1.0.0`), and the receiver routes it to the handler registered for that name. The name is versioned, so a future `1.1.0` can coexist with the old one.

On a stream we send **frames**: a varint length, then a protobuf message, at most 64 KiB ([internal/wire/frame.go](../../internal/wire/frame.go), schemas in [internal/proto](../../internal/proto)). The length prefix is how the reader knows where one message ends, since a stream is just bytes. The size cap stops a hostile peer from making us allocate gigabytes. Contact requests/accepts are handled in [internal/contacts/service.go](../../internal/contacts/service.go), chat in [internal/chat/service.go](../../internal/chat/service.go).

## Not losing messages: outbox, ack, dedupe

Peers go offline, so sending is *store first, deliver later*:

1. **Outbox.** `Send` writes the message to SQLite with status `pending` and returns immediately ([internal/store/messages.go](../../internal/store/messages.go)). The CLI prints `queued <id>`.
2. **Delivery loop.** When the contact is reachable (a connection event, a retry tick or the presence sweep in [internal/core/loop.go](../../internal/core/loop.go)) the chat service opens a stream and sends every pending message, retrying with backoff.
3. **Ack.** The receiver stores the message *first*, and only then replies with an acknowledgement. If storing fails, no ack is sent. The sender flips the message to `delivered` only on receiving the ack (`✓ delivered`).
4. **Dedupe.** If the ack is lost, the sender will send the same message again. Every message has a unique ID (a UUIDv7), and the receiver ignores an ID it has already stored (while still acking it so the sender stops).

At-least-once sending plus dedupe on receipt gives effectively **exactly-once** delivery as the user sees it. You can watch this: quit one CLI, send to it, restart it, and the queued messages arrive once each.
