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
| Android app | The same chat on your phone, in [`android/`](android/) (see [Android app](#android-app)) |

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

## When a contact won't connect

Select the contact and click **Details** above the conversation (or run `/info <who>` and `/check <who>` in `twocan-cli`). The window shows:

- **How you're connected:** direct or through the relay, over TCP or QUIC, who dialled whom, and the latency.
- **How you found each other:** on your local network (mDNS), through the helper, from an address you already had, or they connected to you. A connection can also come from hole punching (upgraded from the relay).
- **When it goes wrong:** when they were last seen, the last problem in plain words, when Twocan tries again, and the addresses it will try after a restart.

**Check connection** then tries every way of reaching them, one step at a time:

1. the helper;
2. your reachability;
3. your relay slot;
4. whether the helper knows them;
5. a direct dial;
6. a dial through the helper's relay;
7. a hole punch.

It ends with a one-line verdict. The direct dial lists every address it tried and why each failed. **Copy results** puts the whole report on the clipboard, ready to paste into a bug report. Successful dials stay connected. "Last seen" and "last problem" cover the current session only.

**Why you might be on the relay, and how Twocan gets off it.** Twocan keeps the same ports from one start to the next, so the addresses your contacts saved keep working. When two contacts connect, even through the relay, each sends the other its own addresses, including the local network ones that libp2p and the helper never pass on. While a contact is connected only through the relay, Twocan tries those addresses right away and then every minute, and switches to a direct connection as soon as one works. If you stay on the relay, the direct dial in **Check connection** shows which addresses fail and why: "no answer" usually means a firewall, or a Wi-Fi network that keeps devices apart.

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
| `/info <who>` | Connection details: how you're connected and found each other, last problem, saved addresses |
| `/check <who>` | Check step by step whether and how they can be reached |
| `/status` | Your peer ID and reachability |
| `/help`, `/quit` | |

`<who>` is the start of a contact's name or peer ID.

## Android app

The Android app (Android 8 and up) does everything the desktop app does: your card as a QR code to scan or text to share, adding contacts by scanning their QR code with the camera or pasting their card, requests, nicknames (long-press a contact), chat with 🕓/✓, connection details and Check connection (the ⓘ in a conversation). The phone is its own identity, with its own card and history, just like another computer.

**It is online only while it is open**, and for a minute after you leave it. Messages sent to you meanwhile wait on the sender's device and arrive when you are both online. Wake-up notifications are planned for the next version.

It finds contacts through the helper only: Android does not let apps list the network interfaces the way local-network discovery (mDNS) needs, so two phones on the same Wi-Fi still need the helper to find each other. Once found, they connect directly over the Wi-Fi.

### Building it

You need Go, a JDK (17 or newer), and the Android SDK with the NDK. Android Studio installs the SDK; without it, use the [command-line tools](https://developer.android.com/studio#command-line-tools-only):

```bash
sdkmanager "platform-tools" "platforms;android-37.0" "build-tools;36.0.0" "ndk;29.0.14206865"
echo "sdk.dir=$HOME/Android/Sdk" > android/local.properties

cd android
TWOCAN_BOOTSTRAP=/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW... ./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk   # or copy the APK to the phone
```

The build first turns the Go code into an Android library with gomobile ([`android/build-go.sh`](android/build-go.sh)); `TWOCAN_BOOTSTRAP` bakes your helper's addresses in, as for the desktop release. You can also set or change them in the app under **Settings → Helper addresses**.

Install updates over the old app (`adb install -r`) and always build on the same computer: Android only accepts an update signed with the same key, and the debug key is per computer. **Uninstalling deletes your identity and history**, and contacts would have to add you again. The app is excluded from Android's cloud backups on purpose, since they would copy your private key off the phone.

To share the log for troubleshooting, use **Settings → Share log**.

## Flags

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--data-dir` | `twocan`, `twocan-cli` | Where your identity and history live (default `~/.config/twocan`) |
| `--bootstrap` | `twocan`, `twocan-cli` | Comma-separated helper addresses. `--bootstrap ""` means no helper |
| `--listen` | `twocan-cli` | Comma-separated listen addresses (default: all interfaces, random ports) |
| `--mdns` | `twocan-cli` | Find peers on the local network (default `true`) |
| `--name` | `twocan-cli` | Display name (required on first run) |
| `--log-level` | `twocan`, `twocan-cli` | How much goes in the log file: `debug`, `info` (default), `warn` or `error` |
| `--key`, `--listen` | `twocan-helper` | Key file path (default `./helper.key`) and listen addresses (default port 4001) |

## Your data

Everything lives in the data directory, which is created with mode 0700:

- `identity.key` is your private key, and your peer ID comes from it. If you lose it, you lose your identity, and contacts will have to add you again. If someone copies it, they can impersonate you.
- `twocan.db` holds your contacts and message history. It is **not encrypted** in v1, so use full-disk encryption if that matters to you.
- `twocan.log` is the log (rotated at 5 MB, the last 3 kept). It records connection attempts and why they failed, discoveries, contact requests and deliveries, with peer IDs and IP addresses but **never message text or keys**. When something misbehaves, run with `--log-level debug` and look here.

Each install is its own identity. History does not sync between devices.

## Privacy and security

- **What's protected:** every connection, including relayed ones, is encrypted end to end between the two peers and authenticated by peer ID. Neither the helper nor the network can read your messages.
- **What isn't:** on a direct connection your contact sees your IP address. The helper can see which peer IDs are online and who connects through it. Anyone with access to your user account can read your history.
- **Contact requests:** nobody can message you until you accept their request. Requests are rate-limited, and rejecting someone is silent.

## Development

```bash
go test -race ./...                              # unit + in-process network tests
go test -tags integration ./cmd/twocan-cli/      # kill -9 durability test (~20 s)
cd android && ./gradlew testDebugUnitTest lintDebug   # Android app: Kotlin tests and lint
```

The Android app talks to Go through the `mobile` package, which wraps the same `core.API` as the desktop app and the CLI. Its tests run two nodes over loopback in `go test ./mobile`.

CI ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs vet, the race-enabled test suite and the durability test on every push to `main` and on every pull request, and builds, lints and unit-tests the Android app.

## Releases

1. **Once:** in GitHub, go to *Settings → Secrets and variables → Actions → Variables* and add `TWOCAN_BOOTSTRAP`. Its value is your helper's addresses, comma-separated, e.g. `/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...,/ip4/203.0.113.7/udp/4001/quic-v1/p2p/12D3KooW...`. This is optional; without it, users pass `--bootstrap`.
2. **Each release:** tag and push.
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
   [`.github/workflows/release.yml`](.github/workflows/release.yml) reruns the full test suite. It then builds the binaries listed under [Download](#download) with the helper addresses baked in, and publishes them as a GitHub release with generated notes.

### Deploying the helper automatically

When the `VPS_HOST` variable is set, each release also updates the helper on your VPS. The workflow connects over SSH with a key that can do only one thing: run [`deploy/twocan-helper-update`](deploy/twocan-helper-update) with the new tag. That script downloads the release's helper binary, checks it against `SHA256SUMS` and restarts the service. The helper's key in `/var/lib/twocan-helper/` is never touched, so its peer ID and the bootstrap addresses stay the same.

**On the VPS** (the helper already runs as the `twocan-helper` systemd service):
```bash
sudo apt install -y curl jq
sudo useradd --system --create-home --shell /bin/sh deploy
sudo install -m 755 -o root -g root twocan-helper-update /usr/local/bin/twocan-helper-update
echo 'deploy ALL=(root) NOPASSWD: /usr/local/bin/twocan-helper-update' | sudo tee /etc/sudoers.d/twocan-deploy
sudo chmod 440 /etc/sudoers.d/twocan-deploy
```
Make a key pair with `ssh-keygen -t ed25519 -N "" -f twocan-deploy -C github-deploy`. Put this line in `/home/deploy/.ssh/authorized_keys` (directory 700, file 600, both owned by `deploy`), followed by the contents of `twocan-deploy.pub`:
```
command="sudo /usr/local/bin/twocan-helper-update \"$SSH_ORIGINAL_COMMAND\"",restrict
```
While the repository is private, the script needs a fine-grained GitHub token with read-only *Contents* access to this repository. Store it as `Authorization: Bearer <token>` in `/etc/twocan-helper-update.token` (mode 600, owned by root). Delete that file once the repository is public.

**In GitHub**, under *Settings → Secrets and variables → Actions*:
- secret `VPS_SSH_KEY`: the private key file `twocan-deploy`;
- secret `VPS_KNOWN_HOSTS`: the output of `ssh-keyscan -t ed25519 <vps>`;
- variable `VPS_HOST`: the VPS address.

Test with `ssh -i twocan-deploy deploy@<vps> v0.1.0`, using a tag that has a release.

Design: [`docs/superpowers/specs/2026-10-23-twocan-design.md`](docs/superpowers/specs/2026-10-23-twocan-design.md). Plan: [`docs/superpowers/plans/2026-10-23-twocan-v1.md`](docs/superpowers/plans/2026-10-23-twocan-v1.md).

## Not in v1

Voice and video calls, group chats, file transfer, offline mailbox, multi-device sync, live webcam QR scanning, macOS and Windows builds.
