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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Case struct {
	ID          string `json:"id"`
	Prompt      string `json:"prompt"`
	ExactAnswer string `json:"exactAnswer,omitempty"`
}
type Thresholds struct {
	MinSuccessRate        float64 `json:"minSuccessRate"`
	MaxP95Millis          float64 `json:"maxP95Millis"`
	MaxP95RegressionRatio float64 `json:"maxP95RegressionRatio"`
	MinThroughputRatio    float64 `json:"minThroughputRatio"`
}
type Profile struct {
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Approved      bool              `json:"approved"`
	Model         string            `json:"model"`
	Endpoints     map[string]string `json:"endpoints"`
	Cases         []Case            `json:"cases"`
	Samples       int               `json:"samples"`
	Warmup        int               `json:"warmup"`
	MaxTokens     int               `json:"maxTokens"`
	TimeoutMillis int               `json:"timeoutMillis"`
	MinSamples    int               `json:"minSamples"`
	Thresholds    Thresholds        `json:"thresholds"`
}
type Summary struct {
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	SHA256        string   `json:"sha256"`
	Samples       int      `json:"samples"`
	Warmup        int      `json:"warmup"`
	TimeoutMillis int      `json:"timeoutMillis"`
	Targets       []string `json:"targets"`
}
type Profiles struct {
	profiles map[string]Profile
	hashes   map[string]string
}

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

func ParseProfile(data []byte) (Profile, string, error) {
	var p Profile
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, "", fmt.Errorf("invalid SLO profile: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, "", fmt.Errorf("SLO profile contains trailing JSON")
	}
	if !safeID.MatchString(p.ID) || !safeID.MatchString(p.Version) || !p.Approved {
		return p, "", fmt.Errorf("SLO profile requires valid id, version and approved=true")
	}
	if len(p.Model) == 0 || len(p.Model) > 128 || len(p.Endpoints) < 2 || len(p.Endpoints) > 32 || len(p.Cases) == 0 || len(p.Cases) > 16 || p.Samples < 1 || p.Samples > 100 || p.Warmup < 0 || p.Warmup > 10 || p.MaxTokens < 1 || p.MaxTokens > 8192 || p.TimeoutMillis < 100 || p.TimeoutMillis > 120000 || p.MinSamples < 1 || p.MinSamples > p.Samples || (p.Samples+p.Warmup)*len(p.Cases)*2 > 400 {
		return p, "", fmt.Errorf("SLO profile budget is invalid or unbounded")
	}
	t := p.Thresholds
	if t.MinSuccessRate <= 0 || t.MinSuccessRate > 1 || t.MaxP95Millis <= 0 || t.MaxP95Millis > 120000 || t.MaxP95RegressionRatio < 1 || t.MaxP95RegressionRatio > 10 || t.MinThroughputRatio <= 0 || t.MinThroughputRatio > 1 {
		return p, "", fmt.Errorf("SLO thresholds are invalid")
	}
	for k, v := range p.Endpoints {
		parts := strings.Split(k, "/")
		if len(parts) != 2 || !safeID.MatchString(parts[0]) || !safeID.MatchString(parts[1]) {
			return p, "", fmt.Errorf("invalid SLO target")
		}
		u, e := url.Parse(v)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return p, "", fmt.Errorf("invalid SLO endpoint")
		}
	}
	seen := map[string]bool{}
	for _, c := range p.Cases {
		if !safeID.MatchString(c.ID) || seen[c.ID] || len(c.Prompt) == 0 || len(c.Prompt) > 8192 || len(c.ExactAnswer) > 8192 {
			return p, "", fmt.Errorf("invalid SLO case")
		}
		seen[c.ID] = true
	}
	canonical, _ := json.Marshal(p)
	h := sha256.Sum256(canonical)
	return p, hex.EncodeToString(h[:]), nil
}
func CanonicalEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
	}
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}
func LoadProfiles(directory string) (*Profiles, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("SLO profile directory is required")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if len(entries) > 128 {
		return nil, fmt.Errorf("too many SLO profile files")
	}
	result := &Profiles{map[string]Profile{}, map[string]string{}}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, e := os.Lstat(filepath.Join(directory, entry.Name()))
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, fmt.Errorf("invalid SLO profile file")
		}
		data, e := os.ReadFile(filepath.Join(directory, entry.Name()))
		if e != nil {
			return nil, e
		}
		p, h, e := ParseProfile(data)
		if e != nil {
			return nil, fmt.Errorf("profile %s: %w", entry.Name(), e)
		}
		if _, ok := result.profiles[p.ID]; ok {
			return nil, fmt.Errorf("duplicate SLO profile id")
		}
		result.profiles[p.ID] = p
		result.hashes[p.ID] = h
	}
	return result, nil
}
func (p *Profiles) Lookup(id string) (Profile, string, error) {
	if p == nil {
		return Profile{}, "", fmt.Errorf("SLO profiles are disabled")
	}
	v, ok := p.profiles[id]
	if !ok {
		return Profile{}, "", fmt.Errorf("SLO profile %q is not approved", id)
	}
	return v, p.hashes[id], nil
}
func (p *Profiles) List() []Summary {
	if p == nil {
		return nil
	}
	out := make([]Summary, 0, len(p.profiles))
	for id, v := range p.profiles {
		targets := make([]string, 0, len(v.Endpoints))
		for k := range v.Endpoints {
			targets = append(targets, k)
		}
		sort.Strings(targets)
		out = append(out, Summary{ID: id, Version: v.Version, SHA256: p.hashes[id], Samples: v.Samples, Warmup: v.Warmup, TimeoutMillis: v.TimeoutMillis, Targets: targets})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
