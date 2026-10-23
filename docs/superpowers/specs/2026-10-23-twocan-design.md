# Twocan — P2P Desktop Messenger: Design

Date: 2026-10-23
Status: draft, awaiting owner approval
Replaces: `docs/p2p-messaging-voice-go-build-plan.md` (larger voice + groups plan, retired)

## Intent

Twocan is a peer-to-peer text messenger for the desktop, written in Go, with a
windowed UI in the style of Skype. It is a **learning project**: the goal is to
understand how a real P2P app fits together, using go-libp2p for the
networking rather than hand-writing NAT traversal, DHTs or crypto.

The one hard requirement: **no server ever holds message data.** Messages and
history exist only on the two participants' computers.

Small helper infrastructure is acceptable, as long as it only introduces peers
and relays bytes it cannot read, and stores nothing about conversations.

Throughout this document, **(owner)** marks decisions the owner made and
**(my call)** marks decisions the designer made on the owner's behalf, which the
owner may still overturn.

## Goals (v1)

1. Two people on different home networks can exchange text messages directly,
   or through the relay when a direct connection is impossible.
2. Messages persist locally and survive restarts on both sides.
3. A message written while the recipient is offline is delivered automatically
   once both are online at the same time, exactly once.
4. People add each other by QR code (owner): the app shows your card as a QR
   code, and adds a contact by decoding a QR code from an image file or a
   pasted screenshot (owner, option "a"). Copy/paste of the card text works
   too (my call). Nobody can message you until you accept their contact
   request (my call).
5. The code is small and clear enough for the owner to understand every part.

## Non-goals (v1)

- Voice or video calls, group chats, file or image attachments.
- Usernames, user search, or any global name registry.
- Multi-device sync: each install is its own identity with its own history.
- Store-and-forward mailbox: offline delivery relies only on the sender's outbox.
- Signal-protocol / Double Ratchet encryption (see Security).
- Encryption of the local history file (my call: deferred).
- Mobile or browser clients.
- Live webcam QR scanning (owner: stretch goal at most, not required for v1).
- macOS and Windows support (owner: Linux only for now).
- Installers, auto-update, code signing.
- Read receipts, typing indicators, edits, deletes, reactions.

## Anti-goals (failure even if it "works")

- Any server, including our own helper, persists messages, contact lists or
  conversation metadata beyond what is in memory for active connections.
- The helper can read message content.
- Scope growth that stops the project from reaching a working v1, such as
  pulling in calls, groups or a mailbox before text chat works end to end.

## Constraints

- **Language:** Go for both the desktop app and the helper (owner).
- **Networking:** go-libp2p (owner). Use the current stable release; at time of
  writing, go-libp2p v0.50 requires Go 1.26. The builder verifies versions.
- **UI:** Fyne (my call; the owner wants a windowed, Skype-like app and named no
  toolkit). Fyne requires cgo and a C toolchain; that is accepted.
- **Platforms:** Linux only for v1 (owner). The owner is on Manjaro. Avoid
  gratuitously Linux-specific code, but don't spend effort on other platforms.
- **Who builds it:** Claude writes the code, test-first, and the owner reads
  along to learn (owner). The plan should therefore include short explanations
  of each libp2p concept as it's introduced (what the DHT, relay, AutoNAT and
  hole punching are doing and why), in code comments where they belong and
  in a `docs/learning/` note per milestone (my call on format).

## Architecture

```
┌──────────────── Twocan desktop app (one Go process) ────────────────┐
│  UI (Fyne)  ──calls──▶  core API  ◀──events── chat / contacts       │
│                            │                                         │
│                 store (SQLite)     p2p (libp2p host: Noise/TLS,      │
│                                    QUIC+TCP, DHT, mDNS, AutoRelay,   │
│                                    hole punching)                    │
└──────────────────────────────┬──────────────────────────────────────┘
                               │ libp2p streams (encrypted)
          ┌────────────────────┼──────────────────────┐
          ▼                    ▼                      ▼
   other Twocan peer    twocan-helper            (direct when possible,
                        DHT bootstrap,            relayed otherwise)
                        circuit relay v2,
                        AutoNAT server
```

**Rule:** the UI only talks to the core API and renders events. All logic
lives in the core, so a headless CLI client can drive the same code for testing
and debugging.

### Components

