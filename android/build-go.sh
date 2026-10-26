#!/bin/sh
# Builds the Go core (the twocan/mobile package) into app/libs/twocan.aar
# with gomobile. Gradle runs it before each build when the Go code changed.
#
# Needs Go and the Android NDK: ANDROID_HOME and ANDROID_NDK_HOME (Gradle
# sets both). TWOCAN_BOOTSTRAP, if set, is baked in as the default helper
# addresses, as in the desktop release builds.
set -eu
cd "$(dirname "$0")/.."

# gomobile runs gobind from PATH; both are pinned as tools in go.mod.
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
go build -o "$bin" golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind

mkdir -p android/app/libs

# -checklinkname=0: anet (which lists the phone's IPs) links to net internals.
PATH="$bin:$PATH" gomobile bind \
  -target=android/arm64,android/amd64 \
  -androidapi 26 \
  -javapkg app.twocan.go \
  -ldflags "-checklinkname=0 -s -w -X twocan/internal/p2p.bootstrapAddrs=${TWOCAN_BOOTSTRAP:-}" \
  -o android/app/libs/twocan.aar \
  ./mobile
