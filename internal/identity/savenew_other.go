//go:build !linux

package identity

// saveNew links tmp to path, failing if path exists (see createKey).
func saveNew(tmp, path string) error { return hardLink(tmp, path) }
