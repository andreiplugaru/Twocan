package errtext

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"twocan/internal/chat"
	"twocan/internal/clipimg"
	"twocan/internal/contacts"
	"twocan/internal/core"
	"twocan/internal/identity"
)

func TestFriendlyError(t *testing.T) {
	cases := []struct {
		err  error
		want string
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
		{identity.ErrImageTooLarge, "That image is too large (max 40 megapixels)"},
		{clipimg.ErrNoTool, "Please install wl-clipboard (Wayland) or xclip (X11) to paste images"},
		{clipimg.ErrNoImage, "The clipboard has no image; copy a screenshot of the QR code first"},
		// Wrapped errors are recognised.
		{fmt.Errorf("send: %w", chat.ErrTooLong), "Message is too long (max 16 KB)"},
		// Unknown errors print as they are.
		{errors.New("disk on fire"), "disk on fire"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			require.Equal(t, c.want, FriendlyError(c.err))
		})
	}
}
