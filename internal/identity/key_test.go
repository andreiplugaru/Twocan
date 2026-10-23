package identity

import (
	"os"
	"path/filepath"
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
