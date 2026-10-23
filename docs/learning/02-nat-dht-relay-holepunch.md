# 2. NAT, the DHT, relays and hole punching

Note 1 got two peers talking when they could already reach each other (same network, mDNS). This note is about the internet, where usually neither side can simply dial the other. Everything here lives in [internal/p2p/node.go](../../internal/p2p/node.go) (the app's node) and [internal/helper/helper.go](../../internal/helper/helper.go) (the helper we run on a small server).

## Why home routers block inbound connections

A home router gives every device a private address (`192.168.x.x`) and shares one public IP between them. This is **NAT** (network address translation). When your laptop opens a connection out, the router writes down "this outgoing flow belongs to the laptop" so replies can find their way back. A packet that arrives *unasked*, with no matching entry, has nowhere to go and is dropped. So your friend can't dial you, and you can't dial them. Mobile networks often add a second layer (carrier-grade NAT), which is worse. The node asks the router to open a port with UPnP/NAT-PMP (`libp2p.NATPortMap()`), which sometimes works, but we can't count on it.

## AutoNAT: "am I reachable?"

A node can't tell from the inside whether it is behind a NAT. **AutoNAT** asks other peers to try dialing it back at its addresses. If dial-backs work it is *public*, if they fail it is *private*. The helper offers this service (`libp2p.EnableNATService()`); the app uses libp2p's built-in AutoNAT client and shows the result in `/status` as `reachability=public|private|unknown`. Tests skip the guessing with `ForcePrivate`.

## The private DHT: finding a peer by its ID

A contact card carries a peer ID and a few addresses, but addresses go stale. To find a peer from its ID alone we use a **Kademlia DHT** (distributed hash table): every peer that can accept connections keeps a small routing table of other peers, arranged by XOR distance between IDs. `FindPeer(id)` repeatedly asks the peers it knows that are "closest" to `id` until one of them knows the peer's current addresses. Ours is private: the protocol prefix `/twocan` gives the protocol `/twocan/kad/1.0.0`, so we never mix with the public IPFS DHT. The helper is always a DHT *server*; the app runs in auto mode and only becomes a server once AutoNAT says it is public. The helper is also the bootstrap peer: the first one every node connects to. When asked for a peer it is connected to, the helper can answer with that peer's addresses, which is how a NATed peer is found. `Reach` tries the addresses already in the peerstore first, and only then `FindPeer`.

## Circuit relay v2: a meeting point

If you are private, the addresses you advertise are useless to others. So the node asks the helper for a **reservation** (libp2p's *autorelay*, given our bootstrap list as its static relays). The helper agrees to forward connections to you, and you advertise an address like `/ip4/<helper-ip>/tcp/4001/p2p/<helper-id>/p2p-circuit`. Others dial that address, the helper opens a circuit to you over the connection you already hold open to it, and bytes flow through it. Relays are a shared, donated resource, so v2 sets limits. libp2p's defaults are 2 minutes and 128 KiB per relayed connection; our helper raises them to **30 minutes and 4 MiB in each direction** (`helper.Resources()`), after which the helper resets the connection. The next queued message, or the presence sweep every 2 minutes, then dials again. libp2p marks relayed connections as *limited* and refuses to open streams on them unless the caller opts in, so every Twocan stream is opened with `network.WithAllowLimitedConn(ctx, "twocan")`. `/contacts` shows `online relayed` for these.

## DCUtR: hole punching

Relaying works, but everything then goes through the helper. **DCUtR** (Direct Connection Upgrade through Relay, `libp2p.EnableHolePunching()`) tries to do better. Over the relayed connection the two peers swap the public addresses their routers use and time a round trip. Then both dial each other *at the same moment*. Each router sees an outgoing packet first, writes down the flow, and so lets the other side's packets in. If it works you get a direct connection and `/contacts` shows `online direct`. It often works with ordinary home routers and usually fails when either side is behind a "symmetric" or carrier-grade NAT. Then you stay on the relay, which is slower but still works.

## Why the relay can't read your messages

The helper only forwards bytes. A and B run their security handshake (Noise or TLS 1.3, as in note 1) end to end *through* the circuit, each proving the key behind its peer ID. All the helper ever sees is ciphertext. It can't read or change messages, and it can't pretend to be B, because it doesn't hold B's private key. It does see metadata: which two peer IDs are talking, when, and roughly how much. It stores none of that; its only file is its own key.

## How we test it

[internal/core/relay_test.go](../../internal/core/relay_test.go) starts a helper on loopback, a node A, and a node B that listens on nothing and is forced private. B has to get a relay reservation. A gets a card with B's peer ID only, so it must use the DHT. It then adds B, B accepts, a message arrives, and A's connection to B must be relayed. One trick: autorelay only builds circuit addresses from a relay's *public* addresses, so the test helper also advertises a fake `/ip4/1.2.3.4/tcp/4001`. It is never dialled; A and B reuse their existing loopback connections to the helper.

---

## Owner checklist: deploy the helper and test with a friend

These are manual steps; nothing in the repo does them for you.

1. **Build** on your machine (static binary; add `GOARCH=arm64` for an ARM VPS):
   `CGO_ENABLED=0 go build -o twocan-helper ./cmd/twocan-helper`
2. **Copy** to the VPS. Use a VPS whose public IP sits on its own network interface (most cheap VPSs do); the helper advertises the addresses it sees on its interfaces.
   `scp twocan-helper deploy/twocan-helper.service you@vps:`
   then on the VPS:
   `sudo install -m 0755 twocan-helper /usr/local/bin/twocan-helper`
   `sudo install -m 0644 twocan-helper.service /etc/systemd/system/twocan-helper.service`
3. **Open port 4001 for TCP and UDP**, both in the VPS firewall (e.g. `sudo ufw allow 4001/tcp && sudo ufw allow 4001/udp`) and in the provider's web firewall if it has one.
4. **Start** it: `sudo systemctl daemon-reload && sudo systemctl enable --now twocan-helper`. The key lives in `/var/lib/twocan-helper/helper.key`, so the peer ID stays the same across restarts.
5. **Copy the addresses** it printed at startup: `journalctl -u twocan-helper -b | grep /p2p/`. Keep the lines with the public IP, for example `/ip4/<public-ip>/tcp/4001/p2p/<id>` and `/ip4/<public-ip>/udp/4001/quic-v1/p2p/<id>`. Ignore the `127.0.0.1` and private-range lines.
6. **Point the app at it.** Either put those strings in `DefaultBootstrap` in [internal/p2p/bootstrap.go](../../internal/p2p/bootstrap.go) and rebuild `twocan-cli`, or pass them at run time: `twocan-cli --bootstrap '<addr1>,<addr2>' --name …`.
7. **Friend test** (both on Linux, on *different* networks, e.g. one on phone tethering):
   - Each runs `twocan-cli --name <you>` (plus `--bootstrap …` if step 6 used the flag).
   - `/status` should show `reachability=private` (or `public`) after a minute; `unknown` means AutoNAT has not decided yet.
   - One side prints `/card` and sends it to the other over any channel. The other runs `/add <card>`, the first runs `/accept <name>`, and you chat with `/msg <name> <text>`.
   - Run `/contacts`. Your friend's line shows `online direct` (hole punch worked, or one side is public) or `online relayed` (going through the helper). Check again after ~30 s, since a hole punch can upgrade a relayed connection.
8. **Record the result** below.

**Real-network result:** not run yet (owner: fill in date, the two network types, and `direct` or `relayed`).
