package identity

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCardQRPNGDecodesAsPNG(t *testing.T) {
	b, err := CardQRPNG("twocan:abcdefgh", 256)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	require.Equal(t, 256, img.Bounds().Dx())
	require.Equal(t, 256, img.Bounds().Dy())
}