| Package (suggested) | Responsibility |
| --- | --- |
| `cmd/twocan` | Fyne desktop app entry point |
| `cmd/twocan-cli` | Headless client: same core, line-based commands. Used for development and integration tests (my call) |
| `cmd/twocan-helper` | Helper node: DHT bootstrap (server mode), circuit relay v2, AutoNAT |
| `internal/core` | Public-ish facade: `Start`, `Stop`, `MyCard`, `AddContact(card)`, `AcceptContact`, `RejectContact`, `Send(peer, text)`, `Contacts`, `History(peer)`, `Subscribe() <-chan Event` |
| `internal/identity` | Key pair generation/loading, contact card encode/decode |
| `internal/p2p` | libp2p host construction, DHT, mDNS, relay/hole-punch config, finding and dialing contacts |
| `internal/contacts` | Contact request protocol and contact state machine |
| `internal/chat` | Message protocol, outbox, acks, dedupe |
| `internal/store` | SQLite access and migrations |
| `internal/proto` | `.proto` definitions and generated code |
| `internal/ui` | Fyne widgets and screens |

The exact package split is left to the builder; the separation of core and UI
is not.

## Identity

- On first launch, generate an **Ed25519** key pair. The libp2p peer ID derived
  from it is the user's identity and address.
- Store the private key in a file in the user config directory
  (`<data-dir>/identity.key`, mode 0600; see Local storage) (my call: simpler than
  an OS keychain for a learning project).
- On first launch, the user also picks a **display name**. It is self-asserted
  and not unique; the peer ID is the real identity. Each user can override a
  contact's name with a local nickname.
- Losing the key file means losing the identity. That is acceptable for v1.

## Contact card and adding contacts

A **contact card** is a short string such as `twocan:<base32 payload>`. The
payload is a protobuf containing:

- protocol version
- peer ID
- display name
- up to a few last-known multiaddrs as hints (optional; the DHT is the real
  lookup)

The QR code encodes exactly this string. The UI offers:

- **Show my card:** displays the QR code and a "Copy" button.
- **Add contact:** paste a card's text, open an image file containing a QR
  code, or paste a QR screenshot from the clipboard.

**Request flow (my call):**

1. A adds B's card. B is stored on A's side as `pending_outgoing`.
2. A's app reaches B (see Connectivity) and sends a `ContactRequest` (A's
   display name) over `/twocan/contact/1.0.0`. If B is offline, the request
   waits and is retried exactly like a queued message.
3. B's app stores A as `pending_incoming` and shows the request.
4. B accepts → B sends `ContactAccept`; both sides mark each other `accepted`.
   B rejects → B stores A as `blocked` and sends nothing further. A stays at
   `pending_outgoing` indefinitely (no rejection signal, so a rejection is not
   revealed).
5. Chat messages from any peer that is not `accepted` are dropped unread.
   Contact requests are rate-limited per peer ID.

**Edge cases (my call):**

- `ContactAccept` is delivered reliably: if A is offline when B accepts, B
  stores a pending accept and retries it on reconnect, like a queued message.
  A replies with an ack.
- If both people add each other at the same time, an incoming `ContactRequest`
  from a peer already in `pending_outgoing` is treated as mutual consent: both
  sides go straight to `accepted`.
- A `ContactRequest` from a peer already `accepted` (for example after the
  requester reinstalled their database but kept their key) is answered with
  `ContactAccept` again.

Because libp2p authenticates every connection by peer ID, a card cannot be used
to impersonate its owner.

**QR handling (owner chose image-based, not webcam):** generate with a pure-Go
encoder (for example `skip2/go-qrcode`) and decode from an image with a pure-Go
decoder (for example `gozxing`). Remote friends send each other a screenshot of
their QR code (or the text) over any channel. Live webcam scanning is a stretch
goal outside v1.

## Connectivity

### libp2p host (client)

Enable, at minimum:

- Transports: QUIC and TCP.
- Security: libp2p defaults (Noise and TLS 1.3), which authenticate peer IDs.
- `NATPortMap` (UPnP/NAT-PMP), `EnableHolePunching` (DCUtR),
  `EnableAutoRelayWithStaticRelays` pinned to the helper, AutoNAT client.
- Kademlia DHT in a **private network**: app-specific protocol prefix
  (for example `/twocan`), bootstrapped only from our helper(s), so it is not
  part of the public IPFS DHT (my call: keeps users' peer IDs and IPs out of
  public crawlers). Clients run in auto mode.
- mDNS for peers on the same LAN (my call: lets two machines at home chat
  without the helper and makes early testing easy).
- Connection manager and resource manager with explicit, modest limits.

### Finding a contact

To reach an accepted (or pending) contact, try in order:

