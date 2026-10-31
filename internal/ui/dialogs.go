package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/clipimg"
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
	// pasteTimeout bounds the wait for the clipboard tool, which runs on the
	// UI goroutine.
	pasteTimeout = 3 * time.Second
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

// showAddContact adds a contact from a pasted card, from a QR code image
// file, or from a QR code image (such as a screenshot) on the clipboard.
func showAddContact(api core.API, w fyne.Window) {
	entry := widget.NewMultiLineEntry()
	entry.SetPlaceHolder("twocan:…")
	entry.Wrapping = fyne.TextWrapBreak
	entry.SetMinRowsVisible(4)
	showForm(w, "Add contact", "Paste their card, or use a picture of their QR code.",
		"Add", entry, true,
		func(text string) error {
			return api.AddContact(context.Background(), text)
		},
		formButton{"Open QR image…", func(done func(error)) { openQRImage(api, w, done) }},
		formButton{"Paste QR from clipboard", func(done func(error)) { done(pasteQR(api)) }},
	)
}

// openQRImage asks for a PNG or JPEG file and adds the contact on the QR
// code in it, then calls done with the outcome. A cancelled file dialog
// does not call done.
func openQRImage(api core.API, w fyne.Window, done func(error)) {
	fd := dialog.NewFileOpen(func(in fyne.URIReadCloser, err error) {
		if err != nil {
			done(err)
			return
		}
		if in == nil { // cancelled
			return
		}
		defer in.Close()
		done(addFromImageFile(api, in))
	}, w)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg"}))
	fd.Show()
}

// addFromImageFile decodes a PNG or JPEG image from r and adds the contact
// on the QR code in it.
func addFromImageFile(api core.API, r io.Reader) error {
	img, err := identity.DecodeImage(r)
	if err != nil {
		return err
	}
	return api.AddContactFromImage(context.Background(), img)
}

// pasteQR adds the contact on the QR code in the clipboard image.
func pasteQR(api core.API) error {
	ctx, cancel := context.WithTimeout(context.Background(), pasteTimeout)
	defer cancel()
	img, err := clipimg.Read(ctx)
	if err != nil {
		return err
	}
	return api.AddContactFromImage(context.Background(), img)
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

// formButton is an extra button in a form, shown under the entry. Its run
// reports its outcome by calling done, possibly later: nil closes the
// dialog, an error is shown as submit's errors are.
type formButton struct {
	label string
	run   func(done func(error))
}

// showForm shows a dialog with prompt, entry, any extra buttons and an
// action button, plus a Cancel button if cancel is set. The action (or
// Enter in a single-line entry) runs submit on the entry text: on success
// the dialog closes, otherwise the error is shown at the bottom of the
// dialog and it stays open.
func showForm(w fyne.Window, title, prompt, action string, entry *widget.Entry,
	cancel bool, submit func(string) error, extras ...formButton) {
	errText := widget.NewLabel("")
	errText.Importance = widget.DangerImportance
	errText.Wrapping = fyne.TextWrapWord // the install hint is long
	errText.Hide()
	content := container.NewVBox(widget.NewLabel(prompt), entry)
	d := dialog.NewCustomWithoutButtons(title, content, w)

	done := func(err error) {
		if err != nil {
			errText.SetText(errtext.FriendlyError(err))
			errText.Show()
			d.Resize(fyne.NewSize(dialogWidth, 0)) // grow to fit errText
			return
		}
		d.Hide()
	}
	if len(extras) > 0 {
		row := container.NewHBox()
		for _, x := range extras {
			row.Add(widget.NewButton(x.label, func() { x.run(done) }))
		}
		content.Add(row)
	}
	content.Add(errText)

	run := func() { done(submit(entry.Text)) }
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

// sendLogNote says what Send log sends.
const sendLogNote = "Sends your log to the helper, so the problem can be looked into. " +
	"Your log, including contact names and network addresses. Never your messages."

// showSendLog sends the log to the helper with an optional note, then shows
// the report's code to pass on, with a Copy button. Sending can take a while,
// so async runs it off the UI goroutine and onUI brings the outcome back.
func showSendLog(api core.API, w fyne.Window, async, onUI func(func())) {
	prompt := widget.NewLabel(sendLogNote)
	prompt.Wrapping = fyne.TextWrapWord
	note := widget.NewMultiLineEntry()
	note.SetPlaceHolder("What went wrong? (optional)")
	note.Wrapping = fyne.TextWrapWord
	note.SetMinRowsVisible(3)
	errText := widget.NewLabel("")
	errText.Importance = widget.DangerImportance
	errText.Wrapping = fyne.TextWrapWord
	errText.Hide()
	d := dialog.NewCustomWithoutButtons("Send log", container.NewVBox(prompt, note, errText), w)

	var send, cancel *widget.Button
	busy := func(b bool) {
		if b {
			send.SetText("Sending…")
			send.Disable()
			cancel.Disable()
			note.Disable()
			return
		}
		send.SetText("Send")
		send.Enable()
		cancel.Enable()
		note.Enable()
	}
	send = widget.NewButton("Send", func() {
		text := note.Text
		if r := []rune(text); len(r) > 500 {
			text = string(r[:500])
		}
		errText.Hide()
		busy(true)
		async(func() {
			code, err := api.SendLog(context.Background(), text)
			onUI(func() {
				if err != nil {
					busy(false)
					errText.SetText(errtext.FriendlyError(err))
					errText.Show()
					d.Resize(fyne.NewSize(dialogWidth, 0)) // grow to fit errText
					return
				}
				d.Hide()
				showLogSent(w, code)
			})
		})
	})
	send.Importance = widget.HighImportance
	cancel = widget.NewButton("Cancel", d.Hide)
	d.SetButtons([]fyne.CanvasObject{cancel, send})
	d.Resize(fyne.NewSize(dialogWidth, 0))
	d.Show()
	w.Canvas().Focus(note)
}

// showLogSent shows the code of a sent log, with a Copy button.
func showLogSent(w fyne.Window, code string) {
	copyCode := widget.NewButton("Copy", func() { fyne.CurrentApp().Clipboard().SetContent(code) })
	content := container.NewVBox(
		widget.NewLabel("Log sent. Code: "+code),
		widget.NewLabel("Pass the code on to whoever looks into the problem."),
		container.NewHBox(layout.NewSpacer(), copyCode),
	)
	d := dialog.NewCustom("Log sent", "Close", content, w)
	d.Resize(fyne.NewSize(dialogWidth, 0))
	d.Show()
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
