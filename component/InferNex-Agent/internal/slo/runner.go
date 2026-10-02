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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Identity struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	SpecSHA256 string `json:"specSha256"`
}
type Sample struct {
	Side             string    `json:"side"`
	Identity         Identity  `json:"identity"`
	CaseID           string    `json:"caseId"`
	At               time.Time `json:"at"`
	Millis           float64   `json:"millis"`
	HTTPStatus       int       `json:"httpStatus"`
	ProtocolOK       bool      `json:"protocolOk"`
	AssertionOK      bool      `json:"assertionOk"`
	Success          bool      `json:"success"`
	ResponseSHA256   string    `json:"responseSha256,omitempty"`
	PromptTokens     int       `json:"promptTokens,omitempty"`
	CompletionTokens int       `json:"completionTokens,omitempty"`
}
type Metrics struct {
	Count               int     `json:"count"`
	WindowMillis        float64 `json:"windowMillis"`
	SuccessRate         float64 `json:"successRate"`
	P95Millis           float64 `json:"p95Millis"`
	ThroughputPerSecond float64 `json:"throughputPerSecond"`
}
type Result struct {
	RunID          string  `json:"runId"`
	Decision       string  `json:"decision"`
	Reason         string  `json:"reason"`
	Baseline       Metrics `json:"baseline"`
	Candidate      Metrics `json:"candidate"`
	EvidenceSHA256 string  `json:"evidenceSha256"`
}
type RunRequest struct {
	RunID         string   `json:"runId"`
	ExperimentID  string   `json:"experimentId"`
	StageIndex    int      `json:"stageIndex"`
	ChangeID      string   `json:"changeId"`
	Profile       Profile  `json:"-"`
	ProfileSHA256 string   `json:"profileSha256"`
	Baseline      Identity `json:"baseline"`
	Candidate     Identity `json:"candidate"`
}
type EvidenceStore interface {
	Begin(RunRequest) error
	Complete(string, []Sample, Result) (string, error)
}
type Runner struct {
	Store  EvidenceStore
	Client *http.Client
}

func NewRunner(store EvidenceStore, client *http.Client) *Runner {
	return &Runner{Store: store, Client: client}
}
func (r *Runner) Run(ctx context.Context, request RunRequest) (Result, error) {
	result := Result{RunID: request.RunID, Decision: "inconclusive"}
	if r == nil || r.Store == nil {
		return result, fmt.Errorf("SLO evidence store is required")
	}
	if err := r.Store.Begin(request); err != nil {
		return result, err
	}
	p := request.Profile
	baselineURL, bok := p.Endpoints[request.Baseline.Namespace+"/"+request.Baseline.Name]
	candidateURL, cok := p.Endpoints[request.Candidate.Namespace+"/"+request.Candidate.Name]
	if !bok || !cok {
		return result, fmt.Errorf("SLO profile lacks exact stage endpoint mappings")
	}
	if CanonicalEndpoint(baselineURL) == CanonicalEndpoint(candidateURL) {
		return result, fmt.Errorf("SLO baseline and candidate endpoints must differ")
	}
	client := r.Client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	samples := make([]Sample, 0, 2*p.Samples*len(p.Cases))
	windows := map[string]time.Duration{}
	for _, side := range []struct {
		name, url string
		identity  Identity
	}{{"baseline", baselineURL, request.Baseline}, {"candidate", candidateURL, request.Candidate}} {
		for _, test := range p.Cases {
			for i := 0; i < p.Warmup; i++ {
				runSample(ctx, &clientCopy, side.name, side.url, side.identity, test, p)
			}
		}
		start := time.Now()
		for _, test := range p.Cases {
			for i := 0; i < p.Samples; i++ {
				if ctx.Err() != nil {
					return result, ctx.Err()
				}
				s := runSample(ctx, &clientCopy, side.name, side.url, side.identity, test, p)
				samples = append(samples, s)
			}
		}
		windows[side.name] = time.Since(start)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.Baseline = metrics(samples, "baseline", windows["baseline"])
	result.Candidate = metrics(samples, "candidate", windows["candidate"])
	result.Decision, result.Reason = Evaluate(p, result.Baseline, result.Candidate)
	hash, err := r.Store.Complete(request.RunID, samples, result)
	if err != nil {
		return result, err
	}
	result.EvidenceSHA256 = hash
	return result, nil
}
func runSample(ctx context.Context, client *http.Client, side, url string, id Identity, c Case, p Profile) (s Sample) {
	s = Sample{Side: side, Identity: id, CaseID: c.ID, At: time.Now().UTC()}
	payload, _ := json.Marshal(map[string]any{"model": p.Model, "messages": []map[string]string{{"role": "user", "content": c.Prompt}}, "max_tokens": p.MaxTokens, "temperature": 0, "stream": false})
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMillis)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return s
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	defer func() { s.Millis = float64(time.Since(start).Microseconds()) / 1000 }()
	response, err := client.Do(req)
	if err != nil {
		return s
	}
	defer response.Body.Close()
	s.HTTPStatus = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || len(body) >= 1<<20 || !utf8.Valid(body) {
		return s
	}
	h := sha256.Sum256(body)
	s.ResponseSHA256 = hex.EncodeToString(h[:])
	if response.StatusCode != http.StatusOK {
		return s
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" || decoded.Choices[0].FinishReason != "stop" {
		return s
	}
	s.ProtocolOK = true
	s.PromptTokens = decoded.Usage.PromptTokens
	s.CompletionTokens = decoded.Usage.CompletionTokens
	s.AssertionOK = c.ExactAnswer == "" || decoded.Choices[0].Message.Content == c.ExactAnswer
	s.Success = s.AssertionOK
	return s
}
func metrics(samples []Sample, side string, window time.Duration) Metrics {
	m := Metrics{}
	m.WindowMillis = float64(window.Microseconds()) / 1000
	var latencies []float64
	for _, s := range samples {
		if s.Side != side {
			continue
		}
		m.Count++
		if s.Success {
			m.SuccessRate++
			latencies = append(latencies, s.Millis)
		}
	}
	if m.Count == 0 {
		return m
	}
	successes := m.SuccessRate
	m.SuccessRate /= float64(m.Count)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		m.P95Millis = latencies[int(math.Ceil(.95*float64(len(latencies))))-1]
	}
	if window > 0 {
		m.ThroughputPerSecond = successes / window.Seconds()
	}
	return m
}
func Evaluate(p Profile, b, c Metrics) (string, string) {
	t := p.Thresholds
	if b.Count < p.MinSamples || c.Count < p.MinSamples {
		return "inconclusive", "insufficient samples"
	}
	if b.SuccessRate < t.MinSuccessRate || b.P95Millis <= 0 {
		return "inconclusive", "baseline did not meet quality gate"
	}
	if c.SuccessRate < t.MinSuccessRate {
		return "regression", "candidate success rate below threshold"
	}
	if c.SuccessRate < b.SuccessRate {
		return "regression", "candidate success rate regressed"
	}
	if c.P95Millis > t.MaxP95Millis {
		return "regression", "candidate end-to-end p95 exceeds threshold"
	}
	if c.P95Millis > b.P95Millis*t.MaxP95RegressionRatio {
		return "regression", "candidate end-to-end p95 regressed"
	}
	if c.ThroughputPerSecond < b.ThroughputPerSecond*t.MinThroughputRatio {
		return "regression", "candidate serial throughput regressed"
	}
	return "passed", "SLO gates passed"
}
