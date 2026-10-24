// Package clipimg reads an image from the desktop clipboard by running the
// system's clipboard tool: wl-paste on Wayland, xclip on X11.
package clipimg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
)

var (
	// ErrNoTool means the clipboard tool for this session is not installed.
	ErrNoTool = errors.New("install wl-clipboard (Wayland) or xclip (X11) to paste images")
	// ErrNoImage means the clipboard holds no PNG image.
	ErrNoImage = errors.New("the clipboard has no image")

	errNoDisplay = errors.New("no Wayland or X11 session to read the clipboard from")
)

// run runs a command and returns its standard output. Tests replace it.
var run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
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
	case len(out) == 0:
		return nil, ErrNoImage
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, ErrNoImage
	}
	return img, nil
}
