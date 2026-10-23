// Package identity manages the node's Ed25519 key and shareable contact cards.
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// LoadOrCreateKey loads the Ed25519 identity key at path, creating it (and its
// parent directory) if missing. It refuses a key file readable by group/world.
func LoadOrCreateKey(path string) (crypto.PrivKey, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return createKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("stat identity key: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("identity key %s has permissions %o; must not be group/world accessible (chmod 600)", path, st.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read identity key: %w", err)
	}
	k, err := crypto.UnmarshalPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse identity key: %w", err)
	}
	return k, nil
}

func createKey(path string) (crypto.PrivKey, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	b, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create key file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, fmt.Errorf("write key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return priv, nil
}
