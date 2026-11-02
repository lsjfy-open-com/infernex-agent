package namedfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadableNamesCollisionsAndIndex(t *testing.T) {
	root := t.TempDir()
	name := Name("../Mooncake prefix hit 超时/网络\n分析", time.Date(2026, 9, 10, 12, 30, 0, 0, time.UTC))
	if name != "mooncake-prefix-hit-超时-网络-分析_2026-09-10_12-30-00Z" {
		t.Fatal(name)
	}
	first, err := Create(root, name, ".md", "first", []byte("first report"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Create(root, name, ".md", "second", []byte("second report"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasSuffix(second, "-2.md") {
		t.Fatal(first, second)
	}
	for id, expected := range map[string]string{"first": first, "second": second} {
		found, err := Lookup(root, id)
		if err != nil || found != expected {
			t.Fatal(found, err)
		}
		info, err := os.Stat(found)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal(info, err)
		}
	}
	if _, err := Create(root, name, ".md", "first", []byte("overwrite")); err == nil {
		t.Fatal("duplicate ID overwrote index")
	}
	data, _ := os.ReadFile(first)
	if string(data) != "first report" {
		t.Fatal(string(data))
	}
}

func TestIndexRejectsEscapesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if _, err := Lookup(root, "../escape"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Mkdir(filepath.Join(root, ".index"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".index", "bad"), []byte("../escape\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(root, "bad"); err == nil {
		t.Fatal("accepted indexed traversal")
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := Index(root, "symlink", "link.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(root, "symlink"); err == nil {
		t.Fatal("accepted indexed symlink")
	}
}
