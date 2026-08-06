/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package diagnostics

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

type fakeLogReader struct {
	contents map[string][]byte
}

func (f *fakeLogReader) Read(
	_ context.Context,
	_ string,
	pod string,
	container string,
	_ time.Time,
	previous bool,
	_ int64,
	_ int64,
) ([]byte, error) {
	key := fmt.Sprintf("%s/%s/%t", pod, container, previous)
	contents, found := f.contents[key]
	if !found {
		return []byte{}, nil
	}
	return contents, nil
}

type fakeObserver struct {
	events observer.EventEvidence
}

func (f *fakeObserver) ListServices(context.Context, string) (observer.ServiceList, error) {
	return observer.ServiceList{}, nil
}

func (f *fakeObserver) InspectService(context.Context, string, string) (observer.ServiceDetail, error) {
	return observer.ServiceDetail{}, nil
}

func (f *fakeObserver) GetTopology(context.Context, string, string) (observer.Topology, error) {
	return observer.Topology{}, nil
}

func (f *fakeObserver) GetEvents(context.Context, string, string, int, int) (observer.EventEvidence, error) {
	return f.events, nil
}

func TestCollectorCorrelatesCrossNodeRootCauseAndRedactsSecrets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 6, 2, 0, 30, 0, time.UTC)
	pods := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "models", Name: "demo-prefill-0",
				Labels: map[string]string{ownerLabel: "demo", componentLabel: "engine-pd-prefill"},
			},
			Spec: corev1.PodSpec{
				NodeName: "npu-a", Containers: []corev1.Container{{Name: "engine"}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "models", Name: "demo-decode-0",
				Labels: map[string]string{ownerLabel: "demo", componentLabel: "engine-pd-decode"},
			},
			Spec: corev1.PodSpec{
				NodeName: "npu-b", Containers: []corev1.Container{{Name: "engine"}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "models", Name: "demo-router-0",
				Labels: map[string]string{ownerLabel: "demo", componentLabel: "hermes-router"},
			},
			Spec: corev1.PodSpec{
				NodeName: "cpu-a", Containers: []corev1.Container{{Name: "router"}},
			},
		},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(pods...).Build()
	reader := &fakeLogReader{contents: map[string][]byte{
		"demo-prefill-0/engine/false": []byte("2026-08-06T02:00:00Z NPU device lost: ACL error api_key=very-secret\n"),
		"demo-decode-0/engine/false":  []byte("2026-08-06T02:00:10Z vLLM worker died after device failure\n"),
		"demo-router-0/router/false":  []byte("2026-08-06T02:00:20Z upstream stream interrupted: connection reset by peer\n"),
	}}
	collector, err := New(kubeClient, reader, &fakeObserver{})
	if err != nil {
		t.Fatal(err)
	}
	collector.now = func() time.Time { return now }

	report, err := collector.Diagnose(context.Background(), Request{
		Namespace: "models", Name: "demo", SinceMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalPods != 3 || len(report.Incidents) != 1 {
		t.Fatalf("unexpected report: pods=%d incidents=%#v", report.TotalPods, report.Incidents)
	}
	incident := report.Incidents[0]
	if incident.RootCategory != "npu-device-failure" || incident.Confidence != "high" {
		t.Fatalf("unexpected incident root: %#v", incident)
	}
	if len(incident.Nodes) != 3 || len(incident.Components) != 3 {
		t.Fatalf("expected cross-node/component correlation, got %#v", incident)
	}
	for _, evidence := range report.Evidence {
		if strings.Contains(evidence.Message, "very-secret") {
			t.Fatalf("secret was not redacted: %q", evidence.Message)
		}
	}
}

func TestCollectorFlagsInvalidUTF8(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "models", Name: "demo-engine-0",
			Labels: map[string]string{ownerLabel: "demo", componentLabel: "engine-aggregate"},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine"}}},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	collector, err := New(kubeClient, &fakeLogReader{contents: map[string][]byte{
		"demo-engine-0/engine/false": {0xff, 0xfe, '\n'},
	}}, &fakeObserver{})
	if err != nil {
		t.Fatal(err)
	}

	report, err := collector.Diagnose(context.Background(), Request{Namespace: "models", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Incidents) != 1 || report.Incidents[0].RootCategory != "output-corruption" {
		t.Fatalf("expected output corruption incident, got %#v", report.Incidents)
	}
}

func TestCompareOnlyFailsOnCandidateRegression(t *testing.T) {
	t.Parallel()
	critical := func(category string) Incident {
		return Incident{RootCategory: category, Severity: SeverityCritical}
	}
	baseline := Report{
		Service:   ServiceReference{Namespace: "models", Name: "stable"},
		Incidents: []Incident{critical("operation-timeout")},
	}
	candidate := Report{
		Service: ServiceReference{Namespace: "models", Name: "candidate"},
		Incidents: []Incident{
			critical("operation-timeout"),
			critical("kv-transport-failure"),
		},
	}
	comparison := Compare(baseline, candidate)
	if comparison.Healthy || len(comparison.RegressionCategories) != 1 ||
		comparison.RegressionCategories[0] != "kv-transport-failure" {
		t.Fatalf("unexpected comparison: %#v", comparison)
	}
}

func TestSanitizeMessageRedactsBearerAndStructuredSecrets(t *testing.T) {
	t.Parallel()
	message := sanitizeMessage(
		"Authorization: Bearer bearer-secret api_key=key-secret password: pass-secret https://user:pass@example.invalid/path",
	)
	for _, secret := range []string{"bearer-secret", "key-secret", "pass-secret", "user:pass"} {
		if strings.Contains(message, secret) {
			t.Fatalf("secret %q was not redacted from %q", secret, message)
		}
	}
	if strings.Count(message, "<redacted>") != 4 {
		t.Fatalf("unexpected sanitized message: %q", message)
	}
}

func TestCollectorRedactsRawEventNotesInReport(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	domainObserver := &fakeObserver{events: observer.EventEvidence{
		Service: observer.ServiceReference{Namespace: "models", Name: "demo"},
		Events: []observer.EventSummary{{
			Type: corev1.EventTypeNormal,
			Note: "request failed with Authorization: Bearer event-secret",
		}},
	}}
	collector, err := New(kubeClient, &fakeLogReader{contents: map[string][]byte{}}, domainObserver)
	if err != nil {
		t.Fatal(err)
	}
	report, err := collector.Diagnose(context.Background(), Request{Namespace: "models", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Events.Events) != 1 || strings.Contains(report.Events.Events[0].Note, "event-secret") {
		t.Fatalf("event note was not redacted: %#v", report.Events.Events)
	}
}
