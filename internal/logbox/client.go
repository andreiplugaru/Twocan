package logbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	pb "twocan/internal/proto"
	"twocan/internal/wire"
)

// defaultTimeout bounds a call whose ctx has no deadline.
const defaultTimeout = 2 * time.Minute

// RefusedError is the helper refusing a report.
type RefusedError struct{ Reason pb.Refusal }

func (e *RefusedError) Error() string {
	switch e.Reason {
	case pb.Refusal_REFUSAL_OFF:
		return "the helper isn't accepting logs"
	case pb.Refusal_REFUSAL_TOO_LARGE:
		return "the log is too large for the helper"
	case pb.Refusal_REFUSAL_RATE_LIMITED:
		return "too many logs were sent recently; try again later"
	case pb.Refusal_REFUSAL_FULL:
		return "the helper has no room for logs right now"
	}
	return "the helper refused the log"
}

// open opens a stream to helper on proto, with a deadline from ctx.
func open(ctx context.Context, h host.Host, helper peer.ID, proto string) (network.Stream, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}
	s, err := h.NewStream(ctx, helper, protocol.ID(proto))
	if err != nil {
		return nil, err
	}
	dl, _ := ctx.Deadline()
	_ = s.SetDeadline(dl)
	return s, nil
}

// Send sends a report (details and the gzipped log) to helper and returns
// its code, or a *RefusedError.
func Send(ctx context.Context, h host.Host, helper peer.ID, d *pb.LogDetails, gz []byte) (string, error) {
	s, err := open(ctx, h, helper, UploadProtocol)
	if err != nil {
		return "", err
	}
	defer s.Close()
	r := bufio.NewReader(s)
	reply := func() (*pb.UploadReply, error) {
		var rep pb.UploadReply
		if err := wire.ReadFrame(r, &rep); err != nil {
			s.Reset()
			return nil, err
		}
		if rep.Refusal != pb.Refusal_REFUSAL_UNSPECIFIED {
			return nil, &RefusedError{Reason: rep.Refusal}
		}
		return &rep, nil
	}
	start := &pb.UploadStart{Details: d, Size: uint64(len(gz))}
	if err := wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_Start{Start: start}}); err != nil {
		s.Reset()
		return "", err
	}
	if _, err := reply(); err != nil {
		return "", err
	}
	for len(gz) > 0 {
		n := min(len(gz), chunkSize)
		if err := wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_Chunk{Chunk: gz[:n]}}); err != nil {
			s.Reset()
			return "", err
		}
		gz = gz[n:]
	}
	if err := wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_End{End: &pb.UploadEnd{}}}); err != nil {
		s.Reset()
		return "", err
	}
	rep, err := reply()
	if err != nil {
		return "", err
	}
	return rep.Code, nil
}

// request sends one ReadRequest and returns a reader of the answer.
func request(ctx context.Context, h host.Host, helper peer.ID, req *pb.ReadRequest) (network.Stream, *bufio.Reader, error) {
	s, err := open(ctx, h, helper, ReadProtocol)
	if err != nil {
		return nil, nil, err
	}
	if err := wire.WriteFrame(s, req); err != nil {
		s.Reset()
		return nil, nil, err
	}
	return s, bufio.NewReader(s), nil
}

// doneErr turns a Done frame into an error.
func doneErr(d *pb.Done) error {
	if d.GetError() != "" {
		return errors.New(d.GetError())
	}
	return nil
}

// List returns the reports on helper, oldest first.
func List(ctx context.Context, h host.Host, helper peer.ID) ([]*pb.ReportInfo, error) {
	s, r, err := request(ctx, h, helper, &pb.ReadRequest{Op: &pb.ReadRequest_List{List: &pb.ListReports{}}})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var f pb.ReadFrame
	if err := wire.ReadFrame(r, &f); err != nil {
		return nil, err
	}
	if d := f.GetDone(); d != nil {
		return nil, doneErr(d)
	}
	return f.GetList().GetReports(), nil
}

// Get writes report code's log (gzipped, as sent) to w and returns its details.
func Get(ctx context.Context, h host.Host, helper peer.ID, code string, w io.Writer) (*pb.ReportInfo, error) {
	s, r, err := request(ctx, h, helper, &pb.ReadRequest{Op: &pb.ReadRequest_Get{Get: code}})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var info *pb.ReportInfo
	for {
		var f pb.ReadFrame
		if err := wire.ReadFrame(r, &f); err != nil {
			return nil, err
		}
		switch b := f.Body.(type) {
		case *pb.ReadFrame_Info:
			info = b.Info
		case *pb.ReadFrame_Chunk:
			if _, err := w.Write(b.Chunk); err != nil {
				return nil, err
			}
		case *pb.ReadFrame_Done:
			if err := doneErr(b.Done); err != nil {
				return nil, err
			}
			if info == nil {
				return nil, fmt.Errorf("helper sent no details for %s", code)
			}
			return info, nil
		}
	}
}

// simple sends a request answered by Done alone.
func simple(ctx context.Context, h host.Host, helper peer.ID, req *pb.ReadRequest) error {
	s, r, err := request(ctx, h, helper, req)
	if err != nil {
		return err
	}
	defer s.Close()
	var f pb.ReadFrame
	if err := wire.ReadFrame(r, &f); err != nil {
		return err
	}
	return doneErr(f.GetDone())
}

// Delete deletes report code on helper.
func Delete(ctx context.Context, h host.Host, helper peer.ID, code string) error {
	return simple(ctx, h, helper, &pb.ReadRequest{Op: &pb.ReadRequest_Delete{Delete: code}})
}

// SetOff switches receiving logs on helper off or on.
func SetOff(ctx context.Context, h host.Host, helper peer.ID, off bool) error {
	return simple(ctx, h, helper, &pb.ReadRequest{Op: &pb.ReadRequest_SetOff{SetOff: off}})
}
