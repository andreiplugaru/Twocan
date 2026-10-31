package logbox

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	manet "github.com/multiformats/go-multiaddr/net"

	"twocan/internal/logging"
	pb "twocan/internal/proto"
	"twocan/internal/wire"
)

const (
	// UploadProtocol is devices sending a report to the helper.
	UploadProtocol = "/twocan/logs/1.0.0"
	// ReadProtocol is readers listing, fetching and deleting reports, and
	// switching receiving off and on.
	ReadProtocol = "/twocan/logs-read/1.0.0"
	// chunkSize is the most log bytes in one frame (frames are 64 KiB).
	chunkSize = 60 << 10
	// minRate (bytes/s) sets the default MaxDuration: an upload slower than
	// this is cut off, so a trickle cannot hold a slot for long.
	minRate = 16 << 10
	// The longest detail fields kept, in characters.
	maxDetail, maxNote = 64, 500
)

// ServerConfig configures Serve. Zero limits take their defaults.
type ServerConfig struct {
	Readers []peer.ID     // peers allowed on ReadProtocol
	PerIP   int           // uploads an hour per sender IP (default 6)
	PerPeer int           // uploads an hour per peer ID (default 3)
	Idle    time.Duration // the longest wait for a frame (default 30 s)
	// MaxDuration is the longest one upload may take (default: 2 Idle plus
	// MaxUpload at 16 KiB/s).
	MaxDuration time.Duration
	Log         *slog.Logger // nil: discard. Never given peer IDs or addresses.
}

type server struct {
	st      *Store
	cfg     ServerConfig
	lim     *limiter
	readers map[peer.ID]bool
}

// Serve offers UploadProtocol and ReadProtocol on h, storing in st, until
// stop is called.
func Serve(h host.Host, st *Store, cfg ServerConfig) (stop func()) {
	if cfg.PerIP == 0 {
		cfg.PerIP = 6
	}
	if cfg.PerPeer == 0 {
		cfg.PerPeer = 3
	}
	if cfg.Idle == 0 {
		cfg.Idle = 30 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = logging.Discard()
	}
	if cfg.MaxDuration == 0 {
		cfg.MaxDuration = 2*cfg.Idle + time.Duration(st.lim.MaxUpload/minRate)*time.Second
	}
	sv := &server{st: st, cfg: cfg, lim: newLimiter(cfg.PerIP, cfg.PerPeer, time.Hour), readers: map[peer.ID]bool{}}
	for _, id := range cfg.Readers {
		sv.readers[id] = true
	}
	h.SetStreamHandler(UploadProtocol, sv.upload)
	h.SetStreamHandler(ReadProtocol, sv.read)
	return func() {
		h.RemoveStreamHandler(UploadProtocol)
		h.RemoveStreamHandler(ReadProtocol)
	}
}

// refuse tells the device why and closes the stream.
func (sv *server) refuse(s network.Stream, why pb.Refusal) {
	sv.cfg.Log.Info("log report refused", "reason", why.String())
	_ = s.SetWriteDeadline(time.Now().Add(sv.cfg.Idle))
	_ = wire.WriteFrame(s, &pb.UploadReply{Refusal: why})
	_ = s.Close()
}

// next reads the next frame, waiting at most Idle for it and never past
// until (if set).
func (sv *server) next(s network.Stream, r *bufio.Reader, m *pb.UploadFrame, until time.Time) error {
	dl := time.Now().Add(sv.cfg.Idle)
	if !until.IsZero() && until.Before(dl) {
		dl = until
	}
	_ = s.SetReadDeadline(dl)
	return wire.ReadFrame(r, m)
}

