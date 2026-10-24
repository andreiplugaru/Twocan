// Package ui is the Twocan desktop app, built with Fyne. It talks to the node
// only through core.API and the events from its Subscribe channel.
package ui

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/event"
	"twocan/internal/store"
)

// MainView is the main window: contacts on the left, the conversation with
// the selected contact on the right, and a status bar along the bottom. All
// its methods run on the Fyne UI goroutine.
type MainView struct {
	api core.API
	win fyne.Window
	ctx context.Context

	// Dialogs opened by the "My card" and "Add contact" buttons and, from
	// Run, on first start (dialogs.go). A nil func does nothing.
	showMyCard, showAddContact, showFirstRun func()

	// Left pane (contacts.go).
	name        *widget.Label
	contacts    []core.ContactView // shown rows: store order, blocked left out
	contactList *widget.List
	selected    peer.ID // "" when no contact is selected

	// Right pane (chat.go).
	msgs       []store.Message // conversation with selected, oldest first
	convo      *widget.List
	convoBox   *fyne.Container // convo, watched for width changes
	convoWidth float32
	measurer   *bubble // off-screen bubble used to measure row heights
	entry      *widget.Entry
	send       *widget.Button
	sendErr    *widget.Label

	// Status bar (status.go).
	status *widget.Label
	reach  network.Reachability
}

// NewMainView builds the main view and loads the contact list. Put its
// Content in w.
func NewMainView(api core.API, w fyne.Window) *MainView {
	v := &MainView{api: api, win: w, ctx: context.Background(), reach: api.Reachability()}
	v.showMyCard = func() { showMyCard(api, w) }
	v.showAddContact = func() { showAddContact(api, w) }
	v.showFirstRun = func() { showFirstRun(api, w, v.refreshName) }
	v.buildContacts()
	v.buildChat()
	v.buildStatus()
	v.refreshName()
	v.reloadContacts()
	v.updateEntry()
	return v
}

// Content returns the window content.
func (v *MainView) Content() fyne.CanvasObject {
	myCard := widget.NewButton("My card", func() { call(v.showMyCard) })
	addContact := widget.NewButton("Add contact", func() { call(v.showAddContact) })
	header := container.NewBorder(nil, nil, nil, myCard, v.name)
	left := container.NewBorder(header, addContact, nil, nil, v.contactList)

	bar := container.NewBorder(v.sendErr, nil, nil, v.send, v.entry)
	right := container.NewBorder(nil, bar, nil, nil, v.convoBox)

	split := container.NewHSplit(left, right)
	split.Offset = 0.3
	return container.NewBorder(nil, v.status, nil, nil, split)
}

// Run opens the Twocan window and runs the app until it is closed. It does
// not stop api.
func Run(api core.API) {
	a := app.NewWithID("app.twocan")
	w := a.NewWindow("Twocan")
	w.Resize(fyne.NewSize(900, 600))
	v := NewMainView(api, w)
	w.SetContent(v.Content())

	events, cancel := api.Subscribe()
	defer cancel()
	go func() {
		for ev := range events {
			fyne.Do(func() { v.apply(ev) })
		}
	}()

	if api.DisplayName() == "" {
		call(v.showFirstRun)
	}
	w.ShowAndRun()
}

// apply updates the view for one event from core.API.Subscribe.
func (v *MainView) apply(ev any) {
	switch e := ev.(type) {
	case event.MessageReceived:
		v.messageReceived(e.Msg)
	case event.MessageDelivered:
		v.messageDelivered(e.Peer, e.ID)
	case event.ContactChanged, event.Connectivity:
		v.reloadContacts()
	case event.Reachability:
		v.reach = e.Status
		v.updateStatus()
	}
}

// showError shows err to the user in a dialog.
func (v *MainView) showError(err error) { errorDialog(v.win, err) }

func call(f func()) {
	if f != nil {
		f()
	}
}
