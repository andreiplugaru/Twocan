package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/require"

	"twocan/internal/identity"
	"twocan/internal/imgtest"
)

// dialogOpen returns the top dialog on w, or nil if none is open.
func dialogOpen(w fyne.Window) fyne.CanvasObject { return w.Canvas().Overlays().Top() }

// findAll returns the visible objects of type T under o.
func findAll[T fyne.CanvasObject](o fyne.CanvasObject) []T {
	var out []T
	walk(o, func(c fyne.CanvasObject) {
		if t, ok := c.(T); ok {
			out = append(out, t)
		}
	})
	return out
}

func findButton(t *testing.T, o fyne.CanvasObject, text string) *widget.Button {
	t.Helper()
	for _, b := range findAll[*widget.Button](o) {
		if b.Text == text {
			return b
		}
	}
	t.Fatalf("no %q button", text)
	return nil
}

func findEntry(t *testing.T, o fyne.CanvasObject) *widget.Entry {
	t.Helper()
	es := findAll[*widget.Entry](o)
	require.Len(t, es, 1, "entries in dialog")
	return es[0]
}

// labelTexts returns the text of every visible label under o.
func labelTexts(o fyne.CanvasObject) []string {
	var out []string
	for _, l := range findAll[*widget.Label](o) {
		out = append(out, l.Text)
	}
	return out
}

// openDialog taps the main view button with text and returns the dialog it
// opened.
func openDialog(t *testing.T, v *MainView, text string) fyne.CanvasObject {
	t.Helper()
	test.Tap(findButton(t, v.win.Content(), text))
	d := dialogOpen(v.win)
	require.NotNil(t, d, "dialog opened by %q", text)
	return d
}

func TestAddContactDialogShowsDamagedCardError(t *testing.T) {
	f := standardFake()
	f.addErr = fmt.Errorf("decode: %w", identity.ErrDamagedCard)
	v := newTestView(t, f)
	d := openDialog(t, v, "Add contact")

	findEntry(t, d).SetText("twocan:abc")
	test.Tap(findButton(t, d, "Add"))

	require.Equal(t, []string{"twocan:abc"}, f.AddCalls)
	require.Same(t, d, dialogOpen(v.win), "dialog stays open")
	var lower []string
	for _, s := range labelTexts(d) {
		lower = append(lower, strings.ToLower(s))
	}
	require.Contains(t, lower, "card is incomplete or damaged")
}

func TestAddContactDialogClosesOnSuccess(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	d := openDialog(t, v, "Add contact")

	findEntry(t, d).SetText("twocan:good")
	test.Tap(findButton(t, d, "Add"))

	require.Equal(t, []string{"twocan:good"}, f.AddCalls)
	require.Nil(t, dialogOpen(v.win))
}

// fakePasteTool puts an executable named wl-paste that runs script first on
// PATH (alone, so no real clipboard tool is found) and pretends we are on
// Wayland. An empty script leaves PATH with no wl-paste at all.
func fakePasteTool(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "wl-paste"),
			[]byte("#!/bin/sh\n"+script+"\n"), 0o700))
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
}

func TestAddDialogPasteQRShowsNoToolError(t *testing.T) {
	fakePasteTool(t, "")
	f := standardFake()
	v := newTestView(t, f)
	d := openDialog(t, v, "Add contact")

	test.Tap(findButton(t, d, "Paste QR from clipboard"))

	require.Empty(t, f.AddImageCalls)
	require.Same(t, d, dialogOpen(v.win), "dialog stays open")
	var hint bool
	for _, s := range labelTexts(d) {
		hint = hint || strings.Contains(s, "install wl-clipboard (Wayland) or xclip (X11) to paste images")
	}
	require.True(t, hint, "install hint shown in %q", labelTexts(d))
}

func TestAddDialogPasteQRAddsContact(t *testing.T) {
	b, err := identity.CardQRPNG("twocan:abcdefgh", 256)
	require.NoError(t, err)
	qr := filepath.Join(t.TempDir(), "qr.png")
	require.NoError(t, os.WriteFile(qr, b, 0o600))
	// The fake tool can't rely on PATH for cat, so use its full path.
	cat, err := exec.LookPath("cat")
	require.NoError(t, err)
	fakePasteTool(t, cat+" '"+qr+"'")

	f := standardFake()
	v := newTestView(t, f)
	d := openDialog(t, v, "Add contact")
	test.Tap(findButton(t, d, "Paste QR from clipboard"))

	require.Len(t, f.AddImageCalls, 1)
	require.Equal(t, image.Rect(0, 0, 256, 256), f.AddImageCalls[0].Bounds())
	require.Nil(t, dialogOpen(v.win), "dialog closes on success")
}

