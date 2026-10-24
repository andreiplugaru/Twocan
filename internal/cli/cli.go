// Package cli is a line-oriented text client for a running Twocan node. It
// reads commands from an io.Reader and writes results and asynchronous
// events to an io.Writer, so tests can drive it without a terminal.
package cli

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // image.Decode reads JPEG
	_ "image/png"  // and PNG
	"io"
	"os"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/errtext"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/store"
)

// maxLine bounds one input line: a 16 KiB message plus a command and slack.
const maxLine = 64 * 1024

const helpText = `commands:
  /card                       print your contact card
  /add <card>                 add a contact from their card
  /addqr <image file>         add a contact from a QR code image (PNG/JPEG)
  /accept <who>               accept a pending request
  /reject <who>               reject (block) a contact
  /rename <who> <nickname>    set a local nickname
  /contacts                   list contacts
  /msg <who> <text>           send a message
  /history <who>              show the conversation
  /stats <who>                in/out/pending message counts
  /status                     your peer ID and reachability
  /quit                       exit`

// client holds one running CLI session.
type client struct {
	api core.API
	ctx context.Context

	mu  sync.Mutex // serializes writes to out, one whole line at a time
	out io.Writer
}

// say writes one complete line; safe for concurrent use.
func (c *client) say(format string, args ...any) {
	line := fmt.Sprintf(format, args...) + "\n"
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.WriteString(c.out, line)
}

func (c *client) fail(err error) { c.say("error: %s", errtext.FriendlyError(err)) }

// Run reads commands from in until /quit, end of input or ctx is done, and
// prints events from api as they happen. It does not stop api.
func Run(ctx context.Context, api core.API, in io.Reader, out io.Writer) error {
	c := &client{api: api, ctx: ctx, out: out}
	events, cancel := api.Subscribe()
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case e, ok := <-events:
				if !ok {
					return
				}
				c.printEvent(e)
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	// Reading in a goroutine lets ctx cancel a blocked read.
	lines := make(chan string)
	scanErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 4096), maxLine)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
		scanErr <- sc.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-scanErr:
					return err
				default:
					return nil
				}
			}
			if c.handle(line) {
				return nil
			}
		}
	}
}

// handle runs one command line and reports whether to quit.
func (c *client) handle(line string) (quit bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch cmd {
	case "/quit":
		return true
	case "/help":
		c.say("%s", helpText)
	case "/card":
		card, err := c.api.MyCard()
		c.result(err, func() { c.say("%s", card) })
	case "/add":
		c.cmdAdd(rest)
	case "/addqr":
		c.cmdAddQR(rest)
	case "/accept":
		c.withContact(rest, "/accept <who>", func(v core.ContactView, _ string) error {
			return c.api.AcceptContact(c.ctx, v.PeerID)
		}, false)
	case "/reject":
		c.withContact(rest, "/reject <who>", func(v core.ContactView, _ string) error {
			return c.api.RejectContact(c.ctx, v.PeerID)
		}, false)
	case "/rename":
		c.withContact(rest, "/rename <who> <nickname>", func(v core.ContactView, nick string) error {
			return c.api.RenameContact(c.ctx, v.PeerID, nick)
		}, true)
	case "/contacts":
		c.cmdContacts()
	case "/msg":
		c.cmdMsg(rest)
	case "/history":
		c.cmdHistory(rest)
	case "/stats":
		c.cmdStats(rest)
	case "/status":
		c.say("peer=%s reachability=%s", c.api.PeerID(), reachText(c.api.Reachability()))
	default:
		c.say("unknown command, try /help")
	}
	return false
}

// result prints err, or runs ok when err is nil.
func (c *client) result(err error, ok func()) {
	if err != nil {
		c.fail(err)
		return
	}
	ok()
}

func (c *client) cmdAdd(card string) {
	if card == "" {
		c.say("usage: /add <card>")
		return
	}
	if err := c.api.AddContact(c.ctx, card); err != nil {
		c.fail(err)
		return
	}
	// The new contact is the one we just added; find it by decoding the card
	// again, so the output names them.
	dec, err := identity.DecodeCard(card)
	c.sayAdded(dec, err)
}

func (c *client) cmdAddQR(path string) {
	if path == "" {
		c.say("usage: /addqr <image file>")
		return
	}
	img, err := decodeImageFile(path)
	if err != nil {
		c.fail(err)
		return
	}
	if err := c.api.AddContactFromImage(c.ctx, img); err != nil {
		c.fail(err)
		return
	}
	dec, err := identity.DecodeCardQR(img)
	c.sayAdded(dec, err)
}

// decodeImageFile reads a PNG or JPEG image from path.
func decodeImageFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

// sayAdded reports a contact just added from card dec, naming them as our
// contact list does. decErr is the error from decoding the card again; on
// error the contact is not named.
func (c *client) sayAdded(dec identity.Card, decErr error) {
	name := "contact"
	if decErr == nil {
		name = dec.PeerID.String()
		if v, ok := c.lookup(dec.PeerID); ok {
			name = v.Name()
		}
	}
	c.say("added %s (request pending)", name)
}

