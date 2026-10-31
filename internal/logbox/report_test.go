package logbox

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "twocan/internal/proto"
)

func gzipped(t *testing.T, b []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return &buf
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	received := time.Date(2026, 10, 31, 12, 0, 5, 0, time.UTC)
	info := &pb.ReportInfo{
		Code: "K7F3-9QX2", ReceivedMs: received.UnixMilli(), PeerId: "12D3KooWX",
		Details: &pb.LogDetails{Name: "Andrei's Phone/../x", App: "android", Note: "it broke",
			DeviceTimeMs: received.Add(-3 * time.Second).UnixMilli()},
	}
	logPath, txtPath, err := WriteReport(dir, info, gzipped(t, []byte("line\n")), 1<<20)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "K7F3-9QX2-AndreisPhonex.log"), logPath)
	require.Equal(t, filepath.Join(dir, "K7F3-9QX2.txt"), txtPath)
	b, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, "line\n", string(b))
	txt, err := os.ReadFile(txtPath)
	require.NoError(t, err)
	require.Contains(t, string(txt), "clock difference: 3s")
	require.Contains(t, string(txt), "it broke")
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, es, 2, "nothing outside the two files")
}

func TestWriteReportNameless(t *testing.T) {
	logPath, _, err := WriteReport(t.TempDir(), &pb.ReportInfo{Code: "K7F3-9QX2", Details: &pb.LogDetails{Name: "../.."}}, gzipped(t, nil), 10)
	require.NoError(t, err)
	require.Equal(t, "K7F3-9QX2-device.log", filepath.Base(logPath))
}

func TestWriteReportDecompressionCap(t *testing.T) {
	dir := t.TempDir()
	logPath, _, err := WriteReport(dir, &pb.ReportInfo{Code: "K7F3-9QX2"}, gzipped(t, make([]byte, 1<<20)), 10)
	require.Error(t, err)
	fi, statErr := os.Stat(filepath.Join(dir, "K7F3-9QX2-device.log"))
	require.NoError(t, statErr)
	require.LessOrEqual(t, fi.Size(), int64(10))
	_ = logPath
}
