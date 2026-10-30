// Package core wires Twocan together: identity, store, the libp2p node and
// the contacts and chat services, plus the background loops that keep
// contacts reachable and outboxes flushed. The CLI and the UI use it only
// through API.
package core

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	libp2pevent "github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"twocan/internal/addrshare"
	"twocan/internal/chat"
	"twocan/internal/contacts"
	"twocan/internal/event"
	"twocan/internal/identity"
	"twocan/internal/logging"
	"twocan/internal/p2p"
	"twocan/internal/store"
	"twocan/internal/svc"
)

const (
	keyFile = "identity.key"
	dbFile  = "twocan.db"
	// protectTag marks contact connections the conn manager must not trim.
	protectTag = "twocan-contact"
	// maxNameRunes caps our display name and contact nicknames.
	maxNameRunes = 64
	// libp2pEventBuf is the buffer of our libp2p event subscription, so a
	// busy loop does not block libp2p's emitters.
	libp2pEventBuf = 256
)

// ErrNameTooLong is returned for a display name or nickname over 64 characters.
var ErrNameTooLong = fmt.Errorf("name is longer than %d characters", maxNameRunes)

// Options configures Start.
type Options struct {
	DataDir      string
	Bootstrap    []string // nil: p2p.DefaultBootstrap
	ListenAddrs  []string // nil: p2p.DefaultListenAddrs
	EnableMDNS   bool
	ForcePrivate bool
	// InterfaceIPs, if set, lists this machine's IPs where libp2p cannot
	// (Android): see p2p.Config.InterfaceIPs.
	InterfaceIPs func() ([]net.IP, error)
	// Log receives the node's events (nil: discard). Never message text or keys.
	Log *slog.Logger
}

// DefaultDataDir returns os.UserConfigDir()/twocan.
func DefaultDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "twocan"), nil
}

// ContactView is a contact plus its live connection state and unread count.
type ContactView struct {
	store.Contact
	Connected, Relayed bool
	Unread             int
}

// API is everything the CLI and the UI may do with a running node.
type API interface {
	PeerID() peer.ID
	DisplayName() string
	SetDisplayName(name string) error
	MyCard() (string, error)
	AddContact(ctx context.Context, cardText string) error
	AddContactFromImage(ctx context.Context, img image.Image) error
	AcceptContact(ctx context.Context, id peer.ID) error
	RejectContact(ctx context.Context, id peer.ID) error
	RenameContact(ctx context.Context, id peer.ID, nickname string) error
	Contacts(ctx context.Context) ([]ContactView, error)
	Send(ctx context.Context, to peer.ID, text string) (store.Message, error)
	Reply(ctx context.Context, to peer.ID, replyTo []byte, text string) (store.Message, error)
	Edit(ctx context.Context, id peer.ID, msgID []byte, text string) error
	Delete(ctx context.Context, id peer.ID, msgID []byte, forEveryone bool) error
	React(ctx context.Context, id peer.ID, msgID []byte, emoji string) error
	Typing(id peer.ID)
	Search(ctx context.Context, query string) ([]store.Message, error)
	History(ctx context.Context, id peer.ID) ([]store.Message, error)
	MarkRead(ctx context.Context, id peer.ID) error
	Subscribe() (<-chan any, func())
	Reachability() network.Reachability
	ContactDetails(ctx context.Context, id peer.ID) (ContactDetails, error)
	Ping(ctx context.Context, id peer.ID) (time.Duration, error)
	CheckConnection(ctx context.Context, id peer.ID, report func(int, p2p.Step)) (p2p.CheckResult, error)
	NetworkChanged()
	Stop() error
}

var _ API = (*Node)(nil)

// Node is a running Twocan node.
type Node struct {
	h        host.Host
	st       *store.Store
	r        p2p.Reacher
	diag     diagnoser // r, if it can run checks (nil under startWith in tests)
	connInfo func(peer.ID) (connected, relayed bool)
	log      *slog.Logger
	bus      *event.Bus
	contacts *contacts.Service
	chat     *chat.Service
	addrs    *addrshare.Service
	dialer   directDialer // r, if it can dial without the relay (nil under startWith in tests)
	net      netWatcher   // r, if it can check connections after a network change (likewise)

	// Set by Start only (nil under startWith): the libp2p node and the
	// cancel func of its lifetime context.
	pn         *p2p.Node
	cancelNode context.CancelFunc

	iv        intervals
	loops     *svc.Tracker
	reach     atomic.Int32  // network.Reachability
	netChange chan struct{} // NetworkChanged's signal to watchNetwork

	mu        sync.Mutex // guards peers, health, nextSweep, leaving and rejoining
	peers     map[peer.ID]*peerState
	health    map[peer.ID]*health
	nextSweep time.Time               // when the presence sweep runs next
	leaving   map[peer.ID]*leaveState // contacts we try to move off the relay
	rejoining map[peer.ID]time.Time   // contacts being reconnected: when the next try is

	saveMu sync.Mutex // serialises saveAddrs

	stopOnce sync.Once
	stopErr  error
}

