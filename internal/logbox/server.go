package logbox

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
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
)

// ServerConfig configures Serve. Zero limits take their defaults.
type ServerConfig struct {
	Readers []peer.ID     // peers allowed on ReadProtocol
	PerIP   int           // uploads an hour per sender IP (default 6)
	PerPeer int           // uploads an hour per peer ID (default 3)
	Idle    time.Duration // the longest wait for a frame (default 30 s)
	Log     *slog.Logger  // nil: discard. Never given peer IDs or addresses.
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

// next reads the next frame, waiting at most Idle for it.
func (sv *server) next(s network.Stream, r *bufio.Reader, m *pb.UploadFrame) error {
	_ = s.SetReadDeadline(time.Now().Add(sv.cfg.Idle))
	return wire.ReadFrame(r, m)
}

func (sv *server) upload(s network.Stream) {
	if !sv.lim.acquire() {
		sv.refuse(s, pb.Refusal_REFUSAL_RATE_LIMITED)
		return
	}
	defer sv.lim.release()
	r := bufio.NewReader(s)
	var f pb.UploadFrame
	if err := sv.next(s, r, &f); err != nil || f.GetStart() == nil {
		s.Reset()
		return
	}
	start := f.GetStart()
	ip := ""
	if a, err := manet.ToIP(s.Conn().RemoteMultiaddr()); err == nil {
		ip = a.String()
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
	}
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
		if err := sv.next(s, r, &f); err != nil {
			u.Abort()
			s.Reset()
			return
		}
		if f.GetEnd() != nil {
			break
		}
		if _, err := u.Write(f.GetChunk()); err != nil {
			u.Abort()
			if errors.Is(err, ErrTooLarge) {
				sv.refuse(s, pb.Refusal_REFUSAL_TOO_LARGE)
			} else {
				sv.refuse(s, pb.Refusal_REFUSAL_FULL)
			}
			return
		}
	}
	info := &pb.ReportInfo{PeerId: s.Conn().RemotePeer().String(), Details: start.Details}
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
		_ = wire.WriteFrame(s, &pb.ReadFrame{Body: &pb.ReadFrame_List{List: &pb.ReportList{Reports: rs}}})
		_ = s.Close()
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
