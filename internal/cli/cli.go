// Package cli is a line-oriented text client for a running Twocan node. It
// reads commands from an io.Reader and writes results and asynchronous
// events to an io.Writer, so tests can drive it without a terminal.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/diagtext"
	"twocan/internal/errtext"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/p2p"
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
  /history <who>              show the conversation (each message with its <id>)
  /reply <who> <id> <text>    answer a message
  /edit <who> <id> <text>     change one of your messages
  /delete <who> <id> [all]    delete a message for you, or with "all" for everyone (yours only)
  /react <who> <id> <emoji>   react to a message ("-" removes your reaction)
  /search <words>             find messages in every conversation
  /stats <who>                in/out/pending message counts
  /info <who>                 connection details: how you're connected, last problem, saved addresses
  /check <who>                check step by step whether and how they can be reached
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
	case "/reply":
		c.withMessage(rest, "/reply <who> <id> <text>", true, func(v core.ContactView, m store.Message, text string) error {
			sent, err := c.api.Reply(c.ctx, v.PeerID, m.ID, text)
			if err == nil {
				c.say("queued %s", shortID(sent.ID))
			}
			return err
		})
	case "/edit":
		c.withMessage(rest, "/edit <who> <id> <text>", true, func(v core.ContactView, m store.Message, text string) error {
			return c.okIf(c.api.Edit(c.ctx, v.PeerID, m.ID, text))
		})
	case "/delete":
		c.withMessage(rest, "/delete <who> <id> [all]", false, func(v core.ContactView, m store.Message, arg string) error {
			if arg != "" && arg != "all" {
				c.say("usage: /delete <who> <id> [all]")
				return nil
			}
			return c.okIf(c.api.Delete(c.ctx, v.PeerID, m.ID, arg == "all"))
		})
	case "/react":
		c.withMessage(rest, "/react <who> <id> <emoji>", true, func(v core.ContactView, m store.Message, emoji string) error {
			if emoji == "-" {
				emoji = ""
			}
			return c.okIf(c.api.React(c.ctx, v.PeerID, m.ID, emoji))
		})
	case "/search":
		c.cmdSearch(rest)
	case "/stats":
		c.cmdStats(rest)
	case "/info":
		c.cmdInfo(rest)
	case "/check":
		c.cmdCheck(rest)
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
	img, err := identity.DecodeImage(f)
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
		c.say("%s", c.messageLine(v.Name(), m))
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

