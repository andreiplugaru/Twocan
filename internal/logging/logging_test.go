package logging

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	golog "github.com/ipfs/go-log/v2"
	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError,
	} {
		got, err := ParseLevel(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := ParseLevel("verbose")
	require.ErrorContains(t, err, "debug, info, warn or error")
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	require.NoError(t, err)
	return string(b)
}

func TestOpenWritesLevelFilteredFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, slog.LevelInfo)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, FileName), l.Path)
	l.Info("hello", "peer", "12D3KooWabc")
	l.Debug("hidden-debug")
	require.NoError(t, l.Close())

	out := readLog(t, dir)
	require.Contains(t, out, "msg=hello")
	require.Contains(t, out, "peer=12D3KooWabc")
	require.NotContains(t, out, "hidden-debug")
	st, err := os.Stat(l.Path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestOpenCapturesStdlibAndLibp2pLogs(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, slog.LevelInfo)
	require.NoError(t, err)
	lib := golog.Logger("twocan-logging-test")
	log.Print("from-stdlib")
	lib.Warn("libp2p-warning")
	lib.Info("libp2p-info")
	require.NoError(t, l.Close())

	out := readLog(t, dir)
	require.Contains(t, out, "from-stdlib")
	require.Contains(t, out, "libp2p-warning")
	require.NotContains(t, out, "libp2p-info", "libp2p info is only logged at debug")
}

func TestDebugLevelIncludesLibp2pInfo(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, slog.LevelDebug)
	require.NoError(t, err)
	golog.Logger("twocan-logging-test").Info("libp2p-info")
	require.NoError(t, l.Close())
	require.Contains(t, readLog(t, dir), "libp2p-info")
}

func TestCloseIsIdempotent(t *testing.T) {
	l, err := Open(t.TempDir(), slog.LevelInfo)
	require.NoError(t, err)
	require.NoError(t, l.Close())
	require.NoError(t, l.Close())
}

func TestRecentTakesNewestBytes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "twocan-2026-10-30T10-00-00.000.log"), []byte("older\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "twocan-2026-10-31T10-00-00.000.log"), []byte("old\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName), []byte("new\n"), 0o600))
	b, err := Recent(dir, 100)
	require.NoError(t, err)
	require.Equal(t, "old\nnew\n", string(b), "the newest rotated file, then the current one")
	b, err = Recent(dir, 4)
	require.NoError(t, err)
	require.Equal(t, "new\n", string(b))
}

func TestRecentWithNoLogFile(t *testing.T) {
	b, err := Recent(t.TempDir(), 100)
	require.NoError(t, err)
	require.Empty(t, b)
}
