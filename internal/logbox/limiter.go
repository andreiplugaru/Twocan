package logbox

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// maxConcurrent is how many uploads the helper receives at once.
const maxConcurrent = 2

// limiter bounds uploads per sender IP and per peer ID over a sliding
// window, and how many run at once. Its counts live in memory only.
type limiter struct {
	perIP, perPeer int
	window         time.Duration
	slots          chan struct{}

	mu     sync.Mutex
	byIP   map[string][]time.Time
	byPeer map[peer.ID][]time.Time
}

func newLimiter(perIP, perPeer int, window time.Duration) *limiter {
	return &limiter{
		perIP: perIP, perPeer: perPeer, window: window,
		slots:  make(chan struct{}, maxConcurrent),
		byIP:   map[string][]time.Time{},
		byPeer: map[peer.ID][]time.Time{},
	}
}

// recent drops the times in ts older than the window.
func (l *limiter) recent(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > l.window {
		i++
	}
	return ts[i:]
}

// allow reports whether ip and id may upload now, and if so counts it.
func (l *limiter) allow(ip string, id peer.ID, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ips := l.recent(l.byIP[ip], now)
	ids := l.recent(l.byPeer[id], now)
	ok := len(ips) < l.perIP && len(ids) < l.perPeer
	if ok {
		ips, ids = append(ips, now), append(ids, now)
	}
	l.store(ip, ips, id, ids)
	return ok
}

func (l *limiter) store(ip string, ips []time.Time, id peer.ID, ids []time.Time) {
	if len(ips) == 0 {
		delete(l.byIP, ip)
	} else {
		l.byIP[ip] = ips
	}
	if len(ids) == 0 {
		delete(l.byPeer, id)
	} else {
		l.byPeer[id] = ids
	}
}

// acquire takes an upload slot if one is free.
func (l *limiter) acquire() bool {
	select {
	case l.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// release frees a slot taken by acquire.
func (l *limiter) release() { <-l.slots }