func (sv *server) upload(s network.Stream) {
	// The start frame is read, and the sender counted, before a slot is
	// taken: a stream that never starts must not hold one.
	r := bufio.NewReader(s)
	var f pb.UploadFrame
	if err := sv.next(s, r, &f, time.Time{}); err != nil || f.GetStart() == nil {
		s.Reset()
		return
	}
	start := f.GetStart()
	ip := ""
	if a, err := manet.ToIP(s.Conn().RemoteMultiaddr()); err == nil {
		ip = ipKey(a)
	}
	switch {
	case sv.st.Off():
		sv.refuse(s, pb.Refusal_REFUSAL_OFF)
		return
	case !sv.lim.allow(ip, s.Conn().RemotePeer(), time.Now()):
		sv.refuse(s, pb.Refusal_REFUSAL_RATE_LIMITED)
		return
	case int64(start.Size) > sv.st.lim.MaxUpload:
		sv.refuse(s, pb.Refusal_REFUSAL_TOO_LARGE)
		return
	case !sv.lim.acquire():
		sv.refuse(s, pb.Refusal_REFUSAL_RATE_LIMITED)
		return
	}
	defer sv.lim.release()
	until := time.Now().Add(sv.cfg.MaxDuration)
	u, err := sv.st.Begin()
	if err != nil {
		sv.refuse(s, pb.Refusal_REFUSAL_FULL)
		return
	}
	if err := wire.WriteFrame(s, &pb.UploadReply{}); err != nil { // go ahead
		u.Abort()
		s.Reset()
		return
	}
	for {
		if err := sv.next(s, r, &f, until); err != nil {
			u.Abort()
			s.Reset()
			return
		}
		if f.GetEnd() != nil {
			break
		}
		if _, err := writeChunk(u, f.GetChunk()); err != nil {
			u.Abort()
			if errors.Is(err, ErrTooLarge) {
				sv.refuse(s, pb.Refusal_REFUSAL_TOO_LARGE)
			} else {
				sv.refuse(s, pb.Refusal_REFUSAL_FULL)
			}
			return
		}
	}
	info := &pb.ReportInfo{PeerId: s.Conn().RemotePeer().String(), Details: capDetails(start.Details)}
	code, err := u.Commit(info, time.Now())
	if err != nil {
		sv.refuse(s, pb.Refusal_REFUSAL_FULL)
		return
	}
	_ = s.SetWriteDeadline(time.Now().Add(sv.cfg.Idle))
	_ = wire.WriteFrame(s, &pb.UploadReply{Code: code})
	_ = s.Close()
	sv.cfg.Log.Info("log report received", "bytes", info.Size)
}

func (sv *server) read(s network.Stream) {
	if !sv.readers[s.Conn().RemotePeer()] {
		s.Reset()
		return
	}
	_ = s.SetDeadline(time.Now().Add(sv.cfg.Idle))
	var req pb.ReadRequest
	if err := wire.ReadFrame(bufio.NewReader(s), &req); err != nil {
		s.Reset()
		return
	}
	done := func(err error) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		_ = wire.WriteFrame(s, &pb.ReadFrame{Body: &pb.ReadFrame_Done{Done: &pb.Done{Error: msg}}})
		_ = s.Close()
	}
	switch op := req.Op.(type) {
	case *pb.ReadRequest_List:
		rs, err := sv.st.List()
		if err != nil {
			done(err)
			return
		}
		// One frame per report: all of them would not fit in one.
		for _, r := range rs {
			if err := wire.WriteFrame(s, &pb.ReadFrame{Body: &pb.ReadFrame_Info{Info: r}}); err != nil {
				s.Reset()
				return
			}
		}
		done(nil)
	case *pb.ReadRequest_Get:
		sv.get(s, op.Get, done)
	case *pb.ReadRequest_Delete:
		done(sv.st.Delete(op.Delete))
	case *pb.ReadRequest_SetOff:
		done(sv.st.SetOff(op.SetOff))
		sv.cfg.Log.Info("receiving logs switched", "off", op.SetOff)
	default:
		s.Reset()
	}
}

// get sends report code: its info, the log in chunks, then done.
func (sv *server) get(s network.Stream, code string, done func(error)) {
	rc, info, err := sv.st.Open(code)
	if err != nil {
		done(err)
		return
	}
	defer rc.Close()
	if err := wire.WriteFrame(s, &pb.ReadFrame{Body: &pb.ReadFrame_Info{Info: info}}); err != nil {
		s.Reset()
		return
	}
	buf := make([]byte, chunkSize)
	for {
		_ = s.SetWriteDeadline(time.Now().Add(sv.cfg.Idle))
		n, err := rc.Read(buf)
		if n > 0 {
			if werr := wire.WriteFrame(s, &pb.ReadFrame{Body: &pb.ReadFrame_Chunk{Chunk: buf[:n]}}); werr != nil {
				s.Reset()
				return
			}
		}
		if errors.Is(err, io.EOF) {
			done(nil)
			return
		}
		if err != nil {
			done(err)
			return
		}
	}
}

// writeChunk stores part of an upload; tests replace it to fail as a full
// disk would.
var writeChunk = (*Upload).Write

// ipKey is the key the per-IP limit counts ip under: an IPv4 address, or
// an IPv6 address's /64, which a home or a phone usually has whole.
func ipKey(ip net.IP) string {
	if ip.To4() != nil {
		return ip.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// capDetails returns d with its fields cut to maxDetail characters (the
// note to maxNote), so a report's details stay small on disk.
func capDetails(d *pb.LogDetails) *pb.LogDetails {
	cut := func(s string, n int) string {
		if r := []rune(s); len(r) > n {
			return string(r[:n])
		}
		return s
	}
	return &pb.LogDetails{
		Name:         cut(d.GetName(), maxDetail),
		App:          cut(d.GetApp(), maxDetail),
		Version:      cut(d.GetVersion(), maxDetail),
		Platform:     cut(d.GetPlatform(), maxDetail),
		DeviceTimeMs: d.GetDeviceTimeMs(),
		Note:         cut(d.GetNote(), maxNote),
	}
}
