# 3. The Fyne UI and the core API

Notes 1 and 2 were about the network. This one is about the desktop app on top of it: [internal/ui](../../internal/ui) (the window and dialogs) and [cmd/twocan/main.go](../../cmd/twocan/main.go) (the program that starts it).

## The UI only sees `core.API`

The UI never touches libp2p, the database or the contact and chat services directly. It gets one value of type `core.API`, an interface in [internal/core/node.go](../../internal/core/node.go) with methods like `Contacts`, `History`, `Send`, `AddContact`, `RenameContact`, `MyCard` and `Subscribe`. The real `*core.Node` implements it, and the text client (`twocan-cli`) talks to the node through the same interface. This buys two things:

- **Testing without a network.** [internal/ui/fake_test.go](../../internal/ui/fake_test.go) is a small in-memory `fakeAPI`. The UI tests build the real window on Fyne's test driver (no screen needed), tap real buttons with `test.Tap`, and check what the fake was asked to do (for example "`AddContact` was called once with this card") and what the window shows. They run in well under a second.
- **One place for the rules.** Name length, card decoding and message checks live in the core and its services. The UI only trims input and refuses a blank name; everything else comes back as an error, which `errtext.FriendlyError` turns into the sentence shown to the user (for example "Card is incomplete or damaged").

The API calls the UI makes are quick: they read or write the local database and return. Anything slow, like dialing a peer to deliver a message or a contact request, happens in the background (`Send` stores the message as pending and starts delivery on its own goroutine). So the UI calls them directly from button handlers.

## How events reach the UI

Fyne has one UI goroutine, and widgets must only be changed on it. Network work happens on other goroutines. The bridge is in `ui.Run` ([internal/ui/app.go](../../internal/ui/app.go)):

1. `api.Subscribe()` returns a channel of events from the core's event bus: a message arrived, a message was delivered, a contact changed, a peer connected or disconnected, our reachability changed. Each subscriber's channel holds up to 256 events; publishing never blocks, so a subscriber that falls that far behind misses events rather than stalling the network.
2. A goroutine reads that channel and, for each event, calls `fyne.Do(func() { v.apply(ev) })`. `fyne.Do` queues the function to run on the UI goroutine, so `apply` may change widgets safely.
3. `apply` decides what to update. A new message in the open conversation is appended; in another conversation, the contact list is reloaded so the unread count shows. A delivery ack turns the message's clock mark (🕓) into ✓. A contact or connectivity change reloads the contact list (that is how the dots go green). A reachability change updates the status bar.

A contact or connectivity event only says which peer changed; the UI then asks the API for the whole contact list again. The database stays the single source of truth; the UI keeps only a copy of what is on screen.

## How the lists are refreshed

Both panes use `widget.List`, which builds only the rows that are visible and recycles them as you scroll. A list is defined by three functions: how many items there are, how to make an empty row, and how to fill a row for item *i*. The data itself lives in a plain slice on `MainView` (`contacts`, `msgs`).

- **Contacts:** `reloadContacts` fetches `api.Contacts`, drops blocked ones, keeps the selection on the same person if they are still there, and calls `contactList.Refresh()`, which re-runs the fill function for the visible rows.
- **Conversation:** appending a message adds it to `msgs`, refreshes the list and scrolls to the bottom. A delivery tick changes one entry in `msgs` and calls `RefreshItem(i)` so only that row is redrawn. Messages wrap, so rows differ in height; before the list needs a row, its height is measured with an off-screen bubble at the current width and set with `SetItemHeight`. All rows are measured again when the conversation pane changes width.

## Dialogs

[internal/ui/dialogs.go](../../internal/ui/dialogs.go) holds four modal dialogs. **First run** asks for your name and has no Cancel button, so it stays up until a name is saved. **My card** shows your card as a QR code (made by `identity.CardQRPNG`) and as text, with Copy and "Save QR as PNG…". **Add contact** takes a pasted card. **Rename** (the "⋯" button on an accepted contact) sets a local nickname only you see. The three input dialogs share one helper: on error they show the friendly text under the entry and stay open, and on success they close.

## Starting and stopping

`cmd/twocan` reads `--data-dir` (default: `twocan` in your config directory, usually `~/.config/twocan`) and `--bootstrap` (unset means the built-in list; an explicit `--bootstrap ""` means none), starts the node with mDNS on, and calls `ui.Run`, which returns when you close the window. Then it calls `Stop`, which closes the network and the database. If the node fails to start, the error is printed and the program exits with status 1.
