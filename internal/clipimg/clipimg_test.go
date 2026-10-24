package clipimg

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"twocan/internal/identity"
	"twocan/internal/imgtest"
)

// stubRun replaces run for the test with one that records its command and
// returns out, err.
func stubRun(t *testing.T, out []byte, err error) *[]string {
	t.Helper()
	var got []string
	old := run
	run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return out, err
	}
	t.Cleanup(func() { run = old })
	return &got
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 3, 2))))
	return buf.Bytes()
}

func TestClipimgWayland(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	t.Setenv("DISPLAY", ":0")
	cmd := stubRun(t, pngBytes(t), nil)

	img, err := Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 3, 2), img.Bounds())
	require.Equal(t, []string{"wl-paste", "--no-newline", "--type", "image/png"}, *cmd)
}

func TestClipimgX11(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	cmd := stubRun(t, pngBytes(t), nil)

	_, err := Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, *cmd)
}

func TestClipimgNoTool(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	stubRun(t, nil, &exec.Error{Name: "wl-paste", Err: exec.ErrNotFound})

	_, err := Read(t.Context())
	require.ErrorIs(t, err, ErrNoTool)
	require.Contains(t, err.Error(), "install wl-clipboard (Wayland) or xclip (X11) to paste images")
}

func TestClipimgNotImage(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	stubRun(t, []byte("hello"), nil)
	_, err := Read(t.Context())
	require.ErrorIs(t, err, ErrNoImage)
}

func TestClipimgTooManyBytes(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	big := append(pngBytes(t), make([]byte, maxBytes)...)
	stubRun(t, big, nil)
	_, err := Read(t.Context())
	require.ErrorIs(t, err, ErrNoImage)
}

func TestClipimgHugeDimensions(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	stubRun(t, imgtest.HugePNGHeader(100000, 100000), nil)
	_, err := Read(t.Context())
	require.ErrorIs(t, err, ErrNoImage)
	require.ErrorIs(t, err, identity.ErrImageTooLarge)
}

// The real run stops reading a tool's output past maxBytes.
func TestRunCapsOutput(t *testing.T) {
	out, err := run(t.Context(), "head", "-c", strconv.Itoa(maxBytes+1<<20), "/dev/zero")
	require.Error(t, err)
	require.LessOrEqual(t, len(out), maxBytes)

	out, err = run(t.Context(), "head", "-c", "10", "/dev/zero")
	require.NoError(t, err)
	require.Len(t, out, 10)
}

func TestClipimgNoDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	cmd := stubRun(t, pngBytes(t), nil)
	_, err := Read(t.Context())
	require.Error(t, err)
	require.Empty(t, *cmd, "nothing to run without a display")
}

func TestClipimgEmptyOrNoImageType(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "x")
	stubRun(t, nil, nil)
	_, err := Read(t.Context())
	require.ErrorIs(t, err, ErrNoImage, "empty output")

	// wl-paste exits non-zero when the clipboard is empty or holds no PNG.
	stubRun(t, nil, fmt.Errorf("exit status 1"))
	_, err = Read(t.Context())
	require.ErrorIs(t, err, ErrNoImage, "tool failed")
}
