# Twocan

A peer-to-peer text messenger for the Linux desktop, written in Go.

Messages go straight from your computer to your contact's computer and are stored only on those two machines. No server ever holds your conversations. A small helper server (which you run yourself) only introduces peers to each other and, when a direct connection is impossible, relays encrypted bytes it cannot read.

Twocan v1 is a learning project: one-to-one text chat only. The notes in [`docs/learning/`](docs/learning/) explain how each part works.

## What's in the box

| Binary | What it is |
| --- | --- |
| `twocan` | The desktop app: contacts on the left, conversation on the right |
| `twocan-cli` | The same thing in a terminal, handy for testing and scripting |
| `twocan-helper` | The helper server: peer discovery (private DHT), relay, reachability checks |

## Download

Grab the binaries from the [latest release](https://github.com/andreiplugaru/Twocan/releases/latest):

| File | For |
| --- | --- |
| `twocan-linux-amd64` | Desktop app (x86-64). Needs the usual desktop OpenGL, X11, Wayland and xkbcommon libraries, which are already installed on any Linux desktop |
| `twocan-cli-linux-amd64`, `twocan-cli-linux-arm64` | Terminal client (static, no dependencies) |
| `twocan-helper-linux-amd64`, `twocan-helper-linux-arm64` | Helper server (static), plus `twocan-helper.service` |
| `SHA256SUMS` | Checksums: `sha256sum -c SHA256SUMS --ignore-missing` |

```bash
chmod +x twocan-linux-amd64
./twocan-linux-amd64
```

If the release was built with a helper address (see [Releases](#releases)), the binaries already know where your helper is, so no `--bootstrap` flag is needed.

## Build

To build from source instead, you need Go 1.27+ and, for the desktop app, a C compiler and the OpenGL, X11 and Wayland development libraries that [Fyne](https://fyne.io) uses.

```bash
# Manjaro / Arch
sudo pacman -S go gcc libxcursor libxrandr libxinerama libxi libxxf86vm mesa wayland libxkbcommon
# Debian / Ubuntu
sudo apt install golang gcc libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

go build -o bin/twocan ./cmd/twocan
go build -o bin/twocan-cli ./cmd/twocan-cli
CGO_ENABLED=0 go build -o bin/twocan-helper ./cmd/twocan-helper
```

To paste a QR screenshot from the clipboard you also need `wl-clipboard` (Wayland) or `xclip` (X11).

## Quick start: two people on the same network

On the same Wi-Fi or LAN, peers find each other automatically, so no helper is needed.

1. Both of you run `bin/twocan` and pick a display name on first launch.
2. One of you clicks **My card** and sends the card to the other: copy the text, or save the QR code as a PNG.
3. The other clicks **Add contact** and pastes the text, opens the QR image, or pastes a QR screenshot from the clipboard.
4. The first person sees **wants to add you** and clicks **Accept**.
5. Chat. 🕓 means the message is waiting; ✓ means it was delivered.

Messages you send while your contact is offline wait on your computer and go out automatically when you're both online.

## Chatting across the internet

People on different home networks need the helper server so they can find each other and fall back to a relay when their routers block a direct connection.

1. **Run the helper** on a small VPS with a public IP. You need to open TCP and UDP port 4001. Full step-by-step instructions, including the systemd unit in [`deploy/twocan-helper.service`](deploy/twocan-helper.service), are in [docs/learning/02](docs/learning/02-nat-dht-relay-holepunch.md).
2. **Point the apps at it.** The helper prints addresses like `/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...`. You can either:
   - pass them on every launch: `bin/twocan --bootstrap /ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...,/ip4/203.0.113.7/udp/4001/quic-v1/p2p/12D3KooW...`, or
   - bake them into the binaries so no flag is needed: set the `TWOCAN_BOOTSTRAP` repository variable and cut a release (see [Releases](#releases)), or build locally with `-ldflags "-X twocan/internal/p2p.bootstrapAddrs=<addr1>,<addr2>"`.
3. Add each other with your cards as above. The status bar shows whether you're reachable and whether the open conversation is **direct** or **via relay**.

## Command-line client

```bash
bin/twocan-cli --name Ana                      # first run: --name is required
bin/twocan-cli --data-dir /tmp/ben --name Ben  # a second identity on the same machine
```

| Command | Does |
| --- | --- |
| `/card` | Print your contact card |
| `/add <card>` | Send a contact request |
| `/addqr <image path>` | Send a contact request from a QR image |
| `/accept <who>` / `/reject <who>` | Answer a request |
| `/rename <who> <nickname>` | Set a local nickname |
| `/contacts` | List contacts with online/offline and direct/relayed |
| `/msg <who> <text>` | Send a message |
| `/history <who>` | Show the conversation |
| `/stats <who>` | Message counts (in, out, pending) |
| `/status` | Your peer ID and reachability |
| `/help`, `/quit` | |

`<who>` is the start of a contact's name or peer ID.

## Flags

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--data-dir` | `twocan`, `twocan-cli` | Where your identity and history live (default `~/.config/twocan`) |
| `--bootstrap` | `twocan`, `twocan-cli` | Comma-separated helper addresses. `--bootstrap ""` means no helper |
| `--listen` | `twocan-cli` | Comma-separated listen addresses (default: all interfaces, random ports) |
| `--mdns` | `twocan-cli` | Find peers on the local network (default `true`) |
| `--name` | `twocan-cli` | Display name (required on first run) |
| `--key`, `--listen` | `twocan-helper` | Key file path (default `./helper.key`) and listen addresses (default port 4001) |

## Your data

Everything lives in the data directory, which is created with mode 0700:

- `identity.key` is your private key, and your peer ID comes from it. If you lose it, you lose your identity, and contacts will have to add you again. If someone copies it, they can impersonate you.
- `twocan.db` holds your contacts and message history. It is **not encrypted** in v1, so use full-disk encryption if that matters to you.

Each install is its own identity. History does not sync between devices.

## Privacy and security

- **What's protected:** every connection, including relayed ones, is encrypted end to end between the two peers and authenticated by peer ID. Neither the helper nor the network can read your messages.
- **What isn't:** on a direct connection your contact sees your IP address. The helper can see which peer IDs are online and who connects through it. Anyone with access to your user account can read your history.
- **Contact requests:** nobody can message you until you accept their request. Requests are rate-limited, and rejecting someone is silent.

## Development

```bash
go test -race ./...                              # unit + in-process network tests
go test -tags integration ./cmd/twocan-cli/      # kill -9 durability test (~20 s)
```

CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs vet, the race-enabled test suite and the durability test on every push to `main` and on every pull request.

## Releases

1. **Once:** in GitHub, go to *Settings → Secrets and variables → Actions → Variables* and add `TWOCAN_BOOTSTRAP`. Its value is your helper's addresses, comma-separated, e.g. `/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...,/ip4/203.0.113.7/udp/4001/quic-v1/p2p/12D3KooW...`. This is optional; without it, users pass `--bootstrap`.
2. **Each release:** tag and push.
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
   [`.github/workflows/release.yml`](.github/workflows/release.yml) reruns the full test suite. It then builds the binaries listed under [Download](#download) with the helper addresses baked in, and publishes them as a GitHub release with generated notes.

Design: [`docs/superpowers/specs/2026-10-23-twocan-design.md`](docs/superpowers/specs/2026-10-23-twocan-design.md). Plan: [`docs/superpowers/plans/2026-10-23-twocan-v1.md`](docs/superpowers/plans/2026-10-23-twocan-v1.md).

## Not in v1

Voice and video calls, group chats, file transfer, offline mailbox, multi-device sync, live webcam QR scanning, macOS and Windows builds.