// Start opens (creating if needed) the data dir, identity key and store,
// starts the libp2p node and the services, and starts the background loops.
func Start(ctx context.Context, opts Options) (*Node, error) {
	if opts.DataDir == "" {
		return nil, errors.New("core: no data dir")
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("core: create data dir: %w", err)
	}
	key, err := identity.LoadOrCreateKey(filepath.Join(opts.DataDir, keyFile))
	if err != nil {
		return nil, err
	}
	listen := opts.ListenAddrs
	if listen == nil {
		listen = p2p.DefaultListenAddrs
	}
	bootAddrs := opts.Bootstrap
	if bootAddrs == nil {
		bootAddrs = p2p.DefaultBootstrap
	}
	boot, err := p2p.ParseBootstrap(bootAddrs)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(filepath.Join(opts.DataDir, dbFile))
	if err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = logging.Discard()
	}
	// The libp2p node's background work lives until Stop, not until the
	// caller's (possibly short-lived) ctx ends.
	nodeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	newNode := func(listen []string) (*p2p.Node, error) {
		return p2p.NewNode(nodeCtx, p2p.Config{
			PrivKey:      key,
			ListenAddrs:  listen,
			Bootstrap:    boot,
			EnableMDNS:   opts.EnableMDNS,
			ForcePrivate: opts.ForcePrivate,
			InterfaceIPs: opts.InterfaceIPs,
			Log:          opts.Log,
		})
	}
	// Ask for last run's ports (see listen.go).
	tried := withSavedPorts(listen, savedListenAddrs(ctx, st))
	pn, err := newNode(tried)
	if err != nil && !slices.Equal(tried, listen) {
		log.Info("saved listen ports taken; using new ones", "err", err)
		tried = listen
		pn, err = newNode(listen)
	}
	if err != nil {
		cancel()
		st.Close()
		return nil, err
	}
	relistenTaken(pn.Host(), listen, tried, log)
	saveListenAddrs(ctx, st, pn.Host(), listen, log)
	n, err := startWith(ctx, pn.Host(), st, pn, pn.ConnInfo, intervals{}, opts.Log)
	if err != nil {
		pn.Close()
		cancel()
		st.Close()
		return nil, err
	}
	n.pn, n.cancelNode = pn, cancel
	return n, nil
}

