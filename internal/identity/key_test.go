package identity

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateKeyStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "identity.key")
	k1, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	k2, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	require.True(t, k1.Equals(k2))
	require.Equal(t, crypto.Ed25519, int(k1.Type()))
	st, err := os.Stat(p)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	dst, err := os.Stat(filepath.Dir(p))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dst.Mode().Perm())
}

func TestLoadOrCreateKeyRejectsLoosePerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "identity.key")
	_, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(p, 0o644))
	_, err = LoadOrCreateKey(p)
	require.Error(t, err)
}

func TestLoadOrCreateKeyFailedWriteLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "identity.key")
	orig := writeKey
	t.Cleanup(func() { writeKey = orig })
	writeKey = func(f *os.File, b []byte) error {
		_, err := f.Write(b[:len(b)/2])
		require.NoError(t, err)
		return errors.New("disk full")
	}

	_, err := LoadOrCreateKey(p)
	require.ErrorContains(t, err, "disk full")
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, ents, "no key file and no temp file left behind")

	writeKey = orig
	k1, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	k2, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	require.True(t, k1.Equals(k2))
}

func TestLoadOrCreateKeyNeverClobbers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "identity.key")
	require.NoError(t, os.WriteFile(p, []byte("existing"), 0o600))
	_, err := createKey(p)
	require.ErrorIs(t, err, os.ErrExist)
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, "existing", string(got))
	ents, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	require.Len(t, ents, 1, "temp file removed")
}

// Android forbids apps to create hard links; the key must still be saved.
func TestCreateKeyWithoutHardLinks(t *testing.T) {
	orig := hardLink
	t.Cleanup(func() { hardLink = orig })
	hardLink = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: syscall.EACCES}
	}
	p := filepath.Join(t.TempDir(), "identity.key")
	k1, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	k2, err := LoadOrCreateKey(p)
	require.NoError(t, err)
	require.True(t, k1.Equals(k2))
	ents, err := os.ReadDir(filepath.Dir(p))
	require.NoError(t, err)
	require.Len(t, ents, 1, "temp file removed")
}