// withContact resolves the first word of rest as <who>, then calls fn with
// the contact and the remaining text. needRest requires non-empty remaining
// text. On success it prints "ok".
func (c *client) withContact(rest, usage string, fn func(core.ContactView, string) error, needRest bool) {
	who, tail, _ := strings.Cut(rest, " ")
	tail = strings.TrimSpace(tail)
	if who == "" || (needRest && tail == "") {
		c.say("usage: %s", usage)
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	if err := fn(v, tail); err != nil {
		c.fail(err)
		return
	}
	c.say("ok")
}

// resolve finds the single contact whose name or peer ID starts with who.
func (c *client) resolve(who string) (core.ContactView, error) {
	vs, err := c.api.Contacts(c.ctx)
	if err != nil {
		return core.ContactView{}, err
	}
	lower := strings.ToLower(who)
	var found []core.ContactView
	for _, v := range vs {
		if strings.HasPrefix(strings.ToLower(v.Name()), lower) ||
			strings.HasPrefix(v.PeerID.String(), who) {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 0:
		return core.ContactView{}, fmt.Errorf("no contact matches %q", who)
	case 1:
		return found[0], nil
	}
	return core.ContactView{}, fmt.Errorf("%q matches %d contacts", who, len(found))
}

func (c *client) cmdContacts() {
	vs, err := c.api.Contacts(c.ctx)
	if err != nil {
		c.fail(err)
		return
	}
	if len(vs) == 0 {
		c.say("no contacts yet")
		return
	}
	for _, v := range vs {
		conn := "offline"
		if v.Connected {
			conn = "online " + linkText(v.Relayed)
		}
		c.say("%s %s %s %s unread=%d", v.State, v.Name(), shortPeer(v.PeerID), conn, v.Unread)
	}
}

func (c *client) cmdMsg(rest string) {
	who, text, _ := strings.Cut(rest, " ")
	text = strings.TrimSpace(text)
	if who == "" || text == "" {
		c.say("usage: /msg <who> <text>")
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	m, err := c.api.Send(c.ctx, v.PeerID, text)
	if err != nil {
		c.fail(err)
		return
	}
	c.say("queued %s", shortID(m.ID))
}

func (c *client) cmdHistory(who string) {
	if who == "" {
		c.say("usage: /history <who>")
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	msgs, err := c.api.History(c.ctx, v.PeerID)
	if err != nil {
		c.fail(err)
		return
	}
	for _, m := range msgs {
		sender, at := "me", m.SentAt
		if m.Direction == store.DirIn {
			sender, at = v.Name(), m.ReceivedAt
		}
		c.say("[%s] %s: %s (%s)", at.Format("15:04"), sender, m.Text, m.Status)
	}
	if err := c.api.MarkRead(c.ctx, v.PeerID); err != nil {
		c.fail(err)
	}
}

func (c *client) cmdStats(who string) {
	if who == "" {
		c.say("usage: /stats <who>")
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	msgs, err := c.api.History(c.ctx, v.PeerID)
	if err != nil {
		c.fail(err)
		return
	}
	var in, out, pending int
	for _, m := range msgs {
		switch m.Direction {
		case store.DirIn:
			in++
		case store.DirOut:
			out++
			if m.Status == store.StatusPending {
				pending++
			}
		}
	}
	c.say("in=%d out=%d pending=%d", in, out, pending)
}

// printEvent shows one asynchronous event.
func (c *client) printEvent(e any) {
	switch e := e.(type) {
	case event.MessageReceived:
		c.say("<- %s: %s", c.nameOf(e.Msg.PeerID), e.Msg.Text)
	case event.MessageDelivered:
		c.say("✓ delivered %s", shortID(e.ID))
	case event.ContactChanged:
		if v, ok := c.lookup(e.Peer); ok {
			c.say("* contact %s is now %s", v.Name(), v.State)
		}
	case event.Connectivity:
		if e.Connected {
			c.say("* %s online (%s)", c.nameOf(e.Peer), linkText(e.Relayed))
		} else {
			c.say("* %s offline", c.nameOf(e.Peer))
		}
	}
}

func (c *client) lookup(id peer.ID) (core.ContactView, bool) {
	vs, err := c.api.Contacts(c.ctx)
	if err != nil {
		return core.ContactView{}, false
	}
	for _, v := range vs {
		if v.PeerID == id {
			return v, true
		}
	}
	return core.ContactView{}, false
}

func (c *client) nameOf(id peer.ID) string {
	if v, ok := c.lookup(id); ok {
		return v.Name()
	}
	return shortPeer(id)
}

func linkText(relayed bool) string {
	if relayed {
		return "relayed"
	}
	return "direct"
}

func reachText(r network.Reachability) string {
	switch r {
	case network.ReachabilityPublic:
		return "public"
	case network.ReachabilityPrivate:
		return "private"
	}
	return "unknown"
}

// shortPeer is the last 6 characters of a peer ID, as Contact.Name uses.
func shortPeer(id peer.ID) string {
	s := id.String()
	if len(s) > 6 {
		s = s[len(s)-6:]
	}
	return s
}

// shortID is the last 4 bytes of a message ID in hex (IDs are UUIDv7, whose
// leading bytes are a timestamp and look alike).
func shortID(id []byte) string {
	return hex.EncodeToString(id[max(len(id)-4, 0):])
}