1. Already connected → use the connection.
2. Cached last-known addresses from the store.
3. mDNS results.
4. DHT `FindPeer(peerID)`. Peers behind NAT will have published
   `/p2p-circuit` addresses through their relay reservation, so this also finds
   them via the relay.

On success, update cached addresses. libp2p's DCUtR then tries to upgrade a
relayed connection to a direct one; if that fails, staying on the relay is fine.

**Reconnect loop:** while the app runs, periodically attempt to reach every
contact that has queued items, with exponential backoff (for example 5 s
doubling to a 5 min cap; exact timings are the builder's call). Also react
immediately to libp2p "peer connected" events by flushing that peer's outbox.

Expect roughly 3 in 10 peer pairs to stay relayed, so relayed chat must
work fully. Large measurements of libp2p hole punching report about 70%
success.

### Helper node (`twocan-helper`)

One Go binary run on a small VPS by the owner. It:

- Runs the DHT in **server mode** as the bootstrap node.
- Runs **circuit relay v2** with limits raised for chat. go-libp2p's defaults
  reset relayed connections after about 2 minutes or 128 KB per direction,
  which is enough for a hole punch but not a conversation. Use limits on the
  order of 30+ minutes and a few MB per circuit, with per-peer and per-IP
  reservation caps so it can't become a free proxy. Exact values are the
  builder's call.
- Runs an **AutoNAT** server so clients learn whether they're reachable.
- Persists **only its own key**, so its peer ID (and thus the bootstrap
  address) never changes. It writes nothing else to disk and logs no peer IDs
  at default log level.
- Is configured with listen addresses and its key file path via flags.

Clients get the helper's multiaddr from a default compiled into the app,
overridable via config file or flag (my call). v1 runs one helper node.

The relay never sees plaintext. A relayed libp2p connection carries the
peers' own Noise/TLS session end to end, so the relay only forwards encrypted
bytes.

## Messaging protocol

### Wire format

- Stream protocols: `/twocan/contact/1.0.0` and `/twocan/msg/1.0.0`.
- Frames: varint length-delimited protobuf (for example
  `google.golang.org/protobuf/encoding/protodelim`) (my call). Cap a frame at
  64 KB; reject larger.
- Version every protocol ID; a breaking change gets a new ID.

### Messages

```proto
message ChatFrame {
  oneof body {
    ChatMessage message = 1;
    Ack ack = 2;
  }
}

message ChatMessage {
  bytes  id = 1;        // UUIDv7, generated by sender
  int64  sent_at = 2;   // sender wall clock, unix millis
  string text = 3;      // UTF-8, max 16 KB (my call)
}

message Ack {
  bytes id = 1;         // id of the ChatMessage received
}
```

Contact protocol messages (`ContactRequest{display_name}`,
`ContactAccept{display_name}`) are defined similarly; their shape is left to
the builder.

### Delivery guarantees

- **Send:** writing a message inserts it into the store with status `pending`
  in one transaction, then attempts delivery.
- **Receive:** the receiver stores the message, **deduplicating by
  (`peer_id`, `id`)**,
  commits, *then* sends `Ack`. A crash before the ack causes a resend, which
  is deduplicated.
- **Ack received:** sender marks the message `delivered`.
- Result: at-least-once on the wire, exactly-once for the user.
- **Outbox flush:** on connection to a contact, send all their `pending`
  messages oldest first.
- **Ordering:** a conversation is displayed sorted by (`sent_at`, `id`) (my
  call). With exactly two participants, wall-clock skew causes at most minor
  misordering, which is acceptable for v1.
- The UI shows each sent message's status: pending (clock icon) or delivered
  (tick).

## Local storage

All per-user state (identity key, database, settings) lives in one data
directory, defaulting to `os.UserConfigDir()/twocan/` and overridable with a
`--data-dir` flag on both the app and the CLI, so several peers can run on one
machine for testing (my call). One SQLite file at `<data-dir>/twocan.db`, using
`modernc.org/sqlite` (pure Go) (my call). WAL mode; migrations embedded and run
at startup.

| Table | Columns (indicative) |
| --- | --- |
| `contacts` | `peer_id` PK, `display_name` (theirs), `nickname` (local, nullable), `state` (`pending_outgoing`, `pending_incoming`, `accepted`, `blocked`), `addrs` (cached multiaddrs), `created_at` |
| `messages` | (`peer_id`, `id`) PK, `peer_id`, `direction` (`in`/`out`), `text`, `sent_at`, `received_at`, `status` (`pending`/`delivered` for outgoing) — index on (`peer_id`, `sent_at`) |
| `settings` | key/value: own display name, helper address override |

The pending contact request is derived from `contacts.state =
pending_outgoing` rather than a separate outbox table.

The database is not encrypted in v1. Anyone with access to the user's account
can read history. Full-disk encryption is the mitigation.

## Desktop UI (Fyne)

Skype-like layout (owner: "a windowed app like Skype"):

- **Left pane:** own display name and a "Show my card" button at the top; the
  contact list below, each row showing name (nickname if set), an online dot
  (green = a libp2p connection to that peer is currently open, grey otherwise),
  and an unread count; an "Add contact" button. Pending incoming requests
  appear at the top with Accept/Reject.
- **Right pane:** conversation with the selected contact. Messages are bubbles
  with timestamps and delivery status. The message entry box and Send button
  are at the bottom (Enter sends).
- **First-run screen:** choose display name.
- **Dialogs:** show my card (QR + copy text + save QR as PNG), add contact
  (paste text field, "Open QR image…" button, "Paste QR from clipboard"
  button), rename contact.
- **Status bar** (my call, for learning value): own reachability (public /
  behind NAT), and for the selected contact whether the connection is direct
  or relayed.

Visual polish is the builder's call within this layout.

## Security model

**Protects against:** network eavesdroppers, the helper/relay operator, and a
compromised helper reading content. libp2p's Noise/TLS encrypts every
connection, including relayed ones, end to end between the two peers and
authenticates both peer IDs.

**Does not protect against:**

- A compromised device or user account (history and key are unencrypted files).
- Metadata: on a direct connection, contacts see each other's IP. The helper
  and DHT see which peer IDs are online and which pairs connect via relay.
- Retroactive decryption if a long-term key is stolen (no forward secrecy
  beyond what each libp2p session provides).

**Why no Signal protocol in v1:** with no mailbox, a message is only ever in
transit inside a live, authenticated, encrypted libp2p session between the two
peers. A Signal-style layer would mainly matter once a third party stores
messages (a future mailbox), and it adds a lot of complexity to a learning v1.

**Hygiene:** never log message text or keys; rate-limit contact requests;
enforce frame size limits; fuzz protobuf decoding (`go test -fuzz`).

## Testing

- **Unit tests:** card encode/decode, contact state machine, outbox/ack/dedupe
  logic, store.
- **Multi-peer tests in-process:** libp2p's `mocknet` to run several peers in
  one test: contact request → accept → message → ack; offline sender queue
  flush on reconnect; duplicate delivery deduped; messages from non-contacts
  dropped.
- **Durability:** kill a peer mid-send (`kill -9` of `twocan-cli`) and verify
  nothing is lost or duplicated after restart.
- **Real-network check (manual):** two machines on different home networks,
  helper on a VPS, verifying both a direct (hole-punched) and a relay-only
  path. The builder may add a Docker/netns NAT lab if it helps; not required.

## Milestones

Each one ends with something runnable.

1. **Two peers, one LAN:** `twocan-cli` creates an identity and finds a peer via
   mDNS; text goes both ways over `/twocan/msg/1.0.0`.
2. **Across the internet:** `twocan-helper` deployed; private DHT, AutoRelay,
   hole punching. Two CLIs on different networks chat; status shows direct or
   relayed.
3. **Contacts and persistence:** cards, contact requests, SQLite, outbox, acks,
   dedupe. Messages survive restarts and queue while offline.
4. **Desktop UI:** Fyne app with the layout above, card display with QR, add
   by pasted text.
5. **QR from images:** add a contact from a QR image file or a clipboard
   screenshot.

Stretch, only after v1 is done: live webcam scanning.

## Success

v1 is done when the owner and a friend, both on Linux and on different home
networks, can run the app, add each other by QR image (or pasted text), chat, close the app, reopen it,
see full history, and have messages sent while one of them was offline arrive
when both are back online. Equally important: the owner can explain how each
part works.

## Left to the builder

- Exact library versions and module layout within the package suggestions above.
- Retry/backoff timings, relay limit values, connection manager limits.
- Contact-protocol message shapes.
- QR encode/decode libraries (pure Go preferred).
- Fyne widget structure and styling.
- Config file format and location of logs.

## Future (not v1, recorded for direction only)

Voice calls (Pion WebRTC, signalled over libp2p), groups, file transfer, an
opt-in ciphertext mailbox (which would bring in Signal-protocol encryption),
encrypted local storage, and multi-device support.
