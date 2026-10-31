# Send log: logs from any device to the helper

## Why

Debugging connectivity needs the logs of every device involved, from the same moment. Today that means sharing each log by hand (Google Drive, then downloading it on the laptop), which is slow and leaves logs on a third-party service. With **Send log**, each device uploads its log to the helper on the VPS, which every device already reaches, and the developer fetches them from there.

## Goals

- A **Send log** action on Android (Settings), the desktop app (menu) and the CLI (`/sendlog [note]`). Nothing is ever sent without it.
- The device shows a short code for the upload (`K7F3-9QX2`) with a Copy button, so the user can say which logs to look at.
- The developer lists and downloads logs from the laptop with one command.
- **An easy off switch** for receiving logs, without editing the service or restarting it.
- No new services, ports, domains or certificates on the VPS: the helper does it.

## Non-goals

- Automatic crash reports, or sending without a tap.
- Removing contact names or addresses from logs. Logs already never contain message text or keys.
- A web interface.

## Anti-goals (a failure even if it works)

- Anyone but the reader can read a log, other than whoever controls the VPS: not the network, and not other Twocan users.
- Log uploads disturb the helper's real job. The relay and the DHT must keep working whatever is uploaded.
- The helper's own output (the systemd journal) shows peer IDs. It never has (see `cmd/twocan-helper`); stored reports are the one place they appear.

## Design

### Who may read

- **The reader** is a libp2p key kept on the developer's laptop in `~/.config/twocan-logs/reader.key` (mode 0600). Its peer ID is the reader's identity.
- **The helper** gets that peer ID with `--log-readers <peerID>[,…]`. Without the flag, it doesn't offer the log protocols at all. That is the default, so existing deployments are unchanged until they opt in.
- Apps need nothing new: every build can send logs to its helper.

### What a device sends

The device builds a `LogReport` protobuf containing:
- its peer ID, display name, app and version, and platform;
- its clock time when sending;
- the user's optional note (up to 500 characters);
- the log, gzipped: the current `twocan.log` plus the previous rotated file, taking the most recent 16 MB at most before compression.

It uploads the report over a new protocol, `/twocan/logs/1.0.0`. Frames are limited to 64 KiB, so the stream carries:
1. a header frame with the total size;
2. chunk frames of up to 60 KiB each;
3. an end frame.

The helper replies with a receipt (`code`) or a refusal (`reason`: off, too large, rate limited, storage full). The device keeps no record of the upload.

### What the helper stores

Each report becomes a pair of files in `$STATE/logs/` (the systemd state directory, `/var/lib/twocan-helper/`, which only the service and root can read):
- `<received-utc>-<code>.log.gz`: the log, as sent;
- `<received-utc>-<code>.json`: the code, the time the helper received it, the size, the sender's peer ID as libp2p verified it, and the details from the report (name, app, version, platform, the device's clock, the note).

Nothing the device sends becomes part of a file name. The code is 8 characters of Crockford base32 (40 random bits), shown as `XXXX-XXXX`.

### Limits

All configurable by flag, with these defaults:

| Limit | Default |
|---|---|
| Size of one upload | 8 MiB |
| Total storage | 256 MiB; the oldest reports are deleted to make room |
| Kept for | 7 days, checked hourly and at startup |
| Uploads at the same time | 2 |
| Per sender IP address | 6 an hour |
| Per peer ID | 3 an hour |
| Idle time per stream | 30 s |
| Memory | Streams are read with deadlines and chunks are written straight to a temporary file; the upload never sits in memory. |

The per-IP and per-peer counters live in memory only.

### Deletion

A report is deleted:
- **7 days after it arrived:** the helper checks at startup and every hour;
- **when storage is full:** the oldest reports go first, to make room for a new upload;
- **on request:** `twocan-logs delete <code>`, or `rm` over SSH.

An upload that fails halfway leaves no file: its temporary file is deleted immediately, and any left over from a crash are removed at startup.

The hourly cleanup runs whenever `$STATE/logs/` exists, even with `--log-readers` removed or the off switch on. So turning the feature off leaves no reports behind for more than 7 days.

### The off switch

- Receiving is off whenever the file `$STATE/logs/OFF` exists. The helper checks it for each upload, so it takes effect at once, with no restart.
- **Two ways to flip it:**
  - over SSH: `sudo touch /var/lib/twocan-helper/logs/OFF` (and `sudo rm` to turn back on);
  - from the laptop: `twocan-logs off` / `twocan-logs on`, through the reader protocol.
- When it's off, devices get the refusal "the helper isn't accepting logs" and are offered Share log instead.
- Taking `--log-readers` out of the service turns the feature off completely.

### Reading

Readers use a second protocol, `/twocan/logs-read/1.0.0`, allowed only for the peer IDs listed in `--log-readers`. Anyone else's stream is reset straight away. Requests:
- `list`: code, received time and size of each report;
- `get <code>`: the report, in chunks;
- `delete <code>`;
- `on` and `off`.

A new command, `cmd/twocan-logs`, uses it:
- `twocan-logs init` creates the reader key and prints its peer ID, for `--log-readers`.
- `twocan-logs list`.
- `twocan-logs get <code>|latest [-n N]` downloads the report and writes `<code>-<name>.log` plus `<code>.txt` (the details, the note, and the difference between the device's clock and the helper's) to the current folder, or the folder given with `-o`. It refuses to decompress more than 256 MB.
- `twocan-logs delete`, `on`, `off`.

It finds the helper through `--helper` or `TWOCAN_BOOTSTRAP`, or falls back to the build's helper.

### The apps

- **Android:** Settings gets **Send log**. It asks for an optional note, then shows the code with a Copy button.
  - On failure (no helper, refused, off), it says why in plain words and offers the existing **Share log**.
  - The screen says what's sent: "Your log, including contact names and network addresses. Never your messages."
- **Desktop:** a **Send log…** menu item, with the same dialog.
- **CLI:** `/sendlog [note]` prints the code or the reason it failed.
- All three call one Go function, `core.SendLog(ctx, note) (code string, err error)`, through `mobile` on Android. With several helpers configured, it tries each in turn until one accepts.
- The sender's peer ID in the stored details comes from libp2p, so it is genuine. The name is what the device says about itself.

### Deployment

- The service file is unchanged, so receiving logs stays off. To turn it on, add a systemd drop-in with `--log-readers` (the README shows how).
- The `deploy/twocan-helper-update` flow stays the same.

## Security notes

- In transit, uploads use libp2p's encryption to the helper whose peer ID is pinned in the device's settings.
- At rest, reports are stored readable, in a directory only the helper's service and root can open, for at most 7 days. Whoever controls the VPS can read them: they hold contact names, peer IDs and IP addresses, never messages or keys.
- Reading over the network is limited to authenticated peer IDs, and nothing secret is built into the apps.
- Junk uploads are contained by the per-IP limits, the size cap, the storage cap and the concurrency limit. Someone with many IP addresses can still crowd out real logs, but can't affect chat or the relay.
- The reader treats every report as untrusted data: names in a report never become file paths, and decompression is capped.

## Calls made in the design

- Reports are not encrypted at rest (decided: keeping it simple matters more here than protection against a compromised VPS).
- The off switch is a file checked on every upload, flipped over SSH or with `twocan-logs off`/`on`.
- The limits and retention in the table above.

## Left to the builder

- Field names in the new protobuf messages.
- The exact refusal texts.
- How the Android Settings layout fits the button in.
- Test layout: unit tests for the helper's log store (limits, retention, the off switch) and the protocols. One in-process test sends a report from a node to a helper and reads it back as a reader.
