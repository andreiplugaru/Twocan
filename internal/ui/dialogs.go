package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/errtext"
	"twocan/internal/identity"
)

const (
	// qrPixels is the size of the QR PNG we show and save.
	qrPixels = 512
	// qrShown is the on-screen size of the QR code.
	qrShown = 280
	// dialogWidth is the width of our dialogs; their content alone would
	// make them narrow.
	dialogWidth = 440
)

// errBlankName is shown when a name entry is left blank.
var errBlankName = errors.New("Please enter a name")

// showFirstRun asks for our display name. It has no dismiss button, so it
// stays up until a name is set; done runs after that.
func showFirstRun(api core.API, w fyne.Window, done func()) {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("Your name")
	showForm(w, "Welcome to Twocan", "Choose the name your contacts will see.",
		"Continue", entry, false, func(text string) error {
			name, err := nonBlank(text)
			if err != nil {
				return err
			}
			if err := api.SetDisplayName(name); err != nil {
				return err
			}
			done()
			return nil
		})
}

// showMyCard shows our contact card as a QR code and as text, with buttons
// to copy the text and to save the QR code as a PNG file.
func showMyCard(api core.API, w fyne.Window) {
	card, err := api.MyCard()
	if err != nil {
		errorDialog(w, err)
		return
	}
	png, err := identity.CardQRPNG(card, qrPixels)
	if err != nil {
		errorDialog(w, err)
		return
	}

	img := canvas.NewImageFromReader(bytes.NewReader(png), "twocan-card.png")
	img.FillMode = canvas.ImageFillContain
	img.ScaleMode = canvas.ImageScalePixels // keep the QR modules sharp
	img.SetMinSize(fyne.NewSquareSize(qrShown))

	text := widget.NewMultiLineEntry()
	text.SetText(card)
	text.Wrapping = fyne.TextWrapBreak // the card has no spaces to wrap at
	text.SetMinRowsVisible(3)
	text.Disable() // read-only; its text can still be selected

	copyText := widget.NewButton("Copy", func() { fyne.CurrentApp().Clipboard().SetContent(card) })
	save := widget.NewButton("Save QR as PNG…", func() { saveQR(w, png) })

	content := container.NewVBox(
		container.NewCenter(img),
		text,
		container.NewHBox(layout.NewSpacer(), copyText, save),
	)
	d := dialog.NewCustom("My card", "Close", content, w)
	d.Resize(fyne.NewSize(dialogWidth, 0))
	d.Show()
}

// saveQR asks where to save png and writes it there.
func saveQR(w fyne.Window, png []byte) {
	fd := dialog.NewFileSave(func(out fyne.URIWriteCloser, err error) {
		if err != nil {
			errorDialog(w, err)
			return
		}
		if out == nil { // cancelled
			return
		}
		_, err = out.Write(png)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			errorDialog(w, err)
		}
	}, w)
	fd.SetFileName("twocan-card.png")
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".png"}))
	fd.Show()
}

// showAddContact asks for a pasted contact card and adds that contact.
func showAddContact(api core.API, w fyne.Window) {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("twocan:…")
	entry.Wrapping = fyne.TextWrapBreak
	entry.SetMinRowsVisible(4)
	showForm(w, "Add contact", "Paste your contact's card.", "Add", entry, true,
		func(text string) error {
			return api.AddContact(context.Background(), text)
		})
}

// showRename asks for a new local nickname for contact id, whose name is
// now current.
func showRename(api core.API, w fyne.Window, id peer.ID, current string) {
	entry := widget.NewEntry()
	entry.SetText(current)
	showForm(w, "Rename contact", "Choose the name you see for this contact.",
		"Rename", entry, true, func(text string) error {
			name, err := nonBlank(text)
			if err != nil {
				return err
			}
			return api.RenameContact(context.Background(), id, name)
		})
}

// showForm shows a dialog with prompt, entry and an action button, plus a
// Cancel button if cancel is set. The action (or Enter in a single-line
// entry) runs submit on the entry text: on success the dialog closes,
// otherwise the error is shown under the entry and the dialog stays open.
func showForm(w fyne.Window, title, prompt, action string, entry *widget.Entry,
	cancel bool, submit func(string) error) {
	errText := widget.NewLabel("")
	errText.Importance = widget.DangerImportance
	errText.Hide()
	content := container.NewVBox(widget.NewLabel(prompt), entry, errText)
	d := dialog.NewCustomWithoutButtons(title, content, w)

	run := func() {
		if err := submit(entry.Text); err != nil {
			errText.SetText(errtext.FriendlyError(err))
			errText.Show()
			d.Resize(fyne.NewSize(dialogWidth, 0)) // grow to fit errText
			return
		}
		d.Hide()
	}
	ok := widget.NewButton(action, run)
	ok.Importance = widget.HighImportance
	buttons := []fyne.CanvasObject{ok}
	if cancel {
		buttons = append([]fyne.CanvasObject{widget.NewButton("Cancel", d.Hide)}, buttons...)
	}
	d.SetButtons(buttons)
	if !entry.MultiLine {
		entry.OnSubmitted = func(string) { run() }
	}

	d.Resize(fyne.NewSize(dialogWidth, 0))
	d.Show()
	w.Canvas().Focus(entry)
}

// nonBlank trims a user-entered name and rejects a blank one. The core
// enforces the length limit.
func nonBlank(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errBlankName
	}
	return name, nil
}

// errorDialog shows err to the user in a dialog over w.
func errorDialog(w fyne.Window, err error) {
	dialog.ShowError(errors.New(errtext.FriendlyError(err)), w)
}
