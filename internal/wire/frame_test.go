package wire

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pb "twocan/internal/proto"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := &pb.ChatFrame{Body: &pb.ChatFrame_Message{Message: &pb.ChatMessage{Id: make([]byte, 16), SentAt: 1700000000000, Text: "héllo"}}}
	require.NoError(t, WriteFrame(&buf, in))
	require.NoError(t, WriteFrame(&buf, &pb.ChatFrame{Body: &pb.ChatFrame_Ack{Ack: &pb.Ack{Id: []byte{1}}}}))
	r := bufio.NewReader(&buf)
	var out pb.ChatFrame
	require.NoError(t, ReadFrame(r, &out))
	require.True(t, proto.Equal(in, &out))
	require.NoError(t, ReadFrame(r, &out))
	require.Equal(t, []byte{1}, out.GetAck().GetId())
	require.ErrorIs(t, ReadFrame(r, &out), io.EOF)
}

func TestWriteFrameTooLarge(t *testing.T) {
	big := &pb.ChatFrame{Body: &pb.ChatFrame_Message{Message: &pb.ChatMessage{Text: strings.Repeat("x", MaxFrameSize)}}}
	require.ErrorIs(t, WriteFrame(io.Discard, big), ErrFrameTooLarge)
}

func TestReadFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(protowire.AppendVarint(nil, MaxFrameSize+1))
	buf.Write(make([]byte, MaxFrameSize+1))
	var out pb.ChatFrame
	require.ErrorIs(t, ReadFrame(bufio.NewReader(&buf), &out), ErrFrameTooLarge)
}

func FuzzReadFrame(f *testing.F) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, &pb.ChatFrame{Body: &pb.ChatFrame_Message{Message: &pb.ChatMessage{Id: []byte{1, 2}, SentAt: 5, Text: "hi"}}})
	f.Add(buf.Bytes())
	buf.Reset()
	_ = WriteFrame(&buf, &pb.ContactFrame{Body: &pb.ContactFrame_Request{Request: &pb.ContactRequest{DisplayName: "bob"}}})
	f.Add(buf.Bytes())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var chat pb.ChatFrame
		_ = ReadFrame(bufio.NewReader(bytes.NewReader(data)), &chat)
		var contact pb.ContactFrame
		_ = ReadFrame(bufio.NewReader(bytes.NewReader(data)), &contact)
	})
}
