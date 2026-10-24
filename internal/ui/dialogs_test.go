package ui

import (
	"fmt"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/stretchr/testify/require"

	"twocan/internal/identity"
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
