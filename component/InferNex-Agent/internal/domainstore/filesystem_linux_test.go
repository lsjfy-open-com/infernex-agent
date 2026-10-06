//go:build linux

package domainstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// This is run only by Linux CI or an explicit Linux test invocation. Cross
// compilation on another host does not count as executing this integration.
func TestLinuxSecureFilesystemPublication(t *testing.T) {
	store, err := Init(filepath.Join(t.TempDir(), "state"), "tenant-a", os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), "tenant-a", env.Reference()); err != nil {
		t.Fatal(err)
	}
}
