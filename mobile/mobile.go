// Package mobile is the Go side of the Twocan Android app: gomobile bind
// turns it into an Android library that the Kotlin app calls. It wraps
// core.API in what gomobile can pass to Kotlin (strings, numbers, []byte
// and interfaces), sending lists and records as JSON, and errors with the
// same user-facing text the desktop app shows.
//
// Every method blocks; call them off the Android main thread. Listener
// and CheckListener methods are called on Go goroutines.
package mobile

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/diagtext"
	"twocan/internal/errtext"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/logging"
	"twocan/internal/p2p"
	"twocan/internal/store"
)

// Listener receives the node's events, each a JSON object with a "type":
//
//	{"type":"message","message":<Message>}        a message arrived
//	{"type":"delivered","peer":"…","id":"…"}      a sent message was delivered
//	{"type":"contacts"}                           the contact list changed
//	{"type":"reachability","text":"public"}       our reachability changed
//
// See contactJSON and messageJSON for the record shapes.
type Listener interface {
	OnEvent(json string)
}

// CheckListener follows a connection check. OnStep is called as each step
// starts and ends; status is "running", "ok", "info", "warn", "fail" or
// "skipped". OnDone is called once at the end, with the verdict and a
// report to copy (empty if the check could not run).
type CheckListener interface {
	OnStep(index int, title, detail, status string)
	OnDone(verdict, report string)
}

// Node is a running Twocan node.
type Node struct {
	api        core.API
	log        *logging.Log
	stopEvents func()
	eventsDone chan struct{}
}

// testOptions, if set by a test, adjusts the options Start uses.
var testOptions func(*core.Options)

// Start starts the node whose identity, contacts and history live in
// dataDir. bootstrap is a comma-separated list of helper addresses; ""
// means the ones built in. logLevel is debug, info, warn or error (""
// means info); the log is twocan.log in dataDir. l gets the node's events
// until Stop.
func Start(dataDir, bootstrap, logLevel string, l Listener) (*Node, error) {
	if logLevel == "" {
		logLevel = "info"
	}
	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		return nil, err
	}
	lg, err := logging.Open(dataDir, level)
	if err != nil {
		return nil, err
	}
	opts := core.Options{
		DataDir: dataDir,
		// mDNS cannot list the interfaces on Android 11+; the helper finds
		// contacts on the same network instead.
		EnableMDNS:   false,
		InterfaceIPs: interfaceIPs,
		Log:          lg.Logger,
	}
	if bootstrap != "" {
		opts.Bootstrap = splitList(bootstrap)
	}
	if testOptions != nil {
		testOptions(&opts)
	}
	api, err := core.Start(context.Background(), opts)
	if err != nil {
		lg.Error("start failed", "err", err)
		lg.Close()
		return nil, err
	}
	events, cancel := api.Subscribe()
	n := &Node{api: api, log: lg, stopEvents: cancel, eventsDone: make(chan struct{})}
	go func() {
		defer close(n.eventsDone)
		for ev := range events {
			if js, ok := eventJSON(ev); ok {
				l.OnEvent(js)
			}
		}
	}()
	return n, nil
}

// Stop stops the node and closes its log. No events are sent after it
// returns.
func (n *Node) Stop() error {
	n.stopEvents()
	<-n.eventsDone
	err := n.api.Stop()
	n.log.Close()
	return err
}

// PeerID is our peer ID.
func (n *Node) PeerID() string { return n.api.PeerID().String() }

// DisplayName is our display name; "" until it is set.
func (n *Node) DisplayName() string { return n.api.DisplayName() }

// SetDisplayName sets our display name (at most 64 characters).
func (n *Node) SetDisplayName(name string) error {
	return friendly(n.api.SetDisplayName(name))
}

// MyCard is our contact card text.
func (n *Node) MyCard() (string, error) {
	card, err := n.api.MyCard()
	return card, friendly(err)
}

