// Package logtest captures slog output in tests.
package logtest

import (
	"bytes"
	"log/slog"
	"sync"
)

// Buffer collects log output; safe for concurrent writes from background
// goroutines while a test reads it.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns everything logged so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// New returns a debug-level text logger writing into a new Buffer.
func New() (*slog.Logger, *Buffer) {
	b := &Buffer{}
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})), b
}
