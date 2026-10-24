// Package wire implements Twocan's frame codec: varint length-delimited
// protobuf messages with a hard size cap.
package wire

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
)

// MaxFrameSize is the largest allowed frame payload in bytes.
const MaxFrameSize = 64 << 10

// ErrFrameTooLarge is returned when a frame exceeds MaxFrameSize.
var ErrFrameTooLarge = errors.New("frame too large")

// WriteFrame writes m to w as a varint length-delimited frame.
func WriteFrame(w io.Writer, m proto.Message) error {
	if proto.Size(m) > MaxFrameSize {
		return ErrFrameTooLarge
	}
	_, err := protodelim.MarshalTo(w, m)
	return err
}

// ReadFrame reads one length-delimited frame from r into m. It returns
// io.EOF at a clean end of stream and an error wrapping ErrFrameTooLarge
// for oversize frames.
func ReadFrame(r *bufio.Reader, m proto.Message) error {
	err := protodelim.UnmarshalOptions{MaxSize: MaxFrameSize}.UnmarshalFrom(r, m)
	var tooLarge *protodelim.SizeTooLargeError
	if errors.As(err, &tooLarge) {
		return fmt.Errorf("%w: %w", ErrFrameTooLarge, err)
	}
	return err
}