func TestAddDialogOpenQRImageOpensFileDialog(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	d := openDialog(t, v, "Add contact")
	test.Tap(findButton(t, d, "Open QR image…"))
	top := dialogOpen(v.win)
	require.NotNil(t, top)
	require.NotSame(t, d, top, "file dialog opens over the add dialog")
}

func TestAddFromImageFileDecodesAndAdds(t *testing.T) {
	b, err := identity.CardQRPNG("twocan:abcdefgh", 256)
	require.NoError(t, err)
	f := standardFake()
	require.NoError(t, addFromImageFile(f, bytes.NewReader(b)))
	require.Len(t, f.AddImageCalls, 1)

	require.Error(t, addFromImageFile(f, strings.NewReader("not an image")))
	require.Len(t, f.AddImageCalls, 1, "undecodable file never reaches the core")

	err = addFromImageFile(f, bytes.NewReader(imgtest.HugePNGHeader(100000, 100000)))
	require.ErrorIs(t, err, identity.ErrImageTooLarge)
	require.Len(t, f.AddImageCalls, 1, "oversized image never reaches the core")
}

func TestFirstRunRejectsBlankName(t *testing.T) {
	f := standardFake()
	f.name = ""
	v := newTestView(t, f)
	call(v.showFirstRun)
	d := dialogOpen(v.win)
	require.NotNil(t, d)
	require.Len(t, findAll[*widget.Button](d), 1, "no way to dismiss it but Continue")

	findEntry(t, d).SetText("   ")
	test.Tap(findButton(t, d, "Continue"))
	require.Empty(t, f.SetNameCalls)
	require.Same(t, d, dialogOpen(v.win))
	require.Contains(t, labelTexts(d), errBlankName.Error())

	findEntry(t, d).SetText("  Alice ")
	test.Tap(findButton(t, d, "Continue"))
	require.Equal(t, []string{"Alice"}, f.SetNameCalls)
	require.Nil(t, dialogOpen(v.win))
	require.Equal(t, "Alice", v.name.Text)
}

func TestRenameFromContactRow(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	test.Tap(rows(v)["Ann"].more)
	d := dialogOpen(v.win)
	require.NotNil(t, d)

	e := findEntry(t, d)
	require.Equal(t, "Ann", e.Text)
	e.SetText(" Annie ")
	test.Tap(findButton(t, d, "Rename"))

	require.Equal(t, []renameCall{{ID: ann, Name: "Annie"}}, f.RenameCalls)
	require.Nil(t, dialogOpen(v.win))
}

func TestMyCardDialogShowsQRAndCopies(t *testing.T) {
	f := standardFake()
	v := newTestView(t, f)
	d := openDialog(t, v, "My card")

	require.Len(t, findAll[*canvas.Image](d), 1, "QR image")
	e := findEntry(t, d)
	require.Equal(t, "twocan:fake", e.Text)
	require.True(t, e.Disabled(), "card text is read-only")

	test.Tap(findButton(t, d, "Copy"))
	require.Equal(t, "twocan:fake", fyne.CurrentApp().Clipboard().Content())
	findButton(t, d, "Save QR as PNG…")
}

func TestSendLogDialogShowsCode(t *testing.T) {
	f := standardFake()
	f.sendLogCode = "K7F3-9QX2"
	v := newTestView(t, f)
	d := openDialog(t, v, "Send log")
	require.Contains(t, strings.Join(labelTexts(d), " "), "Never your messages")

	findEntry(t, d).SetText("it broke")
	test.Tap(findButton(t, d, "Send"))

	require.Equal(t, "it broke", f.sentNote)
	res := dialogOpen(v.win)
	require.NotNil(t, res)
	require.NotSame(t, d, res, "the result replaces the form")
	require.Contains(t, labelTexts(res), "Log sent. Code: K7F3-9QX2")
	test.Tap(findButton(t, res, "Copy"))
	require.Equal(t, "K7F3-9QX2", fyne.CurrentApp().Clipboard().Content())
}

func TestSendLogDialogShowsWhyItFailed(t *testing.T) {
	f := standardFake()
	f.sendLogErr = errors.New("the helper isn't accepting logs")
	v := newTestView(t, f)
	d := openDialog(t, v, "Send log")
	test.Tap(findButton(t, d, "Send"))
	require.Same(t, d, dialogOpen(v.win), "the form stays open")
	require.Contains(t, strings.Join(labelTexts(d), " "), "the helper isn't accepting logs")
}
