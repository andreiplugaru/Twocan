package svc

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// Guard ties str to ctx: it sets str's deadline to ctx's (if any) and resets
// str when ctx ends before stop is called. The reset is what actually
// enforces the timeout, since some transports (mocknet among them) ignore
// deadlines; it also unblocks I/O when a service closes.
func Guard(ctx context.Context, str network.Stream) (stop func()) {
	if dl, ok := ctx.Deadline(); ok {
		_ = str.SetDeadline(dl)
	}
	stopReset := context.AfterFunc(ctx, func() { str.Reset() })
	return func() { stopReset() }
}

// Timeout returns a context that ends after d or with parent, and guards str
// with it (see Guard). cancel releases both without resetting str; call it
// when the guarded I/O is done.
func Timeout(parent context.Context, str network.Stream, d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	ctx, cancelCtx := context.WithTimeout(parent, d)
	stop := Guard(ctx, str)
	return ctx, func() {
		stop() // first, so cancelling ctx does not reset str
		cancelCtx()
	}
}
