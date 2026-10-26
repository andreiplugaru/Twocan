package ui

import (
	"fyne.io/fyne/v2/widget"

	"twocan/internal/diagtext"
)

// buildStatus creates the status bar.
func (v *MainView) buildStatus() {
	v.status = widget.NewLabel("")
}

// updateStatus shows our reachability and the link to the selected contact.
func (v *MainView) updateStatus() {
	text := "You are reachable: " + diagtext.Reachability(v.reach)
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
