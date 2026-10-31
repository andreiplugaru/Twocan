package logbox

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"

	"twocan/internal/logtest"
	pb "twocan/internal/proto"
	"twocan/internal/wire"
)

func newHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	require.NoError(t, err)
	t.Cleanup(func() { h.Close() })
	return h
}

func connect(t *testing.T, a, b host.Host) {
	t.Helper()
	require.NoError(t, a.Connect(t.Context(), peer.AddrInfo{ID: b.ID(), Addrs: b.Addrs()}))
}

// lab is a helper serving a store, a device and a reader, all connected.
type lab struct {
	helper, device, reader host.Host
	st                     *Store
	dir                    string
	logs                   *logtest.Buffer
}

func newLab(t *testing.T, lim Limits, cfg ServerConfig) *lab {
	t.Helper()
	l := &lab{helper: newHost(t), device: newHost(t), reader: newHost(t)}
	l.dir = filepath.Join(t.TempDir(), "logs")
	var err error
	l.st, err = OpenStore(l.dir, lim)
	require.NoError(t, err)
	log, logs := logtest.New()
	l.logs = logs
	cfg.Readers = []peer.ID{l.reader.ID()}
	cfg.Log = log
	stop := Serve(l.helper, l.st, cfg)
	t.Cleanup(stop)
	connect(t, l.device, l.helper)
	connect(t, l.reader, l.helper)
	return l
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func refusal(t *testing.T, err error) pb.Refusal {
	t.Helper()
	var re *RefusedError
	require.True(t, errors.As(err, &re), "not refused: %v", err)
	return re.Reason
}

func leftover(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestSendAndReadBack(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	data := randomBytes(t, 200<<10)
	code, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{Name: "Phone", Note: "hi"}, data)
	require.NoError(t, err)
	require.Regexp(t, codeRe, code)

	rs, err := List(t.Context(), l.reader, l.helper.ID())
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Equal(t, code, rs[0].Code)
	require.Equal(t, l.device.ID().String(), rs[0].PeerId)
	require.Equal(t, "hi", rs[0].Details.GetNote())

	var got bytes.Buffer
	info, err := Get(t.Context(), l.reader, l.helper.ID(), code, &got)
	require.NoError(t, err)
	require.Equal(t, code, info.Code)
	require.Equal(t, data, got.Bytes())
}

func TestUploadRefusedWhenOff(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	require.NoError(t, l.st.SetOff(true))
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("x"))
	require.Equal(t, pb.Refusal_REFUSAL_OFF, refusal(t, err))
	require.ElementsMatch(t, []string{"OFF"}, leftover(t, l.dir))
}

func TestSetOffFromReader(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	require.NoError(t, SetOff(t.Context(), l.reader, l.helper.ID(), true))
	require.True(t, l.st.Off())
	require.NoError(t, SetOff(t.Context(), l.reader, l.helper.ID(), false))
	require.False(t, l.st.Off())
	require.Error(t, SetOff(t.Context(), l.device, l.helper.ID(), true))
	require.False(t, l.st.Off(), "only readers flip the switch")
}

func TestReadRejectsStrangers(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	_, err := List(t.Context(), l.device, l.helper.ID())
	require.Error(t, err)
	require.NotContains(t, l.logs.String(), l.device.ID().String())
}

func TestTooLargeRefused(t *testing.T) {
	l := newLab(t, Limits{MaxUpload: 1000, MaxTotal: 1 << 20, MaxAge: time.Hour}, ServerConfig{})
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, make([]byte, 2000))
	require.Equal(t, pb.Refusal_REFUSAL_TOO_LARGE, refusal(t, err))
	require.Empty(t, leftover(t, l.dir))
}

func TestRateLimited(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{PerPeer: 1})
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("a"))
	require.NoError(t, err)
	_, err = Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("b"))
	require.Equal(t, pb.Refusal_REFUSAL_RATE_LIMITED, refusal(t, err))
}

// startUpload opens an upload, sends the start frame and reads the go-ahead.
func startUpload(t *testing.T, l *lab) (*bufio.Reader, interface {
	Write([]byte) (int, error)
	Reset() error
}) {
	t.Helper()
	s, err := l.device.NewStream(t.Context(), l.helper.ID(), UploadProtocol)
	require.NoError(t, err)
	require.NoError(t, wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_Start{Start: &pb.UploadStart{Details: &pb.LogDetails{}, Size: 100}}}))
	r := bufio.NewReader(s)
	var reply pb.UploadReply
	require.NoError(t, wire.ReadFrame(r, &reply))
	require.Equal(t, pb.Refusal_REFUSAL_UNSPECIFIED, reply.Refusal)
	return r, s
}

func noUploadFiles(dir string) bool {
	es, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(es) == 0
}

func TestAbortedUploadLeavesNothing(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	for range maxConcurrent {
		_, s := startUpload(t, l)
		require.NoError(t, wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_Chunk{Chunk: []byte("half")}}))
		require.NoError(t, s.Reset())
	}
	require.Eventually(t, func() bool { return noUploadFiles(l.dir) }, 2*time.Second, 20*time.Millisecond)
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("x"))
	require.NoError(t, err, "the aborted uploads freed their slots")
}

func TestUnwritableStoreRefusesAsFull(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	l := newLab(t, DefaultLimits(), ServerConfig{})
	require.NoError(t, os.Chmod(l.dir, 0o500))
	t.Cleanup(func() { os.Chmod(l.dir, 0o700) })
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("x"))
	require.Equal(t, pb.Refusal_REFUSAL_FULL, refusal(t, err))
	require.NoError(t, os.Chmod(l.dir, 0o700))
	_, err = List(t.Context(), l.reader, l.helper.ID())
	require.NoError(t, err, "the helper still serves")
}

func TestStalledUploadTimesOut(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{Idle: 200 * time.Millisecond})
	r, _ := startUpload(t, l)
	done := make(chan error, 1)
	go func() {
		var reply pb.UploadReply
		done <- wire.ReadFrame(r, &reply)
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("the helper kept a stalled upload open")
	}
	require.Eventually(t, func() bool { return noUploadFiles(l.dir) }, time.Second, 20*time.Millisecond)
	require.False(t, strings.Contains(l.logs.String(), l.device.ID().String()))
}
