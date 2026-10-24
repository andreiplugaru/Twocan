package identity

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeImage reads JPEG
	_ "image/png"  // and PNG
	"io"

	"github.com/makiuchi-d/gozxing"
	zxqr "github.com/makiuchi-d/gozxing/qrcode"
	qrcode "github.com/skip2/go-qrcode"
)

// ErrNoQRCode means no readable QR code was found in an image.
var ErrNoQRCode = errors.New("no QR code found in the image")

// MaxImagePixels is the largest image (width × height) DecodeImage decodes:
// bigger than any screenshot, small enough to decode in a few hundred MB.
const MaxImagePixels = 40_000_000

// ErrImageTooLarge means an image has more than MaxImagePixels pixels.
var ErrImageTooLarge = errors.New("image is too large")

// DecodeImage decodes a PNG or JPEG image from r. It reads the image size
// from the header first and returns ErrImageTooLarge, before allocating
// any pixels, if the image has more than MaxImagePixels pixels.
func DecodeImage(r io.Reader) (image.Image, error) {
	var head bytes.Buffer // what DecodeConfig read, to replay for Decode
	cfg, _, err := image.DecodeConfig(io.TeeReader(r, &head))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return nil, fmt.Errorf("%w (%d×%d pixels)", ErrImageTooLarge, cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(io.MultiReader(&head, r))
	return img, err
}

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
