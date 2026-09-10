/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package localfiles

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newFixture(t *testing.T) (*Workspace, Root) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "collected-logs")
	if err := os.MkdirAll(filepath.Join(root, "node-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "started\nGET /metrics 200\nERROR /health timeout token=secret-value\nfinished\n"
	if err := os.WriteFile(filepath.Join(root, "node-a", "vllm.log"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("operator note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := New([]string{root}, filepath.Join(t.TempDir(), "reports"))
	if err != nil {
		t.Fatal(err)
	}
	return workspace, workspace.Roots()[0]
}

func TestFindGrepAndReadEvidence(t *testing.T) {
	workspace, root := newFixture(t)
	found, err := workspace.Find(context.Background(), FindRequest{RootID: root.ID, Pattern: "*.log", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Entries) != 1 || found.Entries[0].Path != "node-a/vllm.log" {
		t.Fatalf("find=%#v", found)
	}
	grep, err := workspace.Grep(context.Background(), GrepRequest{RootID: root.ID, Pattern: "(?i)error|timeout", FileGlob: "*.log", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(grep.Matches) != 1 || grep.Matches[0].Line != 3 || strings.Contains(grep.Matches[0].Text, "secret-value") {
		t.Fatalf("grep=%#v", grep)
	}
	read, err := workspace.Read(ReadRequest{RootID: root.ID, Path: "node-a/vllm.log", StartLine: 3, MaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if read.SHA256 == "" || read.EndLine != 3 || strings.Contains(read.Content, "secret-value") {
		t.Fatalf("read=%#v", read)
	}
}

func TestNoiseFiltersAreVisibleAndPreserveAnomalies(t *testing.T) {
	workspace, root := newFixture(t)
	grep, err := workspace.Grep(context.Background(), GrepRequest{RootID: root.ID, Pattern: ".", FileGlob: "*.log", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if grep.FilteredLines != 1 || len(grep.Filters) != 2 {
		t.Fatalf("noise accounting=%#v", grep)
	}
	joined := ""
	for _, match := range grep.Matches {
		joined += match.Text + "\n"
	}
	if strings.Contains(joined, "GET /metrics") || !strings.Contains(joined, "ERROR /health timeout") {
		t.Fatalf("filtered output=%s", joined)
	}
	withNoise, err := workspace.Grep(context.Background(), GrepRequest{RootID: root.ID, Pattern: ".", FileGlob: "*.log", Recursive: true, IncludeNoise: true})
	if err != nil || withNoise.FilteredLines != 0 || len(withNoise.Matches) != 4 {
		t.Fatalf("include noise=%#v err=%v", withNoise, err)
	}
	excluded, err := workspace.Grep(context.Background(), GrepRequest{RootID: root.ID, Pattern: ".", FileGlob: "*.log", Recursive: true, IncludeNoise: true, ExcludePatterns: []string{"finished"}})
	if err != nil || excluded.FilteredLines != 1 {
		t.Fatalf("custom exclude=%#v err=%v", excluded, err)
	}
}

func TestConfiguredRootPreventsTraversalAndSymlinkEscape(t *testing.T) {
	workspace, root := newFixture(t)
	if _, err := workspace.Read(ReadRequest{RootID: root.ID, Path: "../outside.log"}); err == nil {
		t.Fatal("parent traversal was accepted")
	}
	if _, err := workspace.Read(ReadRequest{RootID: root.ID, Path: ".env"}); err == nil {
		t.Fatal("credential-like file was accepted")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root.Path, "escape.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Read(ReadRequest{RootID: root.ID, Path: "escape.log"}); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestCreateListAndReadMarkdownReport(t *testing.T) {
	workspace, root := newFixture(t)
	report, err := workspace.CreateReport(ReportRequest{
		Title: "HCCL incident", Summary: "Correlated historical logs",
		Markdown: "## Finding\n\nTimeout reproduced. password=do-not-store",
		Sources:  []Source{{RootID: root.ID, Path: "node-a/vllm.log"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(report.Path, ".md") || report.SHA256 == "" {
		t.Fatalf("report=%#v", report)
	}
	listed, err := workspace.ListReports()
	if err != nil || len(listed.Reports) != 1 || listed.Reports[0].ID != report.ID {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	read, err := workspace.ReadReport(report.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read.Content, "Evidence sources") || !strings.Contains(read.Content, "SHA-256") || strings.Contains(read.Content, "do-not-store") {
		t.Fatalf("report content=%s", read.Content)
	}
}

func TestReportNamesRestartLegacyAndIndexRecovery(t *testing.T) {
	workspace, _ := newFixture(t)
	first, err := workspace.CreateReport(ReportRequest{Title: "Mooncake prefix hit 超时", Markdown: "first finding"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.CreateReport(ReportRequest{Title: "Mooncake prefix hit 超时", Markdown: "second finding"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path || first.ID != first.SHA256 || !strings.HasPrefix(filepath.Base(first.Path), "mooncake-prefix-hit-超时_") {
		t.Fatal(first, second)
	}
	reopened, err := New(nil, workspace.reportRoot)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.ListReports()
	if err != nil || len(list.Reports) != 2 {
		t.Fatal(list, err)
	}
	for _, r := range list.Reports {
		if r.Title != "Mooncake prefix hit 超时" || r.Name == "" {
			t.Fatal(r)
		}
	}
	if err := os.Remove(filepath.Join(workspace.reportRoot, ".index", first.ID)); err != nil {
		t.Fatal(err)
	}
	read, err := reopened.ReadReport(first.ID)
	if err != nil || !strings.Contains(read.Content, "first finding") {
		t.Fatal(read, err)
	}
	if _, err := os.Stat(filepath.Join(workspace.reportRoot, ".index", first.ID)); err != nil {
		t.Fatal(err)
	}
	legacyID := "20260102T030405Z-abcdef123456"
	legacyPath := filepath.Join(workspace.reportRoot, legacyID+"-hccl-incident.md")
	if err := os.WriteFile(legacyPath, []byte("# HCCL incident\n\nGenerated by InferNex Agent at 2026-01-02T03:04:05Z.\n\nold report"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err = reopened.ReadReport(legacyID)
	if err != nil || !strings.Contains(read.Content, "old report") {
		t.Fatal(read, err)
	}
	list, err = reopened.ListReports()
	if err != nil || len(list.Reports) != 3 {
		t.Fatal(list, err)
	}
	found := false
	for _, r := range list.Reports {
		if r.ID == legacyID {
			found = true
			if r.Name != "hccl-incident_2026-01-02_03-04-05Z" || r.Title != "HCCL incident" {
				t.Fatal(r)
			}
		}
	}
	if !found {
		t.Fatal("legacy report missing")
	}
	// An indexed content change must not silently resolve under the old hash.
	if err := os.WriteFile(second.Path, []byte("modified report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReadReport(second.ID); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}
