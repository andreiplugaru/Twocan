package ui

import (
	"bytes"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/errtext"
	"twocan/internal/store"
)

// bubbleWidth is the share of the conversation width a bubble takes.
const bubbleWidth = 0.75

// buildChat creates the right pane's widgets.
func (v *MainView) buildChat() {
	v.heights = make(map[widget.ListItemID]float32)
	v.convo = widget.NewList(
		func() int { return len(v.msgs) },
		func() fyne.CanvasObject { return newBubble() },
		v.updateBubble)
	v.convo.HideSeparators = true
	v.convo.OnSelected = func(widget.ListItemID) { v.convo.UnselectAll() }

	v.entry = widget.NewEntry()
	v.entry.SetPlaceHolder("Write a message")
	v.entry.OnSubmitted = func(string) { v.sendEntry() }
	v.send = widget.NewButton("Send", v.sendEntry)
	v.sendErr = widget.NewLabel("")
	v.sendErr.Importance = widget.DangerImportance
	v.sendErr.Wrapping = fyne.TextWrapWord
	v.sendErr.Hide()
}

// updateBubble fills a conversation row and sizes it to fit its wrapped text.
func (v *MainView) updateBubble(id widget.ListItemID, o fyne.CanvasObject) {
	b := o.(*bubble)
	b.set(v.msgs[id])
	// Lay the bubble out at the row width so the text wraps, then give the
	// row the height the wrapped text needs.
	b.Resize(fyne.NewSize(v.convo.Size().Width, b.Size().Height))
	if h := b.MinSize().Height; v.heights[id] != h {
		v.heights[id] = h
		v.convo.SetItemHeight(id, h)
	}
}

// showConversation replaces the conversation pane's messages.
func (v *MainView) showConversation(msgs []store.Message) {
	v.msgs = msgs
	clear(v.heights)
	v.convo.Refresh()
	v.convo.ScrollToBottom()
}

// appendMessage adds m to the bottom of the conversation.
func (v *MainView) appendMessage(m store.Message) {
	v.msgs = append(v.msgs, m)
	v.convo.Refresh()
	v.convo.ScrollToBottom()
}

// updateEntry enables the entry and Send only for an accepted contact.
func (v *MainView) updateEntry() {
	c, ok := v.selectedContact()
	if ok && c.State == store.StateAccepted {
		v.entry.Enable()
		v.send.Enable()
	} else {
		v.entry.Disable()
		v.send.Disable()
	}
}

// sendEntry sends the entry's text to the selected contact. On error the
// text stays in the entry and the error shows above it.
func (v *MainView) sendEntry() {
	if v.selected == "" || v.entry.Disabled() {
		return
	}
	m, err := v.api.Send(v.ctx, v.selected, v.entry.Text)
	if err != nil {
		v.sendErr.SetText(errtext.FriendlyError(err))
		v.sendErr.Show()
		return
	}
	v.sendErr.Hide()
	v.entry.SetText("")
	v.appendMessage(m)
}

// messageReceived shows m if its conversation is open (and marks it read);
// otherwise it reloads the contacts so the sender's unread count shows.
func (v *MainView) messageReceived(m store.Message) {
	if m.PeerID != v.selected {
		v.reloadContacts()
		return
	}
	v.appendMessage(m)
	v.markRead()
}

// messageDelivered ticks the delivered message if it is on screen.
func (v *MainView) messageDelivered(from peer.ID, id []byte) {
	if from != v.selected {
		return
	}
	for i := range v.msgs {
		if v.msgs[i].Direction == store.DirOut && bytes.Equal(v.msgs[i].ID, id) {
			v.msgs[i].Status = store.StatusDelivered
			v.convo.RefreshItem(i)
			return
		}
	}
}

// bubble is one message in the conversation. Outgoing bubbles sit on the
// right, incoming ones on the left.
type bubble struct {
	widget.BaseWidget
	bg   *canvas.Rectangle
	text *widget.Label
	meta *widget.Label // "15:04", plus 🕓 or ✓ when outgoing
	out  bool
}

func newBubble() *bubble {
	b := &bubble{
		bg:   canvas.NewRectangle(theme.Color(theme.ColorNameInputBackground)),
		text: widget.NewLabel(""),
		meta: widget.NewLabelWithStyle("", fyne.TextAlignTrailing, fyne.TextStyle{Italic: true}),
	}
	b.bg.CornerRadius = theme.Size(theme.SizeNameInputRadius)
	b.text.Wrapping = fyne.TextWrapWord
	b.meta.SizeName = theme.SizeNameCaptionText
	b.ExtendBaseWidget(b)
	return b
}

func (b *bubble) set(m store.Message) {
	b.out = m.Direction == store.DirOut
	at, mark := m.ReceivedAt, ""
	if b.out {
		at, mark = m.SentAt, " 🕓"
		if m.Status == store.StatusDelivered {
			mark = " ✓"
		}
	}
	b.text.SetText(m.Text)
	b.meta.SetText(at.Format("15:04") + mark)
	b.bg.FillColor = theme.Color(theme.ColorNameInputBackground)
	if b.out {
		b.bg.FillColor = theme.Color(theme.ColorNameSelection)
	}
	b.Refresh()
}

func (b *bubble) CreateRenderer() fyne.WidgetRenderer {
	return &bubbleRenderer{b: b, box: container.NewVBox(b.text, b.meta)}
}

type bubbleRenderer struct {
	b   *bubble
	box *fyne.Container
}

// Layout places the bubble at bubbleWidth of the row, on its side.
func (r *bubbleRenderer) Layout(size fyne.Size) {
	w := size.Width * bubbleWidth
	x := float32(0)
	if r.b.out {
		x = size.Width - w
	}
	r.box.Move(fyne.NewPos(x, 0))
	r.box.Resize(fyne.NewSize(w, size.Height))
	r.b.bg.Move(r.box.Position())
	r.b.bg.Resize(r.box.Size())
}

func (r *bubbleRenderer) MinSize() fyne.Size { return r.box.MinSize() }

func (r *bubbleRenderer) Refresh() {
	r.Layout(r.b.Size())
	r.b.bg.Refresh()
	r.box.Refresh()
}

func (r *bubbleRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.b.bg, r.box}
}

func (r *bubbleRenderer) Destroy() {}
