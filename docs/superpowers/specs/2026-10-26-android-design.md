# Twocan for Android: design

## Intent

Chat with the same contacts from an Android phone while away from the computer. The phone is its own Twocan identity (own key, card and history), exactly like another desktop install; nothing syncs between devices.

## Iterations

1. **Online while open (this document).** The node runs while the app is on screen and for 60 s after it leaves (`Twocan.STOP_DELAY_MS`). Messages sent to a closed phone wait on the sender and go out when both are online, as between desktops today.
2. **Wake-up pushes (next).** The app sleeps; a push wakes it when someone tries to reach it, it connects and the message arrives peer to peer. The push carries no content. Still to decide: Firebase Cloud Messaging (simple, Google sees timing) or UnifiedPush through ntfy on the VPS (no Google, one more service). Either way the helper, or a service beside it, must learn a push token per phone and who is trying to reach whom.

Not planned: an always-on foreground service, or an offline mailbox on the helper (it would break "no server holds your conversations").

## Features

Everything the desktop app does: first-launch name, My card (QR to scan, text to copy or share), Add contact (camera QR scan or pasted text), accept/reject, nicknames, contact list with online and direct/relayed, chat with pending/delivered marks and unread counts, own reachability, connection details and the step-by-step connection check. Plus a Settings screen: rename yourself, peer ID, helper addresses (override the built-in ones), share the log.

## How it is built

- **Go core, unchanged:** `internal/core` and below. The new `mobile` package wraps `core.API` in what gomobile can pass to Kotlin: strings, numbers, `[]byte`, and interfaces for callbacks, with lists and records as JSON and errors as their user-facing `errtext` strings. `gomobile bind` turns it into `android/app/libs/twocan.aar` (`android/build-go.sh`, run by Gradle's `buildGo` task when Go sources change).
- **UI:** Kotlin, Jetpack Compose, Material 3, Navigation Compose. One `Twocan` object (owned by the `Application`) holds the node and exposes contacts, reachability and events as flows. Screens call the node on an IO dispatcher.
- **QR scanning:** zxing-android-embedded (no Google Play services, in keeping with iteration 2's ntfy option).
- **Versions:** minSdk 26, targetSdk 36, compileSdk 37 (current AndroidX needs it), AGP 9.4, Gradle 9.8, Kotlin 2.4, NDK 29, ABIs arm64-v8a and x86_64 (emulator).

## Android constraints and how they are met

- **No interface listing (Android 11+).** Go's `net.Interfaces`/`InterfaceAddrs` fail, because apps may not bind netlink sockets. libp2p then advertises only loopback. `p2p.Config.InterfaceIPs` (set on Android to an `anet`-based lister) feeds an addrs factory that adds the real IPs with the listening ports. `anet` needs `-ldflags=-checklinkname=0`.
- **No mDNS.** It needs the same interface listing and would fail at start, so it is off on Android. Phones find contacts through the helper, then connect directly on the LAN.
- **DNS.** Go uses the system resolver on Android when built with cgo, which gomobile does.
- **Backups.** Disabled (`allowBackup=false` plus data-extraction rules), so `identity.key` never leaves the phone.

## Known gaps

- No notifications at all in iteration 1: a message arriving while the app is in the background (in its last minute) is silent.
- The debug key is per computer, so updates must come from the same machine; uninstalling loses the identity. A release signing key (a CI secret) and an APK in GitHub releases are still to do.
- The QR code is the card as of when it is opened; if the relay address appears later, reopen it.
