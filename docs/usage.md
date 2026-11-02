# Using Twocan

The details behind the [README](../README.md): what you can do in a conversation, what to do when a contact will not connect, the command-line client, flags, and where your data lives.

## In a conversation

- **Formatting:** `*bold*`, `_italic_`, and web addresses become links.
- **Click a message** (long-press on Android) to reply to it, react with an emoji, copy it, edit it or delete it. You can edit or delete for everyone only your own messages. "Delete for everyone" leaves "This message was deleted" on both sides, and a message not yet sent is never sent. "Delete for me" removes it from this device only.
- **Read receipts and typing:** ✓✓ shows once they have read a message, and "typing…" while they write to you.
- **Search** (the box above the contacts, or 🔍 on Android) finds messages in every conversation, ignoring accents and case.

Edits, deletions, reactions and read receipts wait on your device like messages and reach your contact when you are both online. A contact on an older Twocan still gets your messages, just not those changes. Nothing is ever deleted from their device without their Twocan agreeing to it, so treat "delete for everyone" as a request.


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

**Sending your log.** Click **Send log** (desktop), use **Settings → Send log** (Android) or run `/sendlog [note]` (`twocan-cli`). Twocan sends the end of its log, with your note, to your helper and shows a code such as `K7F3-9QX2`: pass the code on to whoever looks into the problem. The log holds contact names and network addresses, never your messages. It only works if the helper receives logs (see [Receiving logs on the helper](maintaining.md#receiving-logs-on-the-helper)); otherwise Twocan says why, and on Android offers **Share log** instead.

**Why you might be on the relay, and how Twocan gets off it.** Twocan keeps the same ports from one start to the next, so the addresses your contacts saved keep working. When two contacts connect, even through the relay, each sends the other all its addresses: the local network ones that libp2p and the helper never pass on, and the public ones (IPv6, a port your router forwards, the address the helper sees) that libp2p stops advertising once you have a relay address. While a contact is connected only through the relay, Twocan tries those addresses right away, then after 1 minute, 2, 4 and so on up to every 30 minutes, and switches to a direct connection as soon as one works. On mobile data a direct connection needs one side to accept incoming connections (often IPv6, or a forwarded port) or a successful hole punch; otherwise the relay is the expected path. If you stay on the relay, the direct dial in **Check connection** shows which addresses fail and why: "no answer" usually means a firewall, or a Wi-Fi network that keeps devices apart.

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
| `/history <who>` | Show the conversation, each message with its short `<id>` |
| `/reply <who> <id> <text>` | Answer a message |
| `/edit <who> <id> <text>` | Change one of your messages |
| `/delete <who> <id> [all]` | Delete a message for you, or with `all` for everyone (your own only) |
| `/react <who> <id> <emoji>` | React to a message; `-` removes your reaction |
| `/search <words>` | Find messages in every conversation |
| `/stats <who>` | Message counts (in, out, pending) |
| `/info <who>` | Connection details: how you're connected and found each other, last problem, saved addresses |
| `/check <who>` | Check step by step whether and how they can be reached |
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
| `--log-level` | `twocan`, `twocan-cli` | How much goes in the log file: `debug`, `info` (default), `warn` or `error` |
| `--key`, `--listen` | `twocan-helper` | Key file path (default `./helper.key`) and listen addresses (default port 4001) |

## Your data

Everything lives in the data directory, which is created with mode 0700:

- `identity.key` is your private key, and your peer ID comes from it. If you lose it, you lose your identity, and contacts will have to add you again. If someone copies it, they can impersonate you.
- `twocan.db` holds your contacts and message history. It is **not encrypted** in v1, so use full-disk encryption if that matters to you.
- `twocan.log` is the log (rotated at 5 MB, the last 3 kept). It records connection attempts and why they failed, discoveries, contact requests and deliveries, with peer IDs and IP addresses but **never message text or keys**. When something misbehaves, run with `--log-level debug` and look here, or send it with **Send log**.

Each install is its own identity. History does not sync between devices.
