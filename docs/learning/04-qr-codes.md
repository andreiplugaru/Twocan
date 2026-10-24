# 4. QR codes: sharing a contact card as a picture

Note 1 explained the contact card: a peer ID plus a few addresses. This note is about carrying that card as a picture, a QR code, and reading it back: [internal/identity/qr.go](../../internal/identity/qr.go), [internal/clipimg/clipimg.go](../../internal/clipimg/clipimg.go) and the "Add contact" dialog in [internal/ui/dialogs.go](../../internal/ui/dialogs.go).

## What the QR code holds

Exactly the card text you can copy from "My card", nothing more. `MyCard` ([internal/core/node.go](../../internal/core/node.go)) fills a small protobuf `Card` ([internal/proto/card.proto](../../internal/proto/card.proto)) with a version number (1), your peer ID, your display name and up to four addresses, best first: public addresses, then relay addresses, then addresses on your home network. `identity.EncodeCard` turns those bytes into text with base32 and puts `twocan:` in front. `identity.CardQRPNG` draws that text as a QR code with the pure-Go `skip2/go-qrcode` library, at medium error correction, which means roughly 15% of the code can be smudged or covered and it still reads.

Reading goes the other way. `identity.DecodeCardQR` hands the image to the pure-Go `gozxing` library (a port of the well-known ZXing scanner) with its "try harder" option, which looks for a QR code anywhere in the picture rather than assuming the whole image is one. The text it finds goes through the same `DecodeCard` as pasted text, so a QR code of a web page is rejected as "not a Twocan contact card", and a picture with no readable code gives "No QR code found in that image". `core.AddContactFromImage` is just `DecodeCardQR` followed by the same contact request that `AddContact` sends.

## Why it is safe to post publicly

Everything on the card is meant to be public. A peer ID is derived from your *public* key; your private key never leaves `identity.key` in your data directory and is never put on a card. Because every libp2p connection proves who holds the key behind a peer ID (note 1), someone who copies your card can only *ask* to become your contact. They cannot pretend to be you, read your messages, or add themselves without you pressing Accept. The worst a stranger can do with your card is send you a request you can reject. What the card does reveal is your chosen name and the network addresses on it, roughly the same as what any peer you chat with already sees.

## Why pictures instead of a webcam

The usual phone-style flow is to point a camera at a code. For two friends on different home networks, that does not fit: they are rarely in the same room. What they do have is some chat channel already, where sending a picture is easy. So Twocan works with images:

- **Save QR as PNG…** in "My card" writes the code to a file you can send.
- **Open QR image…** in "Add contact" opens a PNG or JPEG file someone sent you.
- **Paste QR from clipboard** reads an image you copied, such as a screenshot of their QR code or of their "My card" window.

This needs no camera permissions, no video library and no C code, so the app stays a simple pure-Go build. Live webcam scanning is left as a stretch goal.

## How pasting an image works on Linux

Fyne's clipboard only handles text, so `clipimg.Read` asks the desktop's own clipboard tool for a PNG. On Wayland (the `WAYLAND_DISPLAY` variable is set) it runs `wl-paste --no-newline --type image/png`; on X11 (`DISPLAY` is set) it runs `xclip -selection clipboard -t image/png -o`. If the tool is not installed, the dialog says to install `wl-clipboard` (Wayland) or `xclip` (X11); on Manjaro that is `sudo pacman -S wl-clipboard`. If the clipboard is empty or holds text, the dialog says there is no image. Because whatever program owns the clipboard decides what we get, Twocan reads at most 32 MiB from the tool and, like the file paths, decodes images through `identity.DecodeImage`, which checks the size in the image header and refuses anything over 40 megapixels before decoding a single pixel. Running the tool goes through a replaceable `run` function so the tests can pretend to be `wl-paste` without a desktop.

The tests in [internal/identity/qr_test.go](../../internal/identity/qr_test.go) check the full round trip (card, PNG, image, card) and a "screenshot": a QR code scaled up 1.5 times and drawn in the middle of a 1280×800 white picture, which is what a screenshot from a HiDPI screen looks like to the decoder.
