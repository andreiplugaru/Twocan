package identity

import (
	"errors"
	"image"

	"github.com/makiuchi-d/gozxing"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"
	qrcode "github.com/skip2/go-qrcode"
)

// ErrNoQRCode means no readable QR code was found in an image.
var ErrNoQRCode = errors.New("no QR code found in the image")

// CardQRPNG returns a size×size PNG of a QR code holding card (a card string
// from EncodeCard), at medium error correction.
func CardQRPNG(card string, size int) ([]byte, error) {
	return qrcode.Encode(card, qrcode.Medium, size)
}

// DecodeCardQR finds a QR code anywhere in img (a saved QR PNG or a whole
// screenshot) and decodes the card it holds. It returns ErrNoQRCode when no
// QR code can be read, and DecodeCard's errors (such as ErrNotACard) when
// the code holds something else.
func DecodeCardQR(img image.Image) (Card, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return Card{}, ErrNoQRCode
	}
	hints := map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true}
	res, err := zxqr.NewQRCodeReader().Decode(bmp, hints)
	if err != nil {
		// Not found, or found but too damaged to read: either way there is
		// no code we can use.
		return Card{}, ErrNoQRCode
	}
	return DecodeCard(res.GetText())
}
