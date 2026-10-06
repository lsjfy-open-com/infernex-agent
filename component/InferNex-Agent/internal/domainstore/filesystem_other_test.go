//go:build !linux

package domainstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProductionStoreFailsClosedOutsideLinux(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "state")
	if _, err := Init(root, "tenant-a", os.Geteuid()); !IsCode(err, CodeUnsupported) {
		t.Fatalf("non-Linux production store did not fail closed: %v", err)
	}
}
