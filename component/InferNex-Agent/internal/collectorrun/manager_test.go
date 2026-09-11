package collectorrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnosticexec"
)

type fakeSource struct {
	targets []Target
	calls   atomic.Int32
}

func (f *fakeSource) ListTargets(context.Context, string, string, string, string) ([]Target, error) {
	return f.targets, nil
}

func TestHostRootCollectorDoesNotRequirePodSelectors(t *testing.T) {
	manager, err := NewManager(&fakeSource{}, filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.Create(StartRequest{Channel: "host-root", Profile: "hccn-pfc-stats", DeviceIDs: []int{0}, IntervalSeconds: 60, DurationMinutes: 1, MaxBytes: 1024 * 1024, Confirm: true})
	if err != nil || task.Channel != "host-root" {
		t.Fatalf("task=%#v err=%v", task, err)
	}
}
func (f *fakeSource) Collect(_ context.Context, target Target, profile string, device int) (diagnosticexec.Result, error) {
	f.calls.Add(1)
	return diagnosticexec.Result{Channel: "pod", Probe: profile, Target: target.Pod, Output: "rx pfc: 1\n", ExitCode: 0}, nil
}

func TestCollectorPersistsSamplesAndResumesState(t *testing.T) {
	state, evidence := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "evidence")
	source := &fakeSource{targets: []Target{{Namespace: "models", Pod: "worker-0", UID: "uid-1", Container: "vllm"}}}
	manager, err := NewManager(source, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.StartBackground(ctx)
	task, err := manager.Create(StartRequest{Profile: "hccn-pfc-stats", Namespace: "models", LabelSelector: "app=qwen", Container: "vllm", DeviceIDs: []int{0, 1}, IntervalSeconds: 10, DurationMinutes: 1, MaxBytes: 1024 * 1024, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for source.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if source.calls.Load() != 2 {
		t.Fatalf("calls=%d", source.calls.Load())
	}
	got, err := manager.Stop(task.ID, true)
	if err != nil || got.Samples != 2 {
		t.Fatalf("task=%#v err=%v", got, err)
	}
	payload, err := os.ReadFile(filepath.Join(evidence, task.ID, "samples.jsonl"))
	if err != nil || len(payload) == 0 {
		t.Fatalf("evidence=%q err=%v", payload, err)
	}
	reloaded, err := NewManager(source, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reloaded.Get(task.ID)
	if err != nil || restored.Status != "stopped" {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
}

func TestConcurrentPersistsUseIndependentTemporaryFiles(t *testing.T) {
	state, evidence := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "evidence")
	manager, err := NewManager(&fakeSource{}, state, evidence)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "concurrent-state", Status: "running", CreatedAt: time.Now().UTC()}
	start := make(chan struct{})
	errors := make(chan error, 64)
	var group sync.WaitGroup
	for sample := 0; sample < cap(errors); sample++ {
		group.Add(1)
		go func(sample int) {
			defer group.Done()
			<-start
			copy := task
			copy.Samples = sample
			errors <- manager.persist(copy)
		}(sample)
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent persist failed: %v", err)
		}
	}
	payload, err := os.ReadFile(filepath.Join(state, task.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Task
	if err := json.Unmarshal(payload, &saved); err != nil || saved.ID != task.ID {
		t.Fatalf("invalid final state: task=%#v err=%v", saved, err)
	}
}

func TestCollectorRequiresApprovalAndValidDevices(t *testing.T) {
	manager, err := NewManager(&fakeSource{}, filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(StartRequest{Profile: "hccn-pfc-stats", Namespace: "models", LabelSelector: "app=x", DeviceIDs: []int{0}}); err == nil {
		t.Fatal("unapproved collector accepted")
	}
	if _, err := manager.Create(StartRequest{Profile: "hccn-pfc-stats", Namespace: "models", LabelSelector: "app=x", DeviceIDs: []int{64}, Confirm: true}); err == nil {
		t.Fatal("invalid devices accepted")
	}
}
