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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
)

const DefaultStateDir = "/var/lib/infernex-agent/config-versions"
const maxFile = 8 << 20

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}$`)
var hexPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type Attachment struct {
	SHA256     string `json:"sha256"`
	File       string `json:"file"`
	SnapshotID string `json:"snapshotId,omitempty"`
	Scope      string `json:"scope,omitempty"`
}
type Record struct {
	APIVersion   string                `json:"apiVersion"`
	Kind         string                `json:"kind"`
	ID           string                `json:"id"`
	CreatedAt    time.Time             `json:"createdAt"`
	Name         string                `json:"name"`
	GitCommit    string                `json:"gitCommit"`
	GitRef       string                `json:"gitRef"`
	GitFile      string                `json:"gitFile"`
	GitBlob      string                `json:"gitBlob"`
	ConfigSHA256 string                `json:"configSha256"`
	Before       Attachment            `json:"before"`
	After        Attachment            `json:"after"`
	Evidence     map[string]Attachment `json:"evidence,omitempty"`
	Coverage     string                `json:"coverage"`
	Limitations  []string              `json:"limitations"`
	SHA256       string                `json:"sha256"`
}
type Input struct {
	StateDir, Repo, Ref, File, Before, After, Name string
	Evidence                                       map[string]string
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validPath(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "/") && !strings.Contains(s, "\\") && path.Clean(s) == s && s != "." && !strings.Contains("/"+s+"/", "/../") && !strings.Contains("/"+s+"/", "/./")
}
func safeDir(p string, create bool) error {
	if p == "" {
		return errors.New("state directory required")
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(p)
	cur := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(p, cur), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, e := os.Lstat(cur)
		if os.IsNotExist(e) && create {
			if e = os.Mkdir(cur, 0700); e != nil {
				return e
			}
			info, e = os.Lstat(cur)
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 && cur != p {
			target, targetErr := filepath.EvalSymlinks(cur)
			if targetErr != nil {
				return targetErr
			}
			targetInfo, targetErr := os.Stat(target)
			if targetErr != nil || !targetInfo.IsDir() {
				return fmt.Errorf("unsafe directory %q", cur)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe directory %q", cur)
		}
	}
	return nil
}
func readFile(p string) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFile {
		return nil, fmt.Errorf("unsafe or oversized file %q", p)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening %q", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("oversized file %q", p)
	}
	return b, nil
}
func git(repo string, args ...string) ([]byte, error) {
	a := append([]string{"--no-replace-objects", "-C", repo, "--literal-pathspecs"}, args...)
	c := exec.Command("git", a...)
	env := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			env = append(env, item)
		}
	}
	c.Env = append(env, "GIT_NO_REPLACE_OBJECTS=1")
	b, e := c.Output()
	if e != nil {
		if errors.Is(e, exec.ErrNotFound) {
			return nil, errors.New("git executable unavailable")
		}
		return nil, fmt.Errorf("git %s: %w", args[0], e)
	}
	if len(b) > maxFile+1024 {
		return nil, errors.New("git output too large")
	}
	return b, nil
}
func checkRef(ref string) bool {
	return ref != "" && !strings.HasPrefix(ref, "-") && !strings.ContainsAny(ref, "\x00\n\r ") && !strings.Contains(ref, "..") && !strings.Contains(ref, "@{") && !strings.ContainsAny(ref, "~^?:*[]\\")
}
func gitConfig(repo, ref, file string) (string, string, []byte, error) {
	if !checkRef(ref) {
		return "", "", nil, errors.New("invalid git ref")
	}
	if !validPath(file) {
		return "", "", nil, errors.New("invalid relative git file path")
	}
	commitRaw, err := git(repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", "", nil, err
	}
	commit := strings.TrimSpace(string(commitRaw))
	if !hexPattern.MatchString(commit) {
		return "", "", nil, errors.New("invalid resolved commit")
	}
	tree, err := git(repo, "ls-tree", "-z", commit, "--", file)
	if err != nil {
		return "", "", nil, err
	}
	items := strings.Split(strings.TrimSuffix(string(tree), "\x00"), "\x00")
	if len(items) != 1 {
		return "", "", nil, errors.New("git file absent or ambiguous")
	}
	parts := strings.SplitN(items[0], "\t", 2)
	if len(parts) != 2 || parts[1] != file {
		return "", "", nil, errors.New("git file path mismatch")
	}
	fields := strings.Fields(parts[0])
	if len(fields) != 3 || fields[0] != "100644" && fields[0] != "100755" || fields[1] != "blob" || !hexPattern.MatchString(fields[2]) {
		return "", "", nil, errors.New("git file is not a regular blob")
	}
	sizeRaw, err := git(repo, "cat-file", "-s", fields[2])
	if err != nil {
		return "", "", nil, err
	}
	var size int64
	if _, err = fmt.Sscanf(string(sizeRaw), "%d", &size); err != nil || size < 0 || size > maxFile {
		return "", "", nil, errors.New("git blob too large or invalid")
	}
	b, err := git(repo, "cat-file", "blob", fields[2])
	if err != nil {
		return "", "", nil, err
	}
	if int64(len(b)) != size {
		return "", "", nil, errors.New("git blob size mismatch")
	}
	return commit, fields[2], b, nil
}
func attachment(p, kind string) (Attachment, []byte, error) {
	b, e := readFile(p)
	if e != nil {
		return Attachment{}, nil, e
	}
	a := Attachment{SHA256: sum(b), File: kind + ".json"}
	if kind == "before" || kind == "after" {
		var s changesafety.ClusterSnapshot
		if e = json.Unmarshal(b, &s); e != nil {
			return a, nil, e
		}
		if e = changesafety.VerifySnapshot(s); e != nil {
			return a, nil, e
		}
		a.SnapshotID = s.ID
		a.Scope = "InferNexService resources in captured namespaces; baseRefs templates excluded"
	}
	return a, b, nil
}
func checksum(r Record) (string, error) {
	r.SHA256 = ""
	b, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	return sum(b), nil
}
func write(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}
func RecordVersion(in Input) (Record, error) {
	if in.StateDir == "" {
		in.StateDir = DefaultStateDir
	}
	if err := safeDir(in.StateDir, true); err != nil {
		return Record{}, err
	}
	if info, err := os.Stat(in.StateDir); err != nil {
		return Record{}, err
	} else if info.Mode().Perm()&0077 != 0 {
		return Record{}, errors.New("state directory must be private (0700)")
	}
	commit, blob, config, err := gitConfig(in.Repo, in.Ref, in.File)
	if err != nil {
		return Record{}, err
	}
	before, bb, err := attachment(in.Before, "before")
	if err != nil {
		return Record{}, fmt.Errorf("before snapshot: %w", err)
	}
	after, ab, err := attachment(in.After, "after")
	if err != nil {
		return Record{}, fmt.Errorf("after snapshot: %w", err)
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		name = "config-version"
	}
	name = strings.Trim(name, "-")
	if len(name) > 40 || !regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9-]*[a-z0-9])?$`).MatchString(name) {
		return Record{}, errors.New("name must be 1-40 lowercase letters, digits, or hyphens")
	}
	var random [6]byte
	if _, err = rand.Read(random[:]); err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	id := name + "-" + now.Format("20060102T150405Z") + "-" + hex.EncodeToString(random[:])
	r := Record{APIVersion: "agent.infernex.io/v1alpha1", Kind: "InferNexConfigVersion", ID: id, CreatedAt: now, Name: name, GitCommit: commit, GitRef: in.Ref, GitFile: in.File, GitBlob: blob, ConfigSHA256: sum(config), Before: before, After: after, Coverage: "InferNexService resources in snapshot namespaces", Limitations: []string{"baseRefs templates are not captured by ClusterSnapshot", "SLO and other evidence files are bound by SHA256 only; their meaning is not verified", "local metadata only; no GitOps restore or cluster write", "offline verification checks local bundle integrity only; it does not re-prove Git commit membership or signature", "Git configuration and before/after snapshots are not semantically checked as one change", "the live cluster and current Git ref are not checked"}}
	files := map[string][]byte{"config.bin": config, "before.json": bb, "after.json": ab}
	for _, key := range []string{"experiment", "stage", "change", "slo"} {
		p := in.Evidence[key]
		if p == "" {
			continue
		}
		a, b, e := attachment(p, key)
		if e != nil {
			return Record{}, fmt.Errorf("%s evidence: %w", key, e)
		}
		if r.Evidence == nil {
			r.Evidence = map[string]Attachment{}
		}
		r.Evidence[key] = a
		files[a.File] = b
	}
	r.SHA256, err = checksum(r)
	if err != nil {
		return Record{}, err
	}
	metadata, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return Record{}, err
	}
	metadata = append(metadata, '\n')
	if err = publish(in.StateDir, id, files, metadata); err != nil {
		return Record{}, err
	}
	return r, nil
}

