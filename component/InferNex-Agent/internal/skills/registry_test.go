package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryLoadsAndReadsSkill(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "hixl-diagnosis")
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: hixl-diagnosis\ndescription: Diagnose HiXL\n---\n# Workflow\nInspect evidence."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "errors.md"), []byte("# Errors\nTIMEOUT"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 1 || got[0].Name != "hixl-diagnosis" {
		t.Fatalf("unexpected skills: %#v", got)
	}
	content, err := registry.Read("hixl-diagnosis")
	if err != nil || content.Instructions == "" {
		t.Fatalf("read: %#v %v", content, err)
	}
	reference, err := registry.ReadReference("hixl-diagnosis", "errors.md")
	if err != nil || reference.Content != "# Errors\nTIMEOUT" {
		t.Fatalf("reference: %#v %v", reference, err)
	}
}

func TestRegistryRejectsTraversalAndDuplicateNames(t *testing.T) {
	root := t.TempDir()
	for _, parent := range []string{"a", "b"} {
		directory := filepath.Join(root, parent, "same")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: same\ndescription: duplicate\n---\nbody"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewRegistry([]string{filepath.Join(root, "a"), filepath.Join(root, "b")}); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	directory := filepath.Join(root, "single", "safe")
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: safe\ndescription: safe\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry([]string{filepath.Join(root, "single")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ReadReference("safe", "../SKILL.md"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
