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

package slo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testProfile(a, b string) Profile {
	return Profile{ID: "smoke", Version: "1", Approved: true, Model: "test-model", Endpoints: map[string]string{"ns/base": a, "ns/candidate": b}, Cases: []Case{{ID: "identity", Prompt: "say ok", ExactAnswer: "ok"}}, Samples: 3, MaxTokens: 16, TimeoutMillis: 1000, MinSamples: 3, Thresholds: Thresholds{MinSuccessRate: 1, MaxP95Millis: 1000, MaxP95RegressionRatio: 10, MinThroughputRatio: .01}}
}
func endpoint(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + reply + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
}
func TestRunnerRealHTTP(t *testing.T) {
	base := endpoint("ok")
	defer base.Close()
	candidate := endpoint("ok")
	defer candidate.Close()
	store, e := NewFileStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := testProfile(base.URL, candidate.URL)
	result, e := NewRunner(store, nil).Run(context.Background(), RunRequest{RunID: "run-1", ExperimentID: "exp-1", Profile: p, ProfileSHA256: "hash", Baseline: Identity{Namespace: "ns", Name: "base", UID: "a"}, Candidate: Identity{Namespace: "ns", Name: "candidate", UID: "b"}})
	if e != nil {
		t.Fatal(e)
	}
	if result.Decision != "passed" || result.EvidenceSHA256 == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, e := os.ReadFile(filepath.Join(store.root, "run-1", "evidence.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "say ok") || strings.Contains(string(data), `"content":"ok"`) {
		t.Fatal("private content leaked into evidence")
	}
	intent, e := os.ReadFile(filepath.Join(store.root, "run-1", "intent.json"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(intent), `"profileSha256":"hash"`) {
		t.Fatal("profile hash absent from intent")
	}
	var evidence struct{ Samples []Sample }
	if json.Unmarshal(data, &evidence) != nil || len(evidence.Samples) != 6 {
		t.Fatal("missing samples")
	}
}
func TestRegressionAndInconclusive(t *testing.T) {
	p := testProfile("http://a", "http://b")
	b := Metrics{Count: 3, SuccessRate: 1, P95Millis: 20, ThroughputPerSecond: 3}
	c := b
	c.SuccessRate = .5
	if decision, _ := Evaluate(p, b, c); decision != "regression" {
		t.Fatal(decision)
	}
	b.SuccessRate = .5
	if decision, _ := Evaluate(p, b, c); decision != "inconclusive" {
		t.Fatal(decision)
	}
	b.Count = 2
	if decision, _ := Evaluate(p, b, c); decision != "inconclusive" {
		t.Fatal(decision)
	}
}
func TestStrictProfile(t *testing.T) {
	p := testProfile("http://a", "http://b")
	data, _ := json.Marshal(p)
	if _, _, e := ParseProfile(append(data, []byte("garbage")...)); e == nil {
		t.Fatal("trailing junk accepted")
	}
	if _, _, e := ParseProfile(append(data, []byte(` {}`)...)); e == nil {
		t.Fatal("second document accepted")
	}
	if _, _, e := ParseProfile(append(data[:len(data)-1], []byte(`,"unexpected":1}`)...)); e == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestCanonicalEndpoint(t *testing.T) {
	if CanonicalEndpoint("HTTP://EXAMPLE.COM:80/") != CanonicalEndpoint("http://example.com") {
		t.Fatal("equivalent endpoints differ")
	}
	p := testProfile("http://example.com/", "HTTP://EXAMPLE.COM:80")
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunner(store, nil).Run(context.Background(), RunRequest{RunID: "same-endpoint", Profile: p, Baseline: Identity{Namespace: "ns", Name: "base"}, Candidate: Identity{Namespace: "ns", Name: "candidate"}})
	if err == nil {
		t.Fatal("same endpoint mapping accepted")
	}
}
func TestTimeoutProtocolAndRedirect(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer slow.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"length"}]}`))
	}))
	defer bad.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, bad.URL, http.StatusFound) }))
	defer redirect.Close()
	p := testProfile(slow.URL, bad.URL)
	p.TimeoutMillis = 100
	id := Identity{Namespace: "ns", Name: "base"}
	if s := runSample(context.Background(), &http.Client{}, "baseline", slow.URL, id, p.Cases[0], p); s.Success || s.Millis < 90 {
		t.Fatalf("timeout sample: %+v", s)
	}
	if s := runSample(context.Background(), &http.Client{}, "candidate", bad.URL, id, p.Cases[0], p); s.ProtocolOK {
		t.Fatalf("truncated completion accepted: %+v", s)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s := runSample(context.Background(), client, "candidate", redirect.URL, id, p.Cases[0], p); s.HTTPStatus != http.StatusFound || s.Success {
		t.Fatalf("redirect followed: %+v", s)
	}
}
