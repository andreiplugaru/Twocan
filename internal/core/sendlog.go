package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	msmux "github.com/multiformats/go-multistream"

	"twocan/internal/logbox"
	"twocan/internal/logging"
	pb "twocan/internal/proto"
)

const (
	// maxLogBytes is how much of the log a report carries, before gzip.
	maxLogBytes = 16 << 20
	// maxNoteRunes caps the user's note.
	maxNoteRunes = 500
	// helperConnectTimeout bounds reaching one helper.
	helperConnectTimeout = 10 * time.Second
)

// SendLog's errors besides the helper's refusals (*logbox.RefusedError),
// in words for the user; the details go to the log.
var (
	ErrNoHelper           = errors.New("no helper is configured")
	ErrHelperUnreachable  = errors.New("could not reach the helper")
	ErrHelperNotReceiving = errors.New("the helper isn't accepting logs")
	ErrSendInterrupted    = errors.New("the connection to the helper broke while sending the log; try again")
)

// SendLog sends the end of this node's log, with note, to the first helper
// that accepts it, and returns the report's code. The log holds contact
// names and network addresses, never messages or keys.
func (n *Node) SendLog(ctx context.Context, note string) (string, error) {
	var helpers []peer.AddrInfo
	if n.pn != nil {
		helpers = n.pn.Helpers()
	}
	if len(helpers) == 0 {
		return "", ErrNoHelper
	}
	raw, err := logging.Recent(n.dataDir, maxLogBytes)
	if err != nil {
		return "", err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if r := []rune(note); len(r) > maxNoteRunes {
		note = string(r[:maxNoteRunes])
	}
	d := &pb.LogDetails{
		Name:         n.DisplayName(),
		App:          n.app,
		Version:      n.version,
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		DeviceTimeMs: time.Now().UnixMilli(),
		Note:         note,
	}
	var lastErr error
	for _, h := range helpers {
		cctx, cancel := context.WithTimeout(ctx, helperConnectTimeout)
		err := n.pn.ConnectHelper(cctx, h)
		cancel()
		if err != nil {
			n.log.Info("could not reach a helper to send the log", "helper", h.ID, "err", err)
			lastErr = ErrHelperUnreachable
			continue
		}
		code, err := logbox.Send(ctx, n.h, h.ID, d, gz.Bytes())
		if err == nil {
			n.log.Info("log sent to the helper", "code", code, "bytes", gz.Len())
			return code, nil
		}
		n.log.Info("could not send the log to a helper", "helper", h.ID, "err", err)
		var refused *logbox.RefusedError
		var unsupported msmux.ErrNotSupported[protocol.ID]
		switch {
		case errors.As(err, &refused):
			lastErr = err
		case errors.As(err, &unsupported):
			lastErr = ErrHelperNotReceiving
		default:
			lastErr = ErrSendInterrupted
		}
	}
	return "", lastErr
}

// buildVersion is the version a build reports when none is set: its git
// revision, or "dev".
func buildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
	}
	return "dev"
}
