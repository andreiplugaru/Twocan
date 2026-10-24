package ui

import (
	"github.com/libp2p/go-libp2p/core/network"

	"fyne.io/fyne/v2/widget"
)

// buildStatus creates the status bar.
func (v *MainView) buildStatus() {
	v.status = widget.NewLabel("")
}

// updateStatus shows our reachability and the link to the selected contact.
func (v *MainView) updateStatus() {
	text := "You are reachable: " + reachText(v.reach)
	if c, ok := v.selectedContact(); ok {
		switch {
		case !c.Connected:
			text += " · offline"
		case c.Relayed:
			text += " · via relay"
		default:
			text += " · direct"
		}
	}
	v.status.SetText(text)
}

func reachText(r network.Reachability) string {
	switch r {
	case network.ReachabilityPublic:
		return "public"
	case network.ReachabilityPrivate:
		return "behind NAT"
	}
	return "checking…"
}
