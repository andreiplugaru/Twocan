# Twocan

A peer-to-peer text messenger for the Linux desktop and Android, written in Go.

Messages go straight from your device to your contact's and are stored only on those two. No server ever holds your conversations. A small helper server (which you run yourself) only introduces peers to each other and, when a direct connection is impossible, relays encrypted bytes it cannot read.

Twocan v1 is a learning project: one-to-one text chat only. The notes in [`docs/learning/`](docs/learning/) explain how each part works.

## What's in the box

| Binary | What it is |
| --- | --- |
| `twocan` | The desktop app: contacts on the left, conversation on the right |
| `twocan-cli` | The same thing in a terminal, handy for testing and scripting |
| `twocan-helper` | The helper server: peer discovery (private DHT), relay, reachability checks, and optionally an inbox for logs |
| `twocan-logs` | Reads the logs devices sent to the helper |
| Android app | The same chat on your phone, in [`android/`](android/) |

## Download

Grab the binaries from the [latest release](https://github.com/andreiplugaru/Twocan/releases/latest): `twocan-linux-amd64` (desktop), `twocan-cli-*`, `twocan-helper-*` (with `twocan-helper.service`), `twocan-logs-linux-amd64`, `twocan-android.apk` (Android 8 and up) and `SHA256SUMS` (`sha256sum -c SHA256SUMS --ignore-missing`). The CLI, helper and log reader are static; the desktop app needs the OpenGL, X11, Wayland and xkbcommon libraries any Linux desktop already has.

```bash
chmod +x twocan-linux-amd64
./twocan-linux-amd64
```

Release binaries usually have the helper's address built in, so no `--bootstrap` flag is needed.

## Build

You need Go 1.27+ and, for the desktop app, a C compiler and the OpenGL, X11 and Wayland development libraries that [Fyne](https://fyne.io) uses.

```bash
# Manjaro / Arch
sudo pacman -S go gcc libxcursor libxrandr libxinerama libxi libxxf86vm mesa wayland libxkbcommon
# Debian / Ubuntu
sudo apt install golang gcc libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

go build -o bin/twocan ./cmd/twocan
go build -o bin/twocan-cli ./cmd/twocan-cli
CGO_ENABLED=0 go build -o bin/twocan-helper ./cmd/twocan-helper
```

To paste a QR screenshot from the clipboard you also need `wl-clipboard` (Wayland) or `xclip` (X11). For the Android app, see [docs/maintaining.md](docs/maintaining.md#building-the-android-app).

## Quick start: two people on the same network

On the same Wi-Fi or LAN, peers find each other automatically, so no helper is needed.

1. Both of you run `bin/twocan` and pick a display name on first launch.
2. One of you clicks **My card** and sends the card to the other: copy the text, or save the QR code as a PNG.
3. The other clicks **Add contact** and pastes the text, opens the QR image, or pastes a QR screenshot from the clipboard.
4. The first person sees **wants to add you** and clicks **Accept**.
5. Chat. 🕓 means the message is waiting, ✓ that it was delivered, ✓✓ that they read it.

Messages you send while your contact is offline wait on your computer and go out automatically when you're both online. Click a message (long-press on Android) to reply, react, edit or delete it.

## Chatting across the internet

People on different home networks need the helper server so they can find each other and fall back to a relay when their routers block a direct connection.

1. **Run the helper** on a small VPS with a public IP, with TCP and UDP port 4001 open. Step-by-step instructions, including the systemd unit in [`deploy/twocan-helper.service`](deploy/twocan-helper.service), are in [docs/learning/02](docs/learning/02-nat-dht-relay-holepunch.md).
2. **Point the apps at it.** The helper prints addresses like `/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...`. Pass them with `--bootstrap <addr1>,<addr2>`, or build them in with `-ldflags "-X twocan/internal/p2p.bootstrapAddrs=<addr1>,<addr2>"` (releases do this from the `TWOCAN_BOOTSTRAP` variable).
3. Add each other with your cards as above. The status bar shows whether you're reachable and whether the open conversation is **direct** or **via relay**.

If a contact won't connect, select them and click **Details**, then **Check connection**: see [docs/usage.md](docs/usage.md#when-a-contact-wont-connect).

## Android app

The Android app does everything the desktop app does; the phone is its own identity, with its own card and history. **It is online only while it is open**, and for a minute after you leave it; messages sent to you meanwhile wait on the sender's device. It finds contacts through the helper only, even on the same Wi-Fi.

Install later releases over the first one. **Uninstalling deletes your identity and history**, and an APK you built yourself and a release APK cannot update each other, since Android only accepts an update signed with the same key.

## Privacy and security

- **What's protected:** every connection, including relayed ones, is encrypted end to end between the two peers and authenticated by peer ID. Neither the helper nor the network can read your messages.
- **What isn't:** on a direct connection your contact sees your IP address. The helper can see which peer IDs are online and who connects through it. Your history (`~/.config/twocan/twocan.db`) is **not encrypted** on disk, so anyone with access to your user account can read it.
- **Contact requests:** nobody can message you until you accept their request. Requests are rate-limited, and rejecting someone is silent.

Your private key is `~/.config/twocan/identity.key`: lose it and you lose your identity, leak it and someone can impersonate you.

## More

- [docs/usage.md](docs/usage.md): conversations, troubleshooting connections, the command-line client, flags and your data.
- [docs/maintaining.md](docs/maintaining.md): building the Android app, tests, releases, the signing key, deploying the helper and receiving logs on it.
- Design: [`docs/superpowers/specs/2026-10-23-twocan-design.md`](docs/superpowers/specs/2026-10-23-twocan-design.md).

## Not in v1

Voice and video calls, group chats, file transfer, offline mailbox, multi-device sync, live webcam QR scanning, macOS and Windows builds.