// publish keeps incomplete bundles invisible to List and makes the final name
// visible only after all files and the temporary directory have been synced.
func publish(state, id string, files map[string][]byte, metadata []byte) error {
	temp, err := os.MkdirTemp(state, ".config-version-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for name, b := range files {
		if !validPath(name) || strings.Contains(name, "/") {
			return errors.New("invalid bundle filename")
		}
		if err = write(filepath.Join(temp, name), b); err != nil {
			return err
		}
	}
	if err = write(filepath.Join(temp, "record.json"), metadata); err != nil {
		return err
	}
	d, err := os.Open(temp)
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	final := filepath.Join(state, id)
	if _, err = os.Lstat(final); err == nil {
		return fmt.Errorf("version %s already exists", id)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(temp, final); err != nil {
		return err
	}
	parent, err := os.Open(state)
	if err != nil {
		return err
	}
	err = parent.Sync()
	closeErr = parent.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func load(state, id string) (Record, string, error) {
	if state == "" {
		state = DefaultStateDir
	}
	if !idPattern.MatchString(id) {
		return Record{}, "", errors.New("invalid version id")
	}
	if err := safeDir(state, false); err != nil {
		return Record{}, "", err
	}
	dir := filepath.Join(state, id)
	if err := safeDir(dir, false); err != nil {
		return Record{}, "", err
	}
	b, err := readFile(filepath.Join(dir, "record.json"))
	if err != nil {
		return Record{}, "", err
	}
	var r Record
	if err = json.Unmarshal(b, &r); err != nil {
		return Record{}, "", err
	}
	if r.ID != id {
		return Record{}, "", errors.New("version id mismatch")
	}
	return r, dir, nil
}
func Show(state, id string) (Record, error) { return Verify(state, id) }
func Verify(state, id string) (Record, error) {
	r, dir, e := load(state, id)
	if e != nil {
		return r, e
	}
	if r.APIVersion != "agent.infernex.io/v1alpha1" || r.Kind != "InferNexConfigVersion" || r.CreatedAt.IsZero() || r.Name == "" || !strings.HasPrefix(r.ID, r.Name+"-") || !hexPattern.MatchString(r.GitCommit) || !hexPattern.MatchString(r.GitBlob) || !checkRef(r.GitRef) || !validPath(r.GitFile) {
		return r, errors.New("invalid version metadata")
	}
	expected, e := checksum(r)
	if e != nil {
		return r, e
	}
	if expected != r.SHA256 {
		return r, errors.New("record checksum mismatch")
	}
	config, e := readFile(filepath.Join(dir, "config.bin"))
	if e != nil {
		return r, e
	}
	if sum(config) != r.ConfigSHA256 {
		return r, errors.New("configuration checksum mismatch")
	}
	for k, a := range map[string]Attachment{"before": r.Before, "after": r.After} {
		if a.File != k+".json" {
			return r, errors.New("snapshot binding mismatch")
		}
		b, er := readFile(filepath.Join(dir, a.File))
		if er != nil {
			return r, er
		}
		if sum(b) != a.SHA256 {
			return r, errors.New("snapshot file checksum mismatch")
		}
		var s changesafety.ClusterSnapshot
		if er = json.Unmarshal(b, &s); er != nil {
			return r, er
		}
		if er = changesafety.VerifySnapshot(s); er != nil {
			return r, er
		}
		if s.ID != a.SnapshotID {
			return r, errors.New("snapshot id mismatch")
		}
	}
	for k, a := range r.Evidence {
		if k != "experiment" && k != "stage" && k != "change" && k != "slo" || a.File != k+".json" {
			return r, errors.New("evidence binding mismatch")
		}
		b, er := readFile(filepath.Join(dir, a.File))
		if er != nil {
			return r, er
		}
		if sum(b) != a.SHA256 {
			return r, errors.New("evidence checksum mismatch")
		}
	}
	return r, nil
}
func List(state string) ([]Record, error) {
	if state == "" {
		state = DefaultStateDir
	}
	if err := safeDir(state, false); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, entry := range entries {
		if !idPattern.MatchString(entry.Name()) {
			continue
		}
		r, e := Show(state, entry.Name())
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
