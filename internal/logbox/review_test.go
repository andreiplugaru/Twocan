package logbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "twocan/internal/proto"
	"twocan/internal/wire"
)

// Streams that never send their start frame hold no upload slot.
func TestSilentStreamsDoNotBlockUploads(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	for range maxConcurrent + 1 {
		s, err := l.device.NewStream(t.Context(), l.helper.ID(), UploadProtocol)
		require.NoError(t, err)
		_, err = s.Write([]byte{0x05}) // part of a frame, then silence
		require.NoError(t, err)
		t.Cleanup(func() { s.Reset() })
	}
	time.Sleep(100 * time.Millisecond)
	_, err := Send(t.Context(), l.device, l.helper.ID(), &pb.LogDetails{}, []byte("x"))
	require.NoError(t, err)
}

// An upload that trickles in is cut off after MaxDuration, however often it
// sends something.
func TestTrickleUploadIsCutOff(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{Idle: 300 * time.Millisecond, MaxDuration: 600 * time.Millisecond})
	_, s := startUpload(t, l)
	start := time.Now()
	for time.Since(start) < 3*time.Second {
		if wire.WriteFrame(s, &pb.UploadFrame{Body: &pb.UploadFrame_Chunk{Chunk: []byte{1}}}) != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Less(t, time.Since(start), 2*time.Second, "the helper cut the trickle off")
	require.Eventually(t, func() bool { return noUploadFiles(l.dir) }, time.Second, 20*time.Millisecond)
}

// Details are capped, so a report's .json stays small whatever is sent.
func TestDetailsAreCapped(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	// As much as one start frame (64 KiB) carries.
	long := strings.Repeat("x", 30000)
	_, err := Send(t.Context(), l.device, l.helper.ID(),
		&pb.LogDetails{Name: long[:10000], App: long[:4000], Version: long[:4000], Platform: long[:4000], Note: long}, nil)
	require.NoError(t, err)
	rs, err := l.st.List()
	require.NoError(t, err)
	d := rs[0].Details
	require.LessOrEqual(t, len([]rune(d.Name)), 64)
	require.LessOrEqual(t, len([]rune(d.App)), 64)
	require.LessOrEqual(t, len([]rune(d.Version)), 64)
	require.LessOrEqual(t, len([]rune(d.Platform)), 64)
	require.Equal(t, 500, len([]rune(d.Note)))
}

// The storage cap counts everything a report takes on disk.
func TestTotalCountsDetails(t *testing.T) {
	s, dir := newTestStore(t, Limits{MaxUpload: 100, MaxTotal: 2000, MaxAge: time.Hour})
	for i := range 20 {
		put(t, s, "", time.Now().Add(time.Duration(i)*time.Second), &pb.LogDetails{Note: strings.Repeat("n", 300)})
	}
	var total int64
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range es {
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		total += fi.Size()
	}
	require.LessOrEqual(t, total, int64(2000))
}

// IPv6 senders are counted per /64, the block a home or phone usually has.
func TestIPKeyGroupsIPv6By64(t *testing.T) {
	require.Equal(t, "1.2.3.4", ipKey(net.ParseIP("1.2.3.4")))
	require.Equal(t, ipKey(net.ParseIP("2001:db8:1:2::1")), ipKey(net.ParseIP("2001:db8:1:2:ffff::9")))
	require.NotEqual(t, ipKey(net.ParseIP("2001:db8:1:2::1")), ipKey(net.ParseIP("2001:db8:1:3::1")))
}

// The list works however many reports there are.
func TestListManyReports(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	for i := range 200 { // well over one 64 KiB frame
		u, err := l.st.Begin()
		require.NoError(t, err)
		_, err = u.Commit(&pb.ReportInfo{Details: &pb.LogDetails{Note: strings.Repeat("x", 500)}}, time.Now().Add(time.Duration(i)*time.Millisecond))
		require.NoError(t, err)
	}
	rs, err := List(t.Context(), l.reader, l.helper.ID())
	require.NoError(t, err)
	require.Len(t, rs, 200)
}

// When the helper refuses partway through (its disk filled up), the
// device hears so at once instead of waiting for its deadline.
func TestRefusedMidUploadReturnsQuickly(t *testing.T) {
	l := newLab(t, DefaultLimits(), ServerConfig{})
	writeChunk = func(*Upload, []byte) (int, error) { return 0, errors.New("no space left on device") }
	t.Cleanup(func() { writeChunk = (*Upload).Write })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Send(ctx, l.device, l.helper.ID(), &pb.LogDetails{}, make([]byte, 8<<20-1))
	require.Equal(t, pb.Refusal_REFUSAL_FULL, refusal(t, err))
	require.Less(t, time.Since(start), 5*time.Second)
}
