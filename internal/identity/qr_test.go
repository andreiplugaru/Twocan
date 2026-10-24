package identity

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	xdraw "golang.org/x/image/draw"

	"twocan/internal/imgtest"
)

func TestCardQRPNGDecodesAsPNG(t *testing.T) {
	b, err := CardQRPNG("twocan:abcdefgh", 256)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, 256, img.Bounds().Dx())
	require.Equal(t, 256, img.Bounds().Dy())
}

// qrImage renders text as a size×size QR code PNG and decodes it.
func qrImage(t *testing.T, text string, size int) image.Image {
	t.Helper()
	b, err := CardQRPNG(text, size)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	return img
}

// white returns a white w×h image.
func white(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	return img
}

func TestDecodeCardQRRoundTrip(t *testing.T) {
	card, err := EncodeCard(testCard(t, MaxCardAddrs))
	require.NoError(t, err)
	want, err := DecodeCard(card)
	require.NoError(t, err)

	got, err := DecodeCardQR(qrImage(t, card, 256))
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestDecodeCardQRFromScreenshotLike(t *testing.T) {
	card, err := EncodeCard(testCard(t, MaxCardAddrs))
	require.NoError(t, err)
	want, err := DecodeCard(card)
	require.NoError(t, err)
	qr := qrImage(t, card, 256)

	// A 1280×800 "screenshot": white, with the QR code scaled 1.5× (as on a
	// HiDPI screen, smoothed) and drawn at (300,200).
	shot := white(1280, 800)
	at := image.Rect(300, 200, 300+384, 200+384)
	xdraw.BiLinear.Scale(shot, at, qr, qr.Bounds(), xdraw.Over, nil)

	got, err := DecodeCardQR(shot)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestDecodeCardQRNoCode(t *testing.T) {
	_, err := DecodeCardQR(white(400, 300))
	require.ErrorIs(t, err, ErrNoQRCode)
}

func TestDecodeImage(t *testing.T) {
	b, err := CardQRPNG("twocan:abcdefgh", 256)
	require.NoError(t, err)
	// A reader without ReadByte, so the header is read through a buffer that
	// reads ahead; the full decode must still see every byte.
	img, err := DecodeImage(struct{ io.Reader }{bytes.NewReader(b)})
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 256, 256), img.Bounds())

	_, err = DecodeImage(strings.NewReader("not an image"))
	require.ErrorIs(t, err, image.ErrFormat)
}

func TestDecodeImageRejectsHugeDimensions(t *testing.T) {
	// 100000×100000 is 10 gigapixels. The header carries no pixel data, so
	// ErrImageTooLarge (rather than a truncated-data error, or running out
	// of memory) shows the size was checked before decoding.
	_, err := DecodeImage(bytes.NewReader(imgtest.HugePNGHeader(100000, 100000)))
	require.ErrorIs(t, err, ErrImageTooLarge)

	// Just over the limit is rejected too; at the limit is decoded (and here
	// fails only for the missing pixel data).
	_, err = DecodeImage(bytes.NewReader(imgtest.HugePNGHeader(MaxImagePixels/1000+1, 1000)))
	require.ErrorIs(t, err, ErrImageTooLarge)
	_, err = DecodeImage(bytes.NewReader(imgtest.HugePNGHeader(1000, 1000)))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrImageTooLarge)
}

func TestDecodeCardQRNotTwocan(t *testing.T) {
	_, err := DecodeCardQR(qrImage(t, "https://example.com", 256))
	require.ErrorIs(t, err, ErrNotACard)
}
