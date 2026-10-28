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

// createKey generates a key and saves it at path, so that path either does
// not exist or holds a whole key, even if the write fails or the machine
// crashes part way. The key is written to a temp file in the same directory
// (os.CreateTemp makes it 0600), synced to disk, and then moved to path by
// saveNew, which like O_EXCL fails if path already exists: a key another
// process created in the meantime is never replaced (a plain rename(2)
// would replace it silently). The temp file is always removed; path keeps
// the key.
func createKey(path string) (crypto.PrivKey, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create key file: %w", err)
	}
	defer os.Remove(f.Name())
	if err := writeKey(f, b); err != nil {
		f.Close()
		return nil, fmt.Errorf("write key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("write key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	if err := saveNew(f.Name(), path); err != nil {
		return nil, fmt.Errorf("save key file: %w", err)
	}
	syncDir(dir)
	return priv, nil
}

// syncDir flushes dir's entries to disk so the new key's name survives a
// crash. It is best effort: the key is already in place and valid.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// hardLink is os.Link. Tests replace it to simulate Android, where apps may
// not create hard links.
var hardLink = os.Link

// writeKey writes the new key's bytes to f. Tests replace it to simulate a
// failed write.
var writeKey = func(f *os.File, b []byte) error {
	_, err := f.Write(b)
	return err
}
