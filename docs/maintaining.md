# Maintaining Twocan

For whoever builds, releases and runs Twocan: the Android build, tests, releases, the signing key, deploying the helper and receiving logs on it.

## Building the Android app


You need Go, a JDK (17 or newer), and the Android SDK with the NDK. Android Studio installs the SDK; without it, use the [command-line tools](https://developer.android.com/studio#command-line-tools-only):

```bash
sdkmanager "platform-tools" "platforms;android-37.0" "build-tools;36.0.0" "ndk;29.0.14206865"
echo "sdk.dir=$HOME/Android/Sdk" > android/local.properties

cd android
TWOCAN_BOOTSTRAP=/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW... ./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk   # or copy the APK to the phone
```

The build first turns the Go code into an Android library with gomobile ([`android/build-go.sh`](../android/build-go.sh)); `TWOCAN_BOOTSTRAP` bakes your helper's addresses in, as for the desktop release. You can also set or change them in the app under **Settings → Helper addresses**.

Install updates over the old app (`adb install -r`) and always build on the same computer: Android only accepts an update signed with the same key, and the debug key is per computer. **Uninstalling deletes your identity and history**, and contacts would have to add you again. The app is excluded from Android's cloud backups on purpose, since they would copy your private key off the phone.

## Development

```bash
go test -race ./...                              # unit + in-process network tests
go test -tags integration ./cmd/twocan-cli/      # kill -9 durability test (~20 s)
cd android && ./gradlew testDebugUnitTest lintDebug   # Android app: Kotlin tests and lint
```

The Android app talks to Go through the `mobile` package, which wraps the same `core.API` as the desktop app and the CLI. Its tests run two nodes over loopback in `go test ./mobile`.

CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs vet, the race-enabled test suite and the durability test on every push to `main` and on every pull request, and builds, lints and unit-tests the Android app.

## Releases

1. **Once:** in GitHub, go to *Settings → Secrets and variables → Actions → Variables* and add `TWOCAN_BOOTSTRAP`. Its value is your helper's addresses, comma-separated, e.g. `/ip4/203.0.113.7/tcp/4001/p2p/12D3KooW...,/ip4/203.0.113.7/udp/4001/quic-v1/p2p/12D3KooW...`. This is optional; without it, users pass `--bootstrap`.
   For the Android app, also add two *Secrets* (same page, *Secrets* tab), made from the release signing key (see [Signing the Android app](#signing-the-android-app)). Without them the release fails.
2. **Each release:** tag and push. The tag must look like `v1.2.3`: the Android app's version comes from it.
   ```bash
   git tag v0.1.0
   git push origin v0.1.0
   ```
   [`.github/workflows/release.yml`](../.github/workflows/release.yml) reruns the full test suite. It then builds the binaries listed under [Download](../README.md#download) and the signed Android app with the helper addresses baked in, and publishes them as a GitHub release with generated notes.

## Signing the Android app

Phones install a new release over the old one only if both are signed with the same key, so **every release must use the same key, forever**. If it is lost, users cannot update and would have to uninstall, losing their identity and history. Keep a backup of the key file and its password somewhere safe, off this computer.

Make the key once (PKCS12, alias `twocan`, valid for 100 years):

```bash
mkdir -m 700 ~/twocan-release-key && cd ~/twocan-release-key
(umask 077; openssl rand -base64 32 | tr -d '\n' > password.txt)
keytool -genkeypair -keystore twocan-release.p12 -storetype PKCS12 -alias twocan \
  -keyalg RSA -keysize 4096 -validity 36500 -dname "CN=Twocan" -storepass:file password.txt
```

Add it as two repository secrets, with the [GitHub CLI](https://cli.github.com):

```bash
base64 -w0 twocan-release.p12 | gh secret set ANDROID_KEYSTORE_BASE64
gh secret set ANDROID_KEYSTORE_PASSWORD < password.txt
```

Or on GitHub, under *Settings → Secrets and variables → Actions → Secrets*: run `base64 -w0 twocan-release.p12 > keystore.base64`, open `keystore.base64` in an editor, select all and paste it as `ANDROID_KEYSTORE_BASE64`; paste the contents of `password.txt` as `ANDROID_KEYSTORE_PASSWORD`. Copy from the editor rather than the terminal, which can cut a long line short. The release log prints the size and SHA-256 of the keystore it decoded: they must match `wc -c twocan-release.p12` and `sha256sum twocan-release.p12`.

To build a signed release APK locally, set `TWOCAN_KEYSTORE` to the key file, `TWOCAN_KEYSTORE_PASSWORD` to its password and `TWOCAN_VERSION` to the version (`1.2.3`), then run `./gradlew assembleRelease` in `android/`.

## Deploying the helper automatically

When the `VPS_HOST` variable is set, each release also updates the helper on your VPS. The workflow connects over SSH with a key that can do only one thing: run [`deploy/twocan-helper-update`](../deploy/twocan-helper-update) with the new tag. That script downloads the release's helper binary, checks it against `SHA256SUMS` and restarts the service. The helper's key in `/var/lib/twocan-helper/` is never touched, so its peer ID and the bootstrap addresses stay the same.

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

## Receiving logs on the helper

The helper can keep the logs that devices send with **Send log**, for you to read with `twocan-logs`. It is off until you turn it on.

**Once, on your computer:** run `twocan-logs init`. It creates the reader key in `~/.config/twocan-logs/reader.key` (keep it like an SSH key) and prints its peer ID.

**Once, on the VPS:** let that peer ID read logs. `sudo systemctl edit twocan-helper` opens a drop-in file, which updates leave alone; put this in it:
```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/twocan-helper --key /var/lib/twocan-helper/helper.key --log-readers <reader peer ID>
```
The empty `ExecStart=` clears the original command. Several readers are separated by commas.

**Reading:** `twocan-logs list` lists the reports; `twocan-logs get <code>` (or `get latest -n 2`) writes each one as `<code>-<name>.log` plus `<code>.txt` with the device, the note and how far its clock was from the helper's; `twocan-logs delete <code>` deletes one. It finds the helper through `--helper` or `TWOCAN_BOOTSTRAP`.

**Turning it off:** `twocan-logs off` (and `on`) takes effect at once, as does `sudo touch /var/lib/twocan-helper/logs/OFF` (`sudo rm` to undo). Devices are then told the helper isn't accepting logs. Removing `--log-readers` turns the feature off entirely.

**Limits and deletion:** a report is at most 8 MiB and all of them 256 MiB, the oldest going first when full; each is deleted after 7 days, even with the feature off (`--log-max-upload`, `--log-max-total` and `--log-max-age` change these). Each IP address may send 6 an hour and each device 3, two at a time.

**What a report holds:** the end of the device's log (contact names, peer IDs, IP addresses; never messages or keys), its name, app, version and clock, and your note. Reports are stored as sent, in `/var/lib/twocan-helper/logs/`, readable only by the helper's service and root: whoever controls the VPS can read them.
