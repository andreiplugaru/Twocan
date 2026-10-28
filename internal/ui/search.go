package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"twocan/internal/richtext"
	"twocan/internal/store"
)

// buildSearch creates the search box above the contact list.
func (v *MainView) buildSearch() {
	v.search = widget.NewEntry()
	v.search.SetPlaceHolder("Search messages")
	v.search.OnSubmitted = func(q string) { v.runSearch(q) }
}

// runSearch shows the messages matching q in a dialog; choosing one opens
// its conversation at that message.
func (v *MainView) runSearch(q string) {
	if strings.TrimSpace(q) == "" {
		return
	}
	found, err := v.api.Search(v.ctx, q)
	if err != nil {
		v.showError(err)
		return
	}
	if len(found) == 0 {
		dialog.ShowInformation("Search", "No messages contain \""+q+"\".", v.win)
		return
	}
	var d dialog.Dialog
	list := widget.NewList(
		func() int { return len(found) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(v.resultLine(found[i])) })
	list.OnSelected = func(i widget.ListItemID) {
		d.Hide()
		v.openMessage(found[i])
	}
	d = dialog.NewCustom("Search: "+q, "Close", list, v.win)
	d.Resize(fyne.NewSize(600, 400))
	v.searchResults = list
	d.Show()
}

// resultLine is "Ann · 28 Oct 15:04 — the message" for a search result.
func (v *MainView) resultLine(m store.Message) string {
	who := string(m.PeerID)
	for _, c := range v.contacts {
		if c.PeerID == m.PeerID {
			who = c.Name()
		}
	}
	if m.Direction == store.DirOut {
		who = "You to " + who
	}
	at := m.SentAt
	if m.Direction == store.DirIn {
		at = m.ReceivedAt
	}
	text := strings.Join(strings.Fields(richtext.Plain(m.Text)), " ")
	return who + " · " + at.Format("2 Jan 15:04") + " — " + text
}

// openMessage selects m's conversation and scrolls to m.
func (v *MainView) openMessage(m store.Message) {
	i := v.indexOf(m.PeerID)
	if i < 0 {
		return // a blocked contact's conversation is not shown
	}
	v.contactList.Select(i)
	if j := v.indexOfID(m.ID); j >= 0 {
		v.convo.ScrollTo(j)
	}
}
