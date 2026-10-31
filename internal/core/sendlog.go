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

// ErrNoHelper is returned by SendLog when no helper is configured.
var ErrNoHelper = errors.New("no helper is configured")

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
		err := n.h.Connect(cctx, h)
		cancel()
		if err == nil {
			var code string
			if code, err = logbox.Send(ctx, n.h, h.ID, d, gz.Bytes()); err == nil {
				n.log.Info("log sent to the helper", "code", code, "bytes", gz.Len())
				return code, nil
			}
		}
		n.log.Info("could not send the log to a helper", "helper", h.ID, "err", err)
		lastErr = err
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