// CardQR renders card as a QR code: a PNG of size × size pixels.
func CardQR(card string, size int) ([]byte, error) {
	png, err := identity.CardQRPNG(card, size)
	return png, friendly(err)
}

// AddContact sends a contact request to the peer on a card.
func (n *Node) AddContact(card string) error {
	return friendly(n.api.AddContact(context.Background(), card))
}

// Accept accepts peerID's contact request.
func (n *Node) Accept(peerID string) error {
	id, err := parsePeer(peerID)
	if err != nil {
		return err
	}
	return friendly(n.api.AcceptContact(context.Background(), id))
}

// Reject rejects peerID's contact request; they are not told.
func (n *Node) Reject(peerID string) error {
	id, err := parsePeer(peerID)
	if err != nil {
		return err
	}
	return friendly(n.api.RejectContact(context.Background(), id))
}

// Rename sets a contact's nickname; "" goes back to the name they chose.
func (n *Node) Rename(peerID, nickname string) error {
	id, err := parsePeer(peerID)
	if err != nil {
		return err
	}
	return friendly(n.api.RenameContact(context.Background(), id, nickname))
}

// contactJSON is a contact as Contacts lists it.
type contactJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`  // nickname, else their display name, else a short peer ID
	State     string `json:"state"` // pending_outgoing, pending_incoming or accepted
	Connected bool   `json:"connected"`
	Relayed   bool   `json:"relayed"`
	Unread    int    `json:"unread"`
}

// Contacts lists the contacts, rejected ones left out, as a JSON array of
// contactJSON.
func (n *Node) Contacts() (string, error) {
	vs, err := n.api.Contacts(context.Background())
	if err != nil {
		return "", friendly(err)
	}
	out := []contactJSON{}
	for _, c := range vs {
		if c.State == store.StateBlocked {
			continue
		}
		out = append(out, contactJSON{
			ID: c.PeerID.String(), Name: c.Name(), State: string(c.State),
			Connected: c.Connected, Relayed: c.Relayed, Unread: c.Unread,
		})
	}
	return marshal(out), nil
}

// messageJSON is one message.
type messageJSON struct {
	ID     string `json:"id"` // hex; with out, identifies the message
	Peer   string `json:"peer"`
	Out    bool   `json:"out"`
	Text   string `json:"text"`
	At     int64  `json:"at"`     // Unix milliseconds: when sent (out) or received (in)
	Status string `json:"status"` // pending or delivered (out), received (in)
}

func toMessageJSON(m store.Message) messageJSON {
	at := m.ReceivedAt
	if m.Direction == store.DirOut {
		at = m.SentAt
	}
	return messageJSON{
		ID: hex.EncodeToString(m.ID), Peer: m.PeerID.String(), Out: m.Direction == store.DirOut,
		Text: m.Text, At: at.UnixMilli(), Status: string(m.Status),
	}
}

// History is the conversation with peerID, oldest first, as a JSON array
// of messageJSON.
func (n *Node) History(peerID string) (string, error) {
	id, err := parsePeer(peerID)
	if err != nil {
		return "", err
	}
	msgs, err := n.api.History(context.Background(), id)
	if err != nil {
		return "", friendly(err)
	}
	out := make([]messageJSON, len(msgs))
	for i, m := range msgs {
		out[i] = toMessageJSON(m)
	}
	return marshal(out), nil
}

// Send sends text to peerID and returns the new message as messageJSON. It
// is delivered now or, if they are offline, when you both next are online.
func (n *Node) Send(peerID, text string) (string, error) {
	id, err := parsePeer(peerID)
	if err != nil {
		return "", err
	}
	m, err := n.api.Send(context.Background(), id, text)
	if err != nil {
		return "", friendly(err)
	}
	return marshal(toMessageJSON(m)), nil
}

// MarkRead marks the conversation with peerID read.
func (n *Node) MarkRead(peerID string) error {
	id, err := parsePeer(peerID)
	if err != nil {
		return err
	}
	return friendly(n.api.MarkRead(context.Background(), id))
}

// Reachability says whether others can dial us: "public", "behind NAT" or
// "checking…".
func (n *Node) Reachability() string { return diagtext.Reachability(n.api.Reachability()) }

// fieldJSON is one labelled line of connection details.
type fieldJSON struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Details describes the connection to peerID as a JSON array of fieldJSON,
// the same lines the desktop app's Details window shows.
func (n *Node) Details(peerID string) (string, error) {
	id, err := parsePeer(peerID)
	if err != nil {
		return "", err
	}
	d, err := n.api.ContactDetails(context.Background(), id)
	if err != nil {
		return "", friendly(err)
	}
	out := []fieldJSON{}
	for _, f := range diagtext.Details(d, time.Now()) {
		out = append(out, fieldJSON{f.Label, f.Value})
	}
	return marshal(out), nil
}

// Ping measures the round trip to a connected peerID, in milliseconds. It
// also updates the latency Details shows.
func (n *Node) Ping(peerID string) (int64, error) {
	id, err := parsePeer(peerID)
	if err != nil {
		return 0, err
	}
	rtt, err := n.api.Ping(context.Background(), id)
	return rtt.Milliseconds(), friendly(err)
}

// Check is a running connection check.
type Check struct{ cancel context.CancelFunc }

// Cancel stops the check; l.OnDone is still called.
func (c *Check) Cancel() { c.cancel() }

// CheckConnection checks, step by step, whether and how peerID (shown as
// name in the report) can be reached, reporting to l. It returns at once.
func (n *Node) CheckConnection(peerID, name string, l CheckListener) (*Check, error) {
	id, err := parsePeer(peerID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		res, err := n.api.CheckConnection(ctx, id, func(i int, s p2p.Step) {
			l.OnStep(i, s.Title, s.Detail, stepStatus(s.Status))
		})
		if err != nil {
			l.OnDone(errtext.FriendlyError(err), "")
			return
		}
		l.OnDone(res.Verdict, diagtext.Report(name, time.Now(), res))
	}()
	return &Check{cancel: cancel}, nil
}

func stepStatus(s p2p.StepStatus) string {
	switch s {
	case p2p.StepOK:
		return "ok"
	case p2p.StepInfo:
		return "info"
	case p2p.StepWarn:
		return "warn"
	case p2p.StepFail:
		return "fail"
	case p2p.StepSkipped:
		return "skipped"
	}
	return "running"
}

// eventJSON renders an event from core.API.Subscribe; ok is false for
// events the app does not use.
func eventJSON(ev any) (js string, ok bool) {
	switch e := ev.(type) {
	case event.MessageReceived:
		return marshal(map[string]any{"type": "message", "message": toMessageJSON(e.Msg)}), true
	case event.MessageDelivered:
		return marshal(map[string]any{"type": "delivered", "peer": e.Peer.String(), "id": hex.EncodeToString(e.ID)}), true
	case event.ContactChanged, event.Connectivity:
		return marshal(map[string]any{"type": "contacts"}), true
	case event.Reachability:
		return marshal(map[string]any{"type": "reachability", "text": diagtext.Reachability(e.Status)}), true
	}
	return "", false
}

// marshal encodes v, which is always one of this file's JSON shapes.
func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only plain strings, numbers and bools: cannot fail
	}
	return string(b)
}

// errBadPeer is returned for a peer ID that does not parse; the app only
// passes IDs it got from this package, so it means a bug.
var errBadPeer = errors.New("invalid peer ID")

func parsePeer(s string) (peer.ID, error) {
	id, err := peer.Decode(s)
	if err != nil {
		return "", errBadPeer
	}
	return id, nil
}

// friendly replaces err with its user-facing text, which gomobile hands to
// Kotlin as the exception message.
func friendly(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(errtext.FriendlyError(err))
}

// splitList splits a comma-separated list, dropping blank entries.
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
