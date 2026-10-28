package ui

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/chat"
	"twocan/internal/store"
)

// reactionChoices are the reactions a message's menu offers.
var reactionChoices = []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}

// buildCompose creates the bar above the entry that says what is being
// replied to or edited, with a button to stop.
func (v *MainView) buildCompose() {
	v.composeLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	v.composeLabel.Truncation = fyne.TextTruncateEllipsis
	cancel := widget.NewButtonWithIcon("", theme.CancelIcon(), v.cancelCompose)
	cancel.Importance = widget.LowImportance
	v.composeBar = container.NewBorder(nil, nil, nil, cancel, v.composeLabel)
	v.composeBar.Hide()
}

// showMessageMenu opens the menu of message m at pos.
func (v *MainView) showMessageMenu(m store.Message, pos fyne.Position) {
	widget.ShowPopUpMenuAtPosition(v.messageMenu(m), v.win.Canvas(), pos)
}

// messageMenu is what can be done with message m: reply, react, copy, edit
// (ours), delete for me, delete for everyone (ours). A deleted message can
// only be deleted for me.
func (v *MainView) messageMenu(m store.Message) *fyne.Menu {
	id := v.selected
	deleteForMe := fyne.NewMenuItem("Delete for me", func() {
		dialog.ShowConfirm("Delete message?", "It is removed from this device only.", func(ok bool) {
			if ok {
				v.act(v.api.Delete(v.ctx, id, m.ID, false), id, m)
			}
		}, v.win)
	})
	if m.Deleted {
		return fyne.NewMenu("", deleteForMe)
	}
	c, _ := v.selectedContact()
	accepted := c.State == store.StateAccepted

	var reacts []*fyne.MenuItem
	for _, e := range reactionChoices {
		item := fyne.NewMenuItem(e, func() { v.act(v.api.React(v.ctx, id, m.ID, e), id, m) })
		item.Checked = m.MyReaction == e
		reacts = append(reacts, item)
	}
	if m.MyReaction != "" {
		reacts = append(reacts, fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Remove reaction", func() { v.act(v.api.React(v.ctx, id, m.ID, ""), id, m) }))
	}
	react := fyne.NewMenuItem("React", nil)
	react.ChildMenu = fyne.NewMenu("", reacts...)

	reply := fyne.NewMenuItem("Reply", func() { v.startReply(m) })
	copyText := fyne.NewMenuItem("Copy text", func() { fyne.CurrentApp().Clipboard().SetContent(m.Text) })
	items := []*fyne.MenuItem{reply, react, copyText}
	if !accepted {
		reply.Disabled, react.Disabled = true, true
	}
	if m.Direction == store.DirOut {
		edit := fyne.NewMenuItem("Edit", func() { v.startEdit(m) })
		edit.Disabled = !accepted
		items = append(items, edit)
	}
	items = append(items, fyne.NewMenuItemSeparator(), deleteForMe)
	if m.Direction == store.DirOut {
		everyone := fyne.NewMenuItem("Delete for everyone", func() {
			dialog.ShowConfirm("Delete for everyone?",
				"The message is replaced by \"This message was deleted\" here and on their device.", func(ok bool) {
					if ok {
						v.act(v.api.Delete(v.ctx, id, m.ID, true), id, m)
					}
				}, v.win)
		})
		everyone.Disabled = !accepted
		items = append(items, everyone)
	}
	return fyne.NewMenu("", items...)
}

// act shows the result of an action on message m of conversation id: the
// error, or the message as it is now.
func (v *MainView) act(err error, id peer.ID, m store.Message) {
	if err != nil {
		v.showError(err)
		return
	}
	v.messagesUpdated(id, [][]byte{m.ID})
}

// startReply makes the next message sent a reply to m.
func (v *MainView) startReply(m store.Message) {
	if v.editing != nil {
		v.entry.SetText("")
	}
	v.editing, v.replyTo = nil, &m
	v.composeLabel.SetText("Replying to " + v.snippet(m))
	v.composeBar.Show()
	v.send.SetText("Send")
	v.relayout()
	v.win.Canvas().Focus(v.entry)
}

// startEdit puts m's text in the entry; sending saves it as m's new text.
func (v *MainView) startEdit(m store.Message) {
	if v.editing == nil && v.entry.Text != "" {
		v.drafts[v.selected] = v.entry.Text // keep the draft for afterwards
	}
	v.replyTo, v.editing = nil, &m
	v.composeLabel.SetText("Editing your message")
	v.composeBar.Show()
	v.send.SetText("Save")
	v.relayout()
	v.entry.SetText(m.Text)
	v.win.Canvas().Focus(v.entry)
}

// cancelCompose stops replying or editing; an edit's text is dropped and
// the draft from before it comes back.
func (v *MainView) cancelCompose() {
	if v.editing != nil {
		v.entry.SetText(v.drafts[v.selected])
		delete(v.drafts, v.selected)
	}
	v.endCompose()
}

// endCompose hides the reply/edit bar.
func (v *MainView) endCompose() {
	wasEditing := v.editing != nil
	v.replyTo, v.editing = nil, nil
	v.send.SetText("Send")
	if v.composeBar.Visible() {
		v.composeBar.Hide()
		v.relayout()
	}
	if wasEditing && v.entry.Text == "" {
		v.entry.SetText(v.drafts[v.selected])
		delete(v.drafts, v.selected)
	}
}

// showTyping shows "typing…" for the open conversation with id, for
// chat.TypingFor unless it is shown again.
func (v *MainView) showTyping(id peer.ID) {
	if id != v.selected {
		return
	}
	v.typingGen++
	gen := v.typingGen
	v.typing.Show()
	v.relayout()
	time.AfterFunc(chat.TypingFor, func() {
		fyne.Do(func() {
			if v.typingGen == gen {
				v.hideTyping()
			}
		})
	})
}

func (v *MainView) hideTyping() {
	v.typingGen++
	if v.typing.Visible() {
		v.typing.Hide()
		v.relayout()
	}
}
