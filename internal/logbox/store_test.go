package logbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	pb "twocan/internal/proto"
)

func newTestStore(t *testing.T, lim Limits) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	s, err := OpenStore(dir, lim)
	require.NoError(t, err)
	return s, dir
}

// put stores data as a report received at now and returns its code.
func put(t *testing.T, s *Store, data string, now time.Time, d *pb.LogDetails) string {
	t.Helper()
	u, err := s.Begin()
	require.NoError(t, err)
	_, err = u.Write([]byte(data))
	require.NoError(t, err)
	code, err := u.Commit(&pb.ReportInfo{Details: d}, now)
	require.NoError(t, err)
	return code
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		c := newCode()
		require.Regexp(t, codeRe, c)
		require.False(t, seen[c], "duplicate code %s", c)
		seen[c] = true
	}
}

func TestCommitAndList(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	code := put(t, s, "abc", now, &pb.LogDetails{Name: "Phone"})
	require.Regexp(t, codeRe, code)

	rs, err := s.List()
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Equal(t, code, rs[0].Code)
	require.EqualValues(t, 3, rs[0].Size)
	require.Equal(t, now.UnixMilli(), rs[0].ReceivedMs)
	require.Equal(t, "Phone", rs[0].Details.GetName())

	r, info, err := s.Open(code)
	require.NoError(t, err)
	defer r.Close()
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "abc", string(b))
	require.Equal(t, code, info.Code)

	stem := "20261031T120000Z-" + code
	require.ElementsMatch(t, []string{stem + ".log.gz", stem + ".json"}, names(t, dir))
}

func TestTooLargeUpload(t *testing.T) {
	s, dir := newTestStore(t, Limits{MaxUpload: 10, MaxTotal: 100, MaxAge: time.Hour})
	u, err := s.Begin()
	require.NoError(t, err)
	_, err = u.Write([]byte("0123456789X"))
	require.ErrorIs(t, err, ErrTooLarge)
	u.Abort()
	require.Empty(t, names(t, dir))
}

func TestEvictsOldestWhenFull(t *testing.T) {
	// Each report takes its 300 bytes plus about 100 of details: room for two.
	s, _ := newTestStore(t, Limits{MaxUpload: 1000, MaxTotal: 1000, MaxAge: time.Hour})
	now := time.Now()
	put(t, s, strings.Repeat("a", 300), now, nil)
	b := put(t, s, strings.Repeat("b", 300), now.Add(time.Second), nil)
	c := put(t, s, strings.Repeat("c", 300), now.Add(2*time.Second), nil)
	rs, err := s.List()
	require.NoError(t, err)
	require.Len(t, rs, 2)
	require.Equal(t, b, rs[0].Code)
	require.Equal(t, c, rs[1].Code)
}

func TestPrunesAfterMaxAge(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	now := time.Now()
	put(t, s, "x", now, nil)
	require.NoError(t, s.Prune(now.Add(7*24*time.Hour-time.Minute)))
	require.Len(t, names(t, dir), 2)
	require.NoError(t, s.Prune(now.Add(7*24*time.Hour+time.Minute)))
	require.Empty(t, names(t, dir))
}

func TestPruneRemovesLeftoverTemps(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".upload-x"), []byte("half"), 0o600))
	require.NoError(t, s.Prune(time.Now()))
	require.Len(t, names(t, dir), 1, "an upload that may still be running is kept")
	require.NoError(t, s.Prune(time.Now().Add(time.Hour)))
	require.Empty(t, names(t, dir))
}

func TestOffSwitch(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	require.False(t, s.Off())
	require.NoError(t, s.SetOff(true))
	require.FileExists(t, filepath.Join(dir, "OFF"))
	require.True(t, s.Off())
	require.NoError(t, os.Remove(filepath.Join(dir, "OFF")))
	require.False(t, s.Off(), "removing the file by hand turns receiving back on")
	require.NoError(t, s.SetOff(false))
	require.False(t, s.Off())
}

func TestSameSecondUploadsDoNotCollide(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	now := time.Now()
	a := put(t, s, "a", now, nil)
	b := put(t, s, "b", now, nil)
	require.NotEqual(t, a, b)
	require.Len(t, names(t, dir), 4)
}

func TestNamesNeverBecomePaths(t *testing.T) {
	s, dir := newTestStore(t, DefaultLimits())
	code := put(t, s, "x", time.Now(), &pb.LogDetails{Name: "../../etc/passwd"})
	for _, n := range names(t, dir) {
		require.True(t, strings.Contains(n, code) && !strings.Contains(n, "passwd"), n)
	}
	parent := filepath.Dir(dir)
	require.ElementsMatch(t, []string{"logs"}, names(t, parent))
}