func (c *client) cmdInfo(who string) {
	if who == "" {
		c.say("usage: /info <who>")
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	if v.Connected {
		_, _ = c.api.Ping(c.ctx, v.PeerID) // refreshes the latency; best effort
	}
	d, err := c.api.ContactDetails(c.ctx, v.PeerID)
	if err != nil {
		c.fail(err)
		return
	}
	c.say("%s (%s)", v.Name(), v.PeerID)
	for _, f := range diagtext.Details(d, time.Now()) {
		lines := strings.Split(f.Value, "\n")
		c.say("  %s: %s", f.Label, lines[0])
		for _, l := range lines[1:] {
			c.say("    %s", l)
		}
	}
}

func (c *client) cmdCheck(who string) {
	if who == "" {
		c.say("usage: /check <who>")
		return
	}
	v, err := c.resolve(who)
	if err != nil {
		c.fail(err)
		return
	}
	c.say("checking the connection to %s (this can take a minute)…", v.Name())
	res, err := c.api.CheckConnection(c.ctx, v.PeerID, func(_ int, s p2p.Step) {
		if s.Status != p2p.StepRunning {
			c.say("  %s", diagtext.StepLine(s))
		}
	})
	if err != nil {
		c.fail(err)
		return
	}
	c.say("verdict: %s", res.Verdict)
}

// printEvent shows one asynchronous event.
func (c *client) printEvent(e any) {
	switch e := e.(type) {
	case event.MessageReceived:
		re := ""
		if e.Msg.ReplyTo != nil {
			re = " (re " + shortID(e.Msg.ReplyTo) + ")"
		}
		c.say("<- %s%s: %s [%s]", c.nameOf(e.Msg.PeerID), re, e.Msg.Text, shortID(e.Msg.ID))
	case event.MessagesUpdated:
		c.printUpdated(e)
	case event.Typing:
		c.say("* %s is typing…", c.nameOf(e.Peer))
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

// withMessage resolves "<who> <id> <rest>" to a contact and one of the
// messages /history shows, then calls fn. needRest requires <rest>.
func (c *client) withMessage(rest, usage string, needRest bool, fn func(core.ContactView, store.Message, string) error) {
	fields := strings.SplitN(rest, " ", 3)
	tail := ""
	if len(fields) == 3 {
		tail = strings.TrimSpace(fields[2])
	}
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" || (needRest && tail == "") {
		c.say("usage: %s", usage)
		return
	}
	v, err := c.resolve(fields[0])
	if err != nil {
		c.fail(err)
		return
	}
	m, err := c.findMessage(v.PeerID, fields[1])
	if err != nil {
		c.fail(err)
		return
	}
	if err := fn(v, m, tail); err != nil {
		c.fail(err)
	}
}

// findMessage finds the message in the conversation with id whose short ID
// (as /history prints it) starts with ref.
func (c *client) findMessage(id peer.ID, ref string) (store.Message, error) {
	msgs, err := c.api.History(c.ctx, id)
	if err != nil {
		return store.Message{}, err
	}
	ref = strings.ToLower(ref)
	var found []store.Message
	for _, m := range msgs {
		if strings.HasPrefix(shortID(m.ID), ref) {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 0:
		return store.Message{}, fmt.Errorf("no message %q in this conversation (see /history)", ref)
	case 1:
		return found[0], nil
	}
	return store.Message{}, fmt.Errorf("%q matches %d messages", ref, len(found))
}

// okIf prints "ok" when err is nil, and returns err.
func (c *client) okIf(err error) error {
	if err == nil {
		c.say("ok")
	}
	return err
}

// messageLine renders one message as /history and /search print it, e.g.
// "[15:04] 3fa2b1c0 me (re 9e01ab22): see you (read, edited) [them ❤️]".
func (c *client) messageLine(name string, m store.Message) string {
	sender, at, status := "me", m.SentAt, string(m.Status)
	if m.Direction == store.DirIn {
		sender, at, status = name, m.ReceivedAt, ""
	}
	if m.ReplyTo != nil {
		sender += " (re " + shortID(m.ReplyTo) + ")"
	}
	text := m.Text
	if m.Deleted {
		text, status = "(deleted)", ""
	}
	var notes []string
	if status != "" {
		notes = append(notes, status)
	}
	if !m.EditedAt.IsZero() && !m.Deleted {
		notes = append(notes, "edited")
	}
	line := fmt.Sprintf("[%s] %s %s: %s", at.Format("15:04"), shortID(m.ID), sender, text)
	if len(notes) > 0 {
		line += " (" + strings.Join(notes, ", ") + ")"
	}
	var reactions []string
	if m.MyReaction != "" {
		reactions = append(reactions, "me "+m.MyReaction)
	}
	if m.TheirReaction != "" {
		reactions = append(reactions, "them "+m.TheirReaction)
	}
	if len(reactions) > 0 {
		line += " [" + strings.Join(reactions, ", ") + "]"
	}
	return line
}

func (c *client) cmdSearch(query string) {
	if query == "" {
		c.say("usage: /search <words>")
		return
	}
	found, err := c.api.Search(c.ctx, query)
	if err != nil {
		c.fail(err)
		return
	}
	if len(found) == 0 {
		c.say("nothing found")
		return
	}
	for _, m := range found {
		name := c.nameOf(m.PeerID)
		c.say("%s: %s", name, c.messageLine(name, m))
	}
}

// printUpdated shows the messages an update changed, as they are now.
func (c *client) printUpdated(e event.MessagesUpdated) {
	msgs, err := c.api.History(c.ctx, e.Peer)
	if err != nil {
		return
	}
	name := c.nameOf(e.Peer)
	for _, id := range e.IDs {
		for _, m := range msgs {
			if bytes.Equal(m.ID, id) {
				c.say("~ %s", c.messageLine(name, m))
			}
		}
	}
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
