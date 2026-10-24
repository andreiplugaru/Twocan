// Package errtext turns errors into the text Twocan shows the user. The CLI
// and the desktop UI share it so both say the same thing.
package errtext

import (
	"errors"

	"twocan/internal/chat"
	"twocan/internal/clipimg"
	"twocan/internal/contacts"
	"twocan/internal/core"
	"twocan/internal/identity"
)

// known maps errors to their user-facing text.
var known = []struct {
	err  error
	text string
}{
	{chat.ErrEmpty, "Message is empty"},
	{chat.ErrTooLong, "Message is too long (max 16 KB)"},
	{chat.ErrNotContact, "Not an accepted contact"},
	{chat.ErrInvalidUTF8, "Message is not valid UTF-8"},
	{identity.ErrDamagedCard, "Card is incomplete or damaged"},
	{identity.ErrNotACard, "That is not a Twocan contact card"},
	{identity.ErrUnsupportedVersion, "This card needs a newer version of Twocan"},
	{contacts.ErrSelf, "You cannot add yourself"},
	{contacts.ErrNotPending, "There is no pending request from this contact"},
	{core.ErrNameTooLong, "Name is too long (max 64 characters)"},
	{identity.ErrNoQRCode, "No QR code found in that image"},
	{clipimg.ErrNoTool, "Please install wl-clipboard (Wayland) or xclip (X11) to paste images"},
	{clipimg.ErrNoImage, "The clipboard has no image; copy a screenshot of the QR code first"},
}

// FriendlyError returns the user-facing text for err (which may wrap a known
// error). Unknown errors print as they are; none of them carry message text.
func FriendlyError(err error) string {
	for _, k := range known {
		if errors.Is(err, k.err) {
			return k.text
		}
	}
	return err.Error()
}
