package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/store"
)

// dotSize is the diameter of the connection dot on a contact row.
const dotSize = 10

func connectedColor() color.Color { return theme.Color(theme.ColorNameSuccess) }
func offlineColor() color.Color   { return theme.Color(theme.ColorNameDisabled) }

// buildContacts creates the left pane's widgets.
func (v *MainView) buildContacts() {
	v.name = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	v.name.Truncation = fyne.TextTruncateEllipsis
	v.contactList = widget.NewList(
		func() int { return len(v.contacts) },
		func() fyne.CanvasObject { return newContactRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			c := v.contacts[id]
			o.(*contactRow).set(c,
				func() { v.accept(c.PeerID) },
				func() { v.reject(c.PeerID) })
		})
	v.contactList.OnSelected = func(id widget.ListItemID) { v.selectContact(v.contacts[id].PeerID) }
}

// refreshName shows our own display name in the header.
func (v *MainView) refreshName() {
	v.name.SetText(v.api.DisplayName())
}

// reloadContacts reloads the contact list from the node, keeps the selection
// on the same contact (or clears it if the contact is gone or blocked), and
// updates the entry and status bar.
func (v *MainView) reloadContacts() {
	vs, err := v.api.Contacts(v.ctx)
	if err != nil {
		v.showError(err)
		return
	}
	v.contacts = v.contacts[:0]
	for _, c := range vs {
		if c.State != store.StateBlocked {
			v.contacts = append(v.contacts, c)
		}
	}
	if v.selected != "" {
		if i := v.indexOf(v.selected); i >= 0 {
			v.contactList.Select(i) // no-op if already selected; selectContact ignores the same peer
		} else {
			v.contactList.UnselectAll()
			v.selected = ""
			v.showConversation(nil)
		}
	}
	v.contactList.Refresh()
	v.updateEntry()
	v.updateStatus()
}

func (v *MainView) indexOf(id peer.ID) int {
	for i, c := range v.contacts {
		if c.PeerID == id {
			return i
		}
	}
	return -1
}

// selectedContact returns the selected contact, if any.
func (v *MainView) selectedContact() (core.ContactView, bool) {
	if i := v.indexOf(v.selected); i >= 0 {
		return v.contacts[i], true
	}
	return core.ContactView{}, false
}

// selectContact shows the conversation with id and marks it read.
func (v *MainView) selectContact(id peer.ID) {
	if id == v.selected {
		return
	}
	v.selected = id
	v.sendErr.Hide()
	msgs, err := v.api.History(v.ctx, id)
	if err != nil {
		v.showError(err)
	}
	v.showConversation(msgs)
	v.markRead()
	v.updateEntry()
	v.updateStatus()
}

// markRead marks the selected conversation read and clears its badge.
func (v *MainView) markRead() {
	if err := v.api.MarkRead(v.ctx, v.selected); err != nil {
		v.showError(err)
		return
	}
	if i := v.indexOf(v.selected); i >= 0 && v.contacts[i].Unread != 0 {
		v.contacts[i].Unread = 0
		v.contactList.RefreshItem(i)
	}
}

func (v *MainView) accept(id peer.ID) {
	if err := v.api.AcceptContact(v.ctx, id); err != nil {
		v.showError(err)
	}
}

func (v *MainView) reject(id peer.ID) {
	if err := v.api.RejectContact(v.ctx, id); err != nil {
		v.showError(err)
	}
}

// contactRow is one row of the contact list.
type contactRow struct {
	widget.BaseWidget
	dot    *canvas.Circle
	name   *widget.Label
	unread *widget.Label
	note   *widget.Label // "wants to add you" / "request sent"

	accept, reject *widget.Button // pending_incoming only
	more           *widget.Button // accepted only; rename comes with the dialogs
}

func newContactRow() *contactRow {
	r := &contactRow{
		dot:    canvas.NewCircle(offlineColor()),
		name:   widget.NewLabel(""),
		unread: widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		note:   widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		accept: widget.NewButton("Accept", nil),
		reject: widget.NewButton("Reject", nil),
		more:   widget.NewButton("⋯", nil),
	}
	r.name.Truncation = fyne.TextTruncateEllipsis
	r.accept.Importance = widget.HighImportance
	r.ExtendBaseWidget(r)
	return r
}

func (r *contactRow) CreateRenderer() fyne.WidgetRenderer {
	dot := container.NewCenter(container.NewGridWrap(fyne.NewSquareSize(dotSize), r.dot))
	right := container.NewHBox(r.unread, r.note, r.accept, r.reject, r.more)
	return widget.NewSimpleRenderer(container.NewBorder(nil, nil, dot, right, r.name))
}

// set shows c; onAccept and onReject run when its buttons are tapped.
func (r *contactRow) set(c core.ContactView, onAccept, onReject func()) {
	r.name.SetText(c.Name())

	r.dot.FillColor = offlineColor()
	if c.Connected {
		r.dot.FillColor = connectedColor()
	}
	r.dot.Refresh()

	r.unread.SetText(fmt.Sprintf("(%d)", c.Unread))
	showIf(r.unread, c.Unread > 0)

	switch c.State {
	case store.StatePendingIncoming:
		r.note.SetText("wants to add you")
	case store.StatePendingOutgoing:
		r.note.SetText("request sent")
	default:
		r.note.SetText("")
	}
	showIf(r.note, r.note.Text != "")

	r.accept.OnTapped, r.reject.OnTapped = onAccept, onReject
	showIf(r.accept, c.State == store.StatePendingIncoming)
	showIf(r.reject, c.State == store.StatePendingIncoming)
	showIf(r.more, c.State == store.StateAccepted)
}

func showIf(o fyne.CanvasObject, show bool) {
	if show {
		o.Show()
	} else {
		o.Hide()
	}
}
