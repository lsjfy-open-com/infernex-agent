/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 */

package configversion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRecordRealCommitAndOfflineVerify(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
	}
	run("init")
	run("config", "user.email", "test@example.test")
	run("config", "user.name", "Test")
	if e := os.WriteFile(filepath.Join(repo, "config.yaml"), []byte("version: committed\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "config.yaml")
	run("commit", "-m", "test")
	if e := os.WriteFile(filepath.Join(repo, "config.yaml"), []byte("version: dirty\n"), 0600); e != nil {
		t.Fatal(e)
	}
	scheme := runtime.NewScheme()
	if e := infernexv1alpha1.AddToScheme(scheme); e != nil {
		t.Fatal(e)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	snap, e := changesafety.Capture(context.Background(), client, []string{"models"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	before := filepath.Join(t.TempDir(), "before.json")
	after := filepath.Join(t.TempDir(), "after.json")
	if e = changesafety.WriteSnapshot(before, snap); e != nil {
		t.Fatal(e)
	}
	if e = changesafety.WriteSnapshot(after, snap); e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(t.TempDir(), "versions")
	r, e := RecordVersion(Input{StateDir: state, Repo: repo, Ref: "HEAD", File: "config.yaml", Before: before, After: after, Name: "release-one"})
	if e != nil {
		t.Fatal(e)
	}
	if r.Name != "release-one" || !strings.HasPrefix(r.ID, "release-one-") {
		t.Fatalf("bad name/id: %+v", r)
	}
	b, e := os.ReadFile(filepath.Join(state, r.ID, "config.bin"))
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "version: committed\n" {
		t.Fatalf("worktree content used: %q", b)
	}
	if _, e = Verify(state, r.ID); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(repo, repo+"-offline"); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(state, r.ID); e != nil {
		t.Fatalf("offline verification failed: %v", e)
	}
	for _, name := range []string{"before.json", "after.json", "record.json"} {
		p := filepath.Join(state, r.ID, name)
		original, er := os.ReadFile(p)
		if er != nil {
			t.Fatal(er)
		}
		if er = os.WriteFile(p, []byte("corrupt"), 0600); er != nil {
			t.Fatal(er)
		}
		if _, er = Verify(state, r.ID); er == nil {
			t.Fatalf("accepted damaged %s", name)
		}
		if er = os.WriteFile(p, original, 0600); er != nil {
			t.Fatal(er)
		}
	}
	if _, e = Verify(state, r.ID); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(state, "other-20260920T010203Z-001122334455")
	if e = os.Symlink(filepath.Join(state, r.ID), link); e != nil {
		t.Fatal(e)
	}
	if _, e = Show(state, filepath.Base(link)); e == nil {
		t.Fatal("accepted symlinked version directory")
	}
	if e = os.WriteFile(filepath.Join(state, r.ID, "config.bin"), []byte("corrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(state, r.ID); e == nil {
		t.Fatal("accepted corrupted configuration")
	}
}
func TestRejectUnsafePathsAndRefs(t *testing.T) {
	for _, p := range []string{"../secret", "/etc/passwd", "-option", "a//b", "a/../b"} {
		if validPath(p) {
			t.Errorf("accepted %q", p)
		}
	}
	for _, r := range []string{"-x", "HEAD:evil", "HEAD^{tree}", "main..other"} {
		if checkRef(r) {
			t.Errorf("accepted ref %q", r)
		}
	}
}

func TestGitReplaceCannotChangeCommittedBytes(t *testing.T) {
	repo := t.TempDir()
	command := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	command("init")
	command("config", "user.email", "test@example.test")
	command("config", "user.name", "Test")
	file := filepath.Join(repo, "config.yaml")
	if e := os.WriteFile(file, []byte("version: A\n"), 0600); e != nil {
		t.Fatal(e)
	}
	command("add", "config.yaml")
	command("commit", "-m", "A")
	a := command("rev-parse", "HEAD")
	if e := os.WriteFile(file, []byte("version: B\n"), 0600); e != nil {
		t.Fatal(e)
	}
	command("commit", "-am", "B")
	b := command("rev-parse", "HEAD")
	command("replace", a, b)
	commit, _, contents, e := gitConfig(repo, a, "config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if commit != a || string(contents) != "version: A\n" {
		t.Fatalf("replacement used: commit=%s content=%q", commit, contents)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "other"))
	_, _, contents, e = gitConfig(repo, a, "config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if string(contents) != "version: A\n" {
		t.Fatal("GIT_DIR redirected object read")
	}
}

func TestPublishFailureLeavesNoVisibleVersion(t *testing.T) {
	state := filepath.Join(t.TempDir(), "versions")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	id := "test-20260920T010203Z-001122334455"
	if e := publish(state, id, map[string][]byte{"config.bin": []byte("ok"), "bad/path": []byte("fail")}, []byte("{}")); e == nil {
		t.Fatal("expected write failure")
	}
	if _, e := os.Lstat(filepath.Join(state, id)); !os.IsNotExist(e) {
		t.Fatalf("published incomplete version: %v", e)
	}
	entries, e := os.ReadDir(state)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary directory remained: %v", entries)
	}
	if e := os.Mkdir(filepath.Join(state, ".config-version-crashed"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(state, ".config-version-crashed", "record.json"), []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	records, e := List(state)
	if e != nil {
		t.Fatal(e)
	}
	if len(records) != 0 {
		t.Fatalf("listed unpublished record: %v", records)
	}
}
