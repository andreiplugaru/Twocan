package identity

import qrcode "github.com/skip2/go-qrcode"

// CardQRPNG returns a size×size PNG of a QR code holding card (a card string
// from EncodeCard), at medium error correction.
func CardQRPNG(card string, size int) ([]byte, error) {
	return qrcode.Encode(card, qrcode.Medium, size)
}