// startWith wires a node around an existing host, store and reacher (tests
// pass mocknet hosts) and starts the services and loops. It seeds the
// peerstore with each contact's stored addrs and protects contact
// connections. Zero intervals take their defaults; a nil log discards. On
// success the Node owns st (Stop closes it); h stays the caller's.
func startWith(ctx context.Context, h host.Host, st *store.Store, r p2p.Reacher,
	connInfo func(peer.ID) (bool, bool), iv intervals, log *slog.Logger) (*Node, error) {
	if log == nil {
		log = logging.Discard()
	}
	if iv.tick == 0 {
		iv.tick = defaultTick
	}
	if iv.presence == 0 {
		iv.presence = defaultPresence
	}
	if iv.direct == 0 {
		iv.direct = defaultDirect
	}
	if iv.reconnect == 0 {
		iv.reconnect = defaultReconnect
	}
	if iv.settle == 0 {
		iv.settle = defaultSettle
	}
	cs, err := st.ListContacts(ctx)
	if err != nil {
		return nil, fmt.Errorf("core: list contacts: %w", err)
	}
	nodeLog := log.With("component", "core")
	nodeLog.Info("starting", "peer", h.ID(), "contacts", len(cs))
	for _, c := range cs {
		nodeLog.Info("contact at startup", "peer", c.PeerID, "name", c.Name(), "state", c.State,
			"owes_contact_frame", c.PendingSend, "saved_addrs", addrStrings(c.Addrs))
		if c.State == store.StateBlocked {
			continue
		}
		if len(c.Addrs) > 0 {
			h.Peerstore().AddAddrs(c.PeerID, c.Addrs, peerstore.AddressTTL)
		}
		h.ConnManager().Protect(c.PeerID, protectTag)
	}
	sub, err := h.EventBus().Subscribe([]any{
		new(libp2pevent.EvtPeerConnectednessChanged),
		new(libp2pevent.EvtLocalReachabilityChanged),
	}, eventbus.BufSize(libp2pEventBuf))
	if err != nil {
		return nil, fmt.Errorf("core: subscribe libp2p events: %w", err)
	}

	n := &Node{
		h:         h,
		st:        st,
		r:         r,
		connInfo:  connInfo,
		log:       nodeLog,
		bus:       event.NewBus(),
		iv:        iv,
		loops:     svc.NewTracker(),
		peers:     make(map[peer.ID]*peerState),
		health:    make(map[peer.ID]*health),
		leaving:   make(map[peer.ID]*leaveState),
		rejoining: make(map[peer.ID]time.Time),
		netChange: make(chan struct{}, 1),
	}
	n.diag, _ = r.(diagnoser)
	n.dialer, _ = r.(directDialer)
	n.net, _ = r.(netWatcher)
	n.reach.Store(int32(network.ReachabilityUnknown))
	n.contacts = contacts.New(h, st, r, n.bus, n.DisplayName)
	n.chat = chat.New(h, st, r, n.bus)
	n.addrs = addrshare.New(h, st, n.contactAddrsArrived)
	if src, ok := r.(addrSource); ok {
		n.addrs.SetOwnAddrs(src.ShareableAddrs)
	}
	n.contacts.SetLogger(log)
	n.chat.SetLogger(log)
	n.addrs.SetLogger(log)
	// Subscribe before the services start so no ContactChanged is missed.
	busEvents, cancelBus := n.bus.Subscribe()
	n.contacts.Start()
	n.chat.Start()
	n.addrs.Start()
	n.loops.Go(func(ctx context.Context) {
		defer sub.Close()
		defer cancelBus()
		n.run(ctx, sub.Out(), busEvents)
	})
	n.loops.Go(n.watchNetwork)
	return n, nil
}

// Stop shuts down, in order, the loops, the services, the libp2p node and
// the store. It is safe to call more than once.
func (n *Node) Stop() error {
	n.stopOnce.Do(func() {
		n.loops.Close()
		n.contacts.Close()
		n.chat.Close()
		n.addrs.Close()
		var errs []error
		if n.pn != nil {
			errs = append(errs, n.pn.Close())
			n.cancelNode()
		}
		errs = append(errs, n.st.Close())
		n.stopErr = errors.Join(errs...)
		n.log.Info("stopped", "err", n.stopErr)
	})
	return n.stopErr
}

// PeerID returns our peer ID.
func (n *Node) PeerID() peer.ID { return n.h.ID() }

// DisplayName returns our stored display name ("" if unset or unreadable).
func (n *Node) DisplayName() string {
	v, _, _ := n.st.GetSetting(context.Background(), store.SettingDisplayName)
	return v
}

// SetDisplayName stores our display name, trimmed; at most 64 characters.
func (n *Node) SetDisplayName(name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	return n.st.SetSetting(context.Background(), store.SettingDisplayName, name)
}

// cleanName trims a user-entered name and enforces maxNameRunes.
func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) {
		return "", errors.New("name is not valid UTF-8")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", ErrNameTooLong
	}
	return name, nil
}

// MyCard returns our contact card string (see cardAddrs for the addrs).
func (n *Node) MyCard() (string, error) {
	return identity.EncodeCard(identity.Card{
		PeerID:      n.h.ID(),
		DisplayName: n.DisplayName(),
		Addrs:       cardAddrs(n.h.Addrs()),
	})
}

// cardAddrs picks at most identity.MaxCardAddrs addrs for our card: public
// direct addrs, then relay (/p2p-circuit) addrs, then private ones. Loopback
// addrs are used only when there is nothing else; link-local ones never.
func cardAddrs(all []ma.Multiaddr) []ma.Multiaddr {
	var public, circuit, private, loopback []ma.Multiaddr
	for _, a := range all {
		switch {
		case isCircuit(a):
			circuit = append(circuit, a)
		case manet.IsIPLoopback(a):
			loopback = append(loopback, a)
		case manet.IsIP6LinkLocal(a):
		case manet.IsPublicAddr(a):
			public = append(public, a)
		default:
			private = append(private, a)
		}
	}
	var out []ma.Multiaddr
	out = append(out, public...)
	out = append(out, circuit...)
	out = append(out, private...)
	if len(out) == 0 {
		out = loopback
	}
	return out[:min(len(out), identity.MaxCardAddrs)]
}

