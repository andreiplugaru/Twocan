// Package clipimg reads an image from the desktop clipboard by running the
// system's clipboard tool: wl-paste on Wayland, xclip on X11.
package clipimg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"

	"twocan/internal/identity"
)

var (
	// ErrNoTool means the clipboard tool for this session is not installed.
	ErrNoTool = errors.New("install wl-clipboard (Wayland) or xclip (X11) to paste images")
	// ErrNoImage means the clipboard holds no PNG image.
	ErrNoImage = errors.New("the clipboard has no image")

	errNoDisplay = errors.New("no Wayland or X11 session to read the clipboard from")
)

// maxBytes caps how much clipboard data we read (a 4K screenshot as PNG is
// a few MB).
const maxBytes = 32 << 20

// errTooMuch stops a tool whose output passes maxBytes.
var errTooMuch = fmt.Errorf("more than %d MiB of data", maxBytes>>20)

// run runs a command and returns its standard output, failing with
// errTooMuch once it passes maxBytes. Tests replace it.
var run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	out := &cappedBuffer{max: maxBytes}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	err := cmd.Run()
	if out.over {
		// The tool may have died writing to the closed pipe; the cap is
		// the real reason.
		return nil, errTooMuch
	}
	return out.buf.Bytes(), err
}

// cappedBuffer keeps up to max bytes and fails any write past that, which
// makes exec stop copying from the tool. The buffer is a named field, not
// embedded: an embedded bytes.Buffer's ReadFrom would let io.Copy bypass
// Write and the cap.
type cappedBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.over = true
		return 0, errTooMuch
	}
	return b.buf.Write(p)
}

// Read returns the PNG image on the clipboard.
func Read(ctx context.Context) (image.Image, error) {
	var name string
	var args []string
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		name, args = "wl-paste", []string{"--no-newline", "--type", "image/png"}
	case os.Getenv("DISPLAY") != "":
		name, args = "xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}
	default:
		return nil, errNoDisplay
	}

	out, err := run(ctx, name, args...)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, ErrNoTool
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		// Both tools fail when the clipboard is empty or holds no PNG.
		return nil, fmt.Errorf("%w (%s: %v)", ErrNoImage, name, err)
	case len(out) > maxBytes:
		return nil, fmt.Errorf("%w (%v)", ErrNoImage, errTooMuch)
	case len(out) == 0:
		return nil, ErrNoImage
	}
	img, err := identity.DecodeImage(bytes.NewReader(out))
	if errors.Is(err, identity.ErrImageTooLarge) {
		return nil, fmt.Errorf("%w: %w", ErrNoImage, err)
	}
	if err != nil {
		return nil, ErrNoImage
	}
	return img, nil
}
