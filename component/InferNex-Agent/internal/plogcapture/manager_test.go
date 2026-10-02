package plogcapture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeSource struct {
	targets []PodContainer
	files   map[string][]byte
}

type fakeSnapshotSource struct{ *fakeSource }

func (f *fakeSnapshotSource) Snapshot(context.Context, PodContainer) (TargetSnapshot, error) {
	return TargetSnapshot{PodJSON: []byte(`{"metadata":{"name":"worker-0"}}`), CurrentLog: []byte("runtime stdout\n"), PreviousLog: []byte("previous crash\n")}, nil
}

func (f *fakeSource) ListTargets(context.Context, string, string, string) ([]PodContainer, error) {
	return append([]PodContainer(nil), f.targets...), nil
}

func (f *fakeSource) ListFiles(_ context.Context, _ PodContainer) ([]string, error) {
	result := make([]string, 0, len(f.files))
	for file := range f.files {
		result = append(result, file)
	}
	return result, nil
}

func (f *fakeSource) FileSize(_ context.Context, _ PodContainer, file string) (int64, error) {
	return int64(len(f.files[file])), nil
}

func (f *fakeSource) ReadChunk(_ context.Context, _ PodContainer, file string, offset, limit int64) ([]byte, error) {
	contents := f.files[file]
	end := offset + limit
	if end > int64(len(contents)) {
		end = int64(len(contents))
	}
	return append([]byte(nil), contents[offset:end]...), nil
}

func TestCapturePersistsAcrossPodUIDsAndRestart(t *testing.T) {
	state, evidence := t.TempDir(), t.TempDir()
	source := &fakeSource{targets: []PodContainer{{Namespace: "models", Pod: "worker-0", UID: "uid-1", Container: "vllm"}}, files: map[string][]byte{"/root/ascend/log/plog/runtime.log": []byte("first\n")}}
	manager, err := NewManager(source, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Create(StartRequest{Namespace: "models", LabelSelector: "app=vllm", MaxBytes: 1024 * 1024, DurationMinutes: 60, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.poll(context.Background(), task.ID) {
		t.Fatal("capture stopped unexpectedly")
	}
	source.targets[0].UID = "uid-2"
	source.targets[0].Pod = "worker-1"
	source.files["/root/ascend/log/plog/runtime.log"] = []byte("second\n")
	if !manager.poll(context.Background(), task.ID) {
		t.Fatal("capture stopped after Pod replacement")
	}
	current, err := manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Segments != 2 || current.CapturedBytes != int64(len("first\nsecond\n")) {
		t.Fatalf("unexpected capture state: %#v", current)
	}
	for _, file := range current.SegmentFiles {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("missing evidence segment: %v", err)
		}
	}
	reloaded, err := NewManager(source, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reloaded.Get(task.ID)
	if err != nil || loaded.CapturedBytes != current.CapturedBytes || loaded.Segments != 2 {
		t.Fatalf("reloaded task = %#v, %v", loaded, err)
	}
}

func TestCreateRequiresApprovalAndBoundedSelector(t *testing.T) {
	manager, err := NewManager(&fakeSource{}, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(StartRequest{Namespace: "models", LabelSelector: "app=vllm"}); err == nil {
		t.Fatal("capture without approval was accepted")
	}
	if _, err := manager.Create(StartRequest{Namespace: "models", LabelSelector: "not a selector", Confirm: true}); err == nil {
		t.Fatal("invalid selector was accepted")
	}
	request := StartRequest{Namespace: "models", LabelSelector: "app=vllm", Confirm: true}
	if _, err := manager.Create(request); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(request); err == nil {
		t.Fatal("duplicate running capture was accepted")
	}
}

func TestWithinPlogRoots(t *testing.T) {
	if !withinPlogRoots("/root/ascend/log/plog/a.log", defaultPlogRoots) {
		t.Fatal("valid plog path rejected")
	}
	for _, candidate := range []string{"/etc/shadow", "/root/ascend/log/plog/../../secret", "/root/ascend/log/plog/a\n/etc/shadow"} {
		if withinPlogRoots(candidate, defaultPlogRoots) {
			t.Fatalf("unsafe path accepted: %q", candidate)
		}
	}
}

func TestCaptureStoresPodAndContainerSnapshot(t *testing.T) {
	state, evidence := t.TempDir(), t.TempDir()
	base := &fakeSource{targets: []PodContainer{{Namespace: "models", Pod: "worker-0", UID: "uid-1", Container: "vllm"}}, files: map[string][]byte{"/root/ascend/log/plog/runtime.log": []byte("plog\n")}}
	manager, err := NewManager(&fakeSnapshotSource{base}, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Create(StartRequest{Namespace: "models", LabelSelector: "app=vllm", MaxBytes: 1024 * 1024, DurationMinutes: 60, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.poll(context.Background(), task.ID) {
		t.Fatal("capture stopped unexpectedly")
	}
	directory := filepath.Join(evidence, safeName(task.ID), "uid-1", "vllm")
	for _, name := range []string{"pod.json", "current.log", "previous.log"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}