func isCircuit(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// AddContact adds the peer on a card. Card decoding errors (identity.ErrNotACard
// and friends) are returned unchanged.
func (n *Node) AddContact(ctx context.Context, cardText string) error {
	card, err := identity.DecodeCard(cardText)
	if err != nil {
		return err
	}
	return n.contacts.Add(ctx, card)
}

// AddContactFromImage adds the peer on the card in a QR code somewhere in
// img. It returns identity.ErrNoQRCode if there is no readable code, and
// the same errors as AddContact otherwise.
func (n *Node) AddContactFromImage(ctx context.Context, img image.Image) error {
	card, err := identity.DecodeCardQR(img)
	if err != nil {
		return err
	}
	return n.contacts.Add(ctx, card)
}

// AcceptContact accepts a pending incoming request.
func (n *Node) AcceptContact(ctx context.Context, id peer.ID) error {
	return n.contacts.Accept(ctx, id)
}

// RejectContact blocks the peer.
func (n *Node) RejectContact(ctx context.Context, id peer.ID) error {
	return n.contacts.Reject(ctx, id)
}

// RenameContact sets a contact's local nickname (trimmed; "" clears it).
func (n *Node) RenameContact(ctx context.Context, id peer.ID, nickname string) error {
	nickname, err := cleanName(nickname)
	if err != nil {
		return err
	}
	if err := n.st.SetNickname(ctx, id, nickname); err != nil {
		return err
	}
	n.bus.Publish(event.ContactChanged{Peer: id})
	return nil
}

// Contacts lists all contacts (blocked included) with live state.
func (n *Node) Contacts(ctx context.Context) ([]ContactView, error) {
	cs, err := n.st.ListContacts(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ContactView, 0, len(cs))
	for _, c := range cs {
		unread, err := n.st.UnreadCount(ctx, c.PeerID)
		if err != nil {
			return nil, err
		}
		connected, relayed := n.connInfo(c.PeerID)
		views = append(views, ContactView{Contact: c, Connected: connected, Relayed: relayed, Unread: unread})
	}
	return views, nil
}

// Send queues text for an accepted contact and starts delivering it.
func (n *Node) Send(ctx context.Context, to peer.ID, text string) (store.Message, error) {
	return n.chat.Send(ctx, to, text, nil)
}

// Reply is Send, answering message replyTo of the same conversation.
func (n *Node) Reply(ctx context.Context, to peer.ID, replyTo []byte, text string) (store.Message, error) {
	return n.chat.Send(ctx, to, text, replyTo)
}

// Edit replaces the text of one of our messages, on both sides.
func (n *Node) Edit(ctx context.Context, id peer.ID, msgID []byte, text string) error {
	return n.chat.Edit(ctx, id, msgID, text)
}

// Delete deletes a message: for everyone (our own only) or from this
// device only.
func (n *Node) Delete(ctx context.Context, id peer.ID, msgID []byte, forEveryone bool) error {
	return n.chat.Delete(ctx, id, msgID, forEveryone)
}

// React sets our reaction (an emoji; "" removes it) to a message.
func (n *Node) React(ctx context.Context, id peer.ID, msgID []byte, emoji string) error {
	return n.chat.React(ctx, id, msgID, emoji)
}

// Typing tells a connected contact we are typing (throttled; best effort).
func (n *Node) Typing(id peer.ID) { n.chat.Typing(id) }

// Search finds messages in every conversation containing all the words of
// query, newest first.
func (n *Node) Search(ctx context.Context, query string) ([]store.Message, error) {
	return n.st.Search(ctx, query)
}

// History returns the conversation with id, oldest first.
func (n *Node) History(ctx context.Context, id peer.ID) ([]store.Message, error) {
	return n.st.History(ctx, id)
}

// MarkRead marks every message from id as read and sends a read receipt.
func (n *Node) MarkRead(ctx context.Context, id peer.ID) error {
	return n.chat.MarkRead(ctx, id)
}

// Subscribe returns a channel of event.* values and its cancel func.
func (n *Node) Subscribe() (<-chan any, func()) { return n.bus.Subscribe() }

// Reachability returns our latest NAT reachability (Unknown until AutoNAT reports).
func (n *Node) Reachability() network.Reachability {
	return network.Reachability(n.reach.Load())
}
