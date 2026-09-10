/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package localfiles

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/namedfiles"
)

const (
	defaultMaxEntries   = 200
	maximumEntries      = 1000
	defaultMaxMatches   = 100
	maximumMatches      = 500
	maximumReadLines    = 1000
	maximumReadBytes    = 128 * 1024
	maximumScannedBytes = 256 * 1024 * 1024
	maximumFileBytes    = 64 * 1024 * 1024
	maximumReportBytes  = 1024 * 1024
)

type Root struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Entry struct {
	RootID   string    `json:"rootId"`
	Path     string    `json:"path"`
	Type     string    `json:"type"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified"`
}

type FindRequest struct {
	RootID     string
	Path       string
	Pattern    string
	Recursive  bool
	MaxEntries int
}

type FindResult struct {
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

type GrepRequest struct {
	RootID          string
	Path            string
	Pattern         string
	FileGlob        string
	Recursive       bool
	IncludeNoise    bool
	ExcludePatterns []string
	MaxMatches      int
}

type Match struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}

type GrepResult struct {
	Matches       []Match  `json:"matches"`
	FilesScanned  int      `json:"filesScanned"`
	BytesScanned  int64    `json:"bytesScanned"`
	SkippedFiles  int      `json:"skippedFiles"`
	FilteredLines int      `json:"filteredLines"`
	Filters       []string `json:"filters,omitempty"`
	Truncated     bool     `json:"truncated"`
}

type ReadRequest struct {
	RootID          string
	Path            string
	StartLine       int
	MaxLines        int
	Contains        string
	IncludeNoise    bool
	ExcludePatterns []string
}

type ReadResult struct {
	Name          string   `json:"name,omitempty"`
	RootID        string   `json:"rootId"`
	Path          string   `json:"path"`
	SHA256        string   `json:"sha256"`
	Size          int64    `json:"size"`
	StartLine     int      `json:"startLine"`
	EndLine       int      `json:"endLine"`
	NextLine      int      `json:"nextLine,omitempty"`
	EOF           bool     `json:"eof"`
	FilteredLines int      `json:"filteredLines"`
	Filters       []string `json:"filters,omitempty"`
	Content       string   `json:"content"`
}

type Source struct {
	RootID string `json:"rootId"`
	Path   string `json:"path"`
}

type ReportRequest struct {
	Title    string
	Summary  string
	Markdown string
	Sources  []Source
}

type Report struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	SHA256    string    `json:"sha256"`
	Bytes     int       `json:"bytes"`
	CreatedAt time.Time `json:"createdAt"`
}

type ReportList struct {
	Reports []Report `json:"reports"`
}

type Workspace struct {
	roots      map[string]string
	rootList   []Root
	reportRoot string
}

func New(roots []string, reportRoot string) (*Workspace, error) {
	workspace := &Workspace{roots: map[string]string{}}
	seen := map[string]bool{}
	for _, raw := range roots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		absolute, err := filepath.Abs(raw)
		if err != nil {
			return nil, fmt.Errorf("resolve evidence root %q: %w", raw, err)
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("resolve evidence root %q: %w", raw, err)
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("evidence root %q must be an existing directory", raw)
		}
		canonical = filepath.Clean(canonical)
		if unsafeEvidenceRoot(canonical) {
			return nil, fmt.Errorf("evidence root %q is too broad or security-sensitive; use a dedicated collected-log directory", raw)
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		sum := sha256.Sum256([]byte(canonical))
		id := "root-" + hex.EncodeToString(sum[:6])
		workspace.roots[id] = canonical
		workspace.rootList = append(workspace.rootList, Root{ID: id, Path: canonical})
	}
	sort.Slice(workspace.rootList, func(i, j int) bool { return workspace.rootList[i].ID < workspace.rootList[j].ID })
	if strings.TrimSpace(reportRoot) == "" {
		return nil, fmt.Errorf("report root is required")
	}
	absoluteReport, err := filepath.Abs(reportRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve report root: %w", err)
	}
	if err := os.MkdirAll(absoluteReport, 0o700); err != nil {
		return nil, fmt.Errorf("create report root: %w", err)
	}
	if err := os.Chmod(absoluteReport, 0o700); err != nil {
		return nil, fmt.Errorf("protect report root: %w", err)
	}
	canonicalReport, err := filepath.EvalSymlinks(absoluteReport)
	if err != nil {
		return nil, fmt.Errorf("resolve report root: %w", err)
	}
	workspace.reportRoot = filepath.Clean(canonicalReport)
	return workspace, nil
}

func unsafeEvidenceRoot(canonical string) bool {
	volumeRoot := filepath.Clean(filepath.VolumeName(canonical) + string(filepath.Separator))
	if canonical == volumeRoot {
		return true
	}
	if filepath.Separator != '/' {
		return false
	}
	for _, blocked := range []string{"/etc", "/proc", "/sys", "/dev", "/run", "/var/lib/infernex-agent", "/var/lib/infernex-agent/semantic-memory"} {
		if canonical == blocked || (blocked != "/var/lib/infernex-agent" && strings.HasPrefix(canonical, blocked+"/")) {
			return true
		}
	}
	return false
}

func (w *Workspace) Roots() []Root {
	return append([]Root(nil), w.rootList...)
}

func (w *Workspace) Find(ctx context.Context, request FindRequest) (FindResult, error) {
	base, relativeBase, err := w.resolve(request.RootID, request.Path, true)
	if err != nil {
		return FindResult{}, err
	}
	pattern := strings.TrimSpace(request.Pattern)
	if pattern == "" {
		pattern = "*"
	}
	maxEntries := request.MaxEntries
	if maxEntries == 0 {
		maxEntries = defaultMaxEntries
	}
	if maxEntries < 1 || maxEntries > maximumEntries {
		return FindResult{}, fmt.Errorf("max entries must be between 1 and %d", maximumEntries)
	}
	result := FindResult{Entries: []Entry{}}
	err = filepath.WalkDir(base, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == base {
			return nil
		}
		rel, err := filepath.Rel(base, current)
		if err != nil {
			return nil
		}
		if entry.IsDir() && !request.Recursive {
			return filepath.SkipDir
		}
		if !entry.IsDir() && sensitiveFileName(entry.Name()) {
			return nil
		}
		matched, matchErr := matchPattern(pattern, filepath.ToSlash(rel))
		if matchErr != nil {
			return matchErr
		}
		if !matched {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if !info.Mode().IsRegular() {
			return nil
		}
		if len(result.Entries) >= maxEntries {
			result.Truncated = true
			return fs.SkipAll
		}
		result.Entries = append(result.Entries, Entry{
			RootID: request.RootID, Path: filepath.ToSlash(filepath.Join(relativeBase, rel)),
			Type: kind, Size: info.Size(), Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return FindResult{}, err
	}
	return result, nil
}

func (w *Workspace) Grep(ctx context.Context, request GrepRequest) (GrepResult, error) {
	if strings.TrimSpace(request.Pattern) == "" {
		return GrepResult{}, fmt.Errorf("grep pattern is required")
	}
	expression, err := regexp.Compile(request.Pattern)
	if err != nil {
		return GrepResult{}, fmt.Errorf("compile grep pattern: %w", err)
	}
	filters, filterNames, err := compileFilters(request.IncludeNoise, request.ExcludePatterns)
	if err != nil {
		return GrepResult{}, err
	}
	base, relativeBase, err := w.resolve(request.RootID, request.Path, true)
	if err != nil {
		return GrepResult{}, err
	}
	maxMatches := request.MaxMatches
	if maxMatches == 0 {
		maxMatches = defaultMaxMatches
	}
	if maxMatches < 1 || maxMatches > maximumMatches {
		return GrepResult{}, fmt.Errorf("max matches must be between 1 and %d", maximumMatches)
	}
	fileGlob := strings.TrimSpace(request.FileGlob)
	if fileGlob == "" {
		fileGlob = "*"
	}
	result := GrepResult{Matches: []Match{}, Filters: filterNames}
	err = filepath.WalkDir(base, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.SkippedFiles++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == base && !entry.IsDir() {
			return w.grepFile(current, request.RootID, relativeBase, expression, filters, maxMatches, &result)
		}
		if entry.IsDir() {
			if current != base && !request.Recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if sensitiveFileName(entry.Name()) {
			result.SkippedFiles++
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maximumFileBytes {
			result.SkippedFiles++
			return nil
		}
		rel, _ := filepath.Rel(base, current)
		matched, matchErr := matchPattern(fileGlob, filepath.ToSlash(rel))
		if matchErr != nil {
			return matchErr
		}
		if !matched {
			return nil
		}
		return w.grepFile(current, request.RootID, filepath.ToSlash(filepath.Join(relativeBase, rel)), expression, filters, maxMatches, &result)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return GrepResult{}, err
	}
	return result, nil
}

func (w *Workspace) grepFile(filename, rootID, relative string, expression *regexp.Regexp, filters []lineFilter, maxMatches int, result *GrepResult) error {
	info, err := os.Stat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumFileBytes {
		result.SkippedFiles++
		return nil
	}
	if result.BytesScanned+info.Size() > maximumScannedBytes {
		result.Truncated = true
		return fs.SkipAll
	}
	file, err := os.Open(filename)
	if err != nil {
		result.SkippedFiles++
		return nil
	}
	defer file.Close()
	result.FilesScanned++
	result.BytesScanned += info.Size()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.IndexByte(text, 0) >= 0 {
			result.SkippedFiles++
			return nil
		}
		if filtered(text, filters) {
			result.FilteredLines++
			continue
		}
		if !expression.MatchString(text) {
			continue
		}
		result.Matches = append(result.Matches, Match{RootID: rootID, Path: filepath.ToSlash(relative), Line: line, Text: redact(bounded(text, 2048))})
		if len(result.Matches) >= maxMatches {
			result.Truncated = true
			return fs.SkipAll
		}
	}
	if err := scanner.Err(); err != nil {
		result.SkippedFiles++
	}
	return nil
}

func (w *Workspace) Read(request ReadRequest) (ReadResult, error) {
	filename, relative, err := w.resolve(request.RootID, request.Path, false)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return ReadResult{}, fmt.Errorf("evidence path must be a regular file")
	}
	if info.Size() > maximumFileBytes {
		return ReadResult{}, fmt.Errorf("evidence file exceeds %d bytes", maximumFileBytes)
	}
	filters, filterNames, err := compileFilters(request.IncludeNoise, request.ExcludePatterns)
	if err != nil {
		return ReadResult{}, err
	}
	start := request.StartLine
	if start == 0 {
		start = 1
	}
	lines := request.MaxLines
	if lines == 0 {
		lines = 200
	}
	if start < 1 || lines < 1 || lines > maximumReadLines {
		return ReadResult{}, fmt.Errorf("start line must be positive and max lines between 1 and %d", maximumReadLines)
	}
	file, err := os.Open(filename)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ReadResult{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{RootID: request.RootID, Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(hash.Sum(nil)), Size: info.Size(), StartLine: start, Filters: filterNames}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var output strings.Builder
	line := 0
	selected := 0
	for scanner.Scan() {
		line++
		if line < start || (request.Contains != "" && !strings.Contains(scanner.Text(), request.Contains)) {
			continue
		}
		if filtered(scanner.Text(), filters) {
			result.FilteredLines++
			continue
		}
		text := redact(scanner.Text())
		if output.Len()+len(text)+1 > maximumReadBytes || selected >= lines {
			result.NextLine = line
			break
		}
		output.WriteString(text)
		output.WriteByte('\n')
		selected++
		result.EndLine = line
	}
	if err := scanner.Err(); err != nil {
		return ReadResult{}, err
	}
	result.EOF = result.NextLine == 0
	result.Content = output.String()
	return result, nil
}

func (w *Workspace) CreateReport(request ReportRequest) (Report, error) {
	title := strings.TrimSpace(request.Title)
	if title == "" || len([]rune(title)) > 200 {
		return Report{}, fmt.Errorf("report title is required and must not exceed 200 characters")
	}
	if len(request.Markdown) > maximumReportBytes {
		return Report{}, fmt.Errorf("report markdown exceeds %d bytes", maximumReportBytes)
	}
	now := time.Now().UTC()
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\nGenerated by InferNex Agent at %s.\n\n", cleanHeading(title), now.Format(time.RFC3339))
	if summary := strings.TrimSpace(request.Summary); summary != "" {
		fmt.Fprintf(&body, "> %s\n\n", strings.ReplaceAll(redact(summary), "\n", "\n> "))
	}
	if len(request.Sources) > 0 {
		body.WriteString("## Evidence sources\n\n")
		for _, source := range request.Sources {
			filename, relative, err := w.resolve(source.RootID, source.Path, false)
			if err != nil {
				return Report{}, fmt.Errorf("resolve report source: %w", err)
			}
			info, err := os.Stat(filename)
			if err != nil || !info.Mode().IsRegular() || info.Size() > maximumFileBytes {
				return Report{}, fmt.Errorf("report source %s must be a bounded regular file", source.Path)
			}
			digest, err := fileSHA256(filename)
			if err != nil {
				return Report{}, err
			}
			fmt.Fprintf(&body, "- `%s:%s` — SHA-256 `%s`, %d bytes, modified %s\n", source.RootID, filepath.ToSlash(relative), digest, info.Size(), info.ModTime().UTC().Format(time.RFC3339))
		}
		body.WriteString("\n")
	}
	body.WriteString(strings.TrimSpace(redact(request.Markdown)))
	body.WriteString("\n")
	payload := []byte(body.String())
	sum := sha256.Sum256(payload)
	id := hex.EncodeToString(sum[:])
	name := namedfiles.Name(title, now)
	filename, err := namedfiles.Lookup(w.reportRoot, id)
	if os.IsNotExist(err) {
		filename, err = namedfiles.Create(w.reportRoot, name, ".md", id, payload)
	}
	if err != nil {
		return Report{}, fmt.Errorf("create report: %w", err)
	}
	return Report{ID: id, Name: strings.TrimSuffix(filepath.Base(filename), ".md"), Path: filename, Title: title, SHA256: id, Bytes: len(payload), CreatedAt: now}, nil
}

var legacyReportID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[a-f0-9]{12}$`)
var legacyReportName = regexp.MustCompile(`^([0-9]{8}T[0-9]{6}Z-[a-f0-9]{12})-`)
var hashReportID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var readableReportName = regexp.MustCompile(`_[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{2}-[0-9]{2}-[0-9]{2}Z(?:-[0-9]+)?\.md$`)

func (w *Workspace) report(filename string) (Report, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return Report{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximumReportBytes+64*1024 {
		return Report{}, fmt.Errorf("report is not a bounded regular file")
	}
	payload, err := os.ReadFile(filename)
	if err != nil {
		return Report{}, err
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	id := digest
	legacy := legacyReportName.FindStringSubmatch(filepath.Base(filename))
	if len(legacy) == 2 {
		id = legacy[1]
	}
	title, _, _ := strings.Cut(string(payload), "\n")
	title = strings.TrimPrefix(title, "# ")
	created := info.ModTime().UTC()
	for _, line := range strings.SplitN(string(payload), "\n", 5) {
		if timestamp, ok := strings.CutPrefix(line, "Generated by InferNex Agent at "); ok {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSuffix(timestamp, ".")); err == nil {
				created = parsed
			}
		}
	}
	name := strings.TrimSuffix(filepath.Base(filename), ".md")
	if len(legacy) == 2 {
		name = namedfiles.Name(title, created)
	}
	return Report{ID: id, Name: name, Path: filename, Title: title, SHA256: digest, Bytes: len(payload), CreatedAt: created}, nil
}

func (w *Workspace) ListReports() (ReportList, error) {
	entries, err := os.ReadDir(w.reportRoot)
	if err != nil {
		return ReportList{}, err
	}
	result := ReportList{Reports: []Report{}}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") || (!legacyReportName.MatchString(entry.Name()) && !readableReportName.MatchString(entry.Name())) {
			continue
		}
		report, err := w.report(filepath.Join(w.reportRoot, entry.Name()))
		if err != nil {
			continue
		}
		result.Reports = append(result.Reports, report)
	}
	sort.Slice(result.Reports, func(i, j int) bool { return result.Reports[i].CreatedAt.After(result.Reports[j].CreatedAt) })
	return result, nil
}

func (w *Workspace) ReadReport(id string) (ReadResult, error) {
	id = strings.TrimSpace(id)
	if !legacyReportID.MatchString(id) && !hashReportID.MatchString(id) {
		return ReadResult{}, fmt.Errorf("invalid report id")
	}
	filename, err := namedfiles.Lookup(w.reportRoot, id)
	if os.IsNotExist(err) {
		// Rebuild from the actual reports only when the direct index is missing.
		reports, listErr := w.ListReports()
		if listErr != nil {
			return ReadResult{}, listErr
		}
		for _, report := range reports.Reports {
			if report.ID == id || report.SHA256 == id {
				filename = report.Path
				err = namedfiles.Index(w.reportRoot, id, filepath.Base(filename))
				break
			}
		}
	}
	if err != nil {
		return ReadResult{}, fmt.Errorf("report not found: %w", err)
	}
	report, err := w.report(filename)
	if err != nil {
		return ReadResult{}, err
	}
	if report.ID != id && report.SHA256 != id {
		return ReadResult{}, fmt.Errorf("report index id mismatch")
	}
	payload, err := os.ReadFile(filename)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Name: report.Name, Path: filename, SHA256: report.SHA256, Size: int64(len(payload)), StartLine: 1, EOF: true, Content: string(payload)}, nil
}

func (w *Workspace) resolve(rootID, relative string, allowDirectory bool) (string, string, error) {
	root, ok := w.roots[strings.TrimSpace(rootID)]
	if !ok {
		return "", "", fmt.Errorf("unknown evidence root id")
	}
	relative = filepath.Clean(strings.TrimSpace(relative))
	if relative == "." {
		relative = ""
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("evidence path must be relative and remain within its configured root")
	}
	target := filepath.Join(root, relative)
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve evidence path: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if canonical != root && !strings.HasPrefix(canonical, root+string(filepath.Separator)) {
		return "", "", fmt.Errorf("evidence path escapes its configured root")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() && !allowDirectory {
		return "", "", fmt.Errorf("evidence path must be a regular file")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("special files are not readable evidence")
	}
	if info.Mode().IsRegular() && sensitiveFileName(filepath.Base(canonical)) {
		return "", "", fmt.Errorf("credential-like files are excluded from local evidence")
	}
	resolvedRelative, _ := filepath.Rel(root, canonical)
	if resolvedRelative == "." {
		resolvedRelative = ""
	}
	return canonical, resolvedRelative, nil
}

func matchPattern(pattern, relative string) (bool, error) {
	pattern = filepath.ToSlash(pattern)
	relative = filepath.ToSlash(relative)
	matched, err := path.Match(pattern, relative)
	if err != nil {
		return false, fmt.Errorf("invalid glob pattern: %w", err)
	}
	if matched || strings.Contains(pattern, "/") {
		return matched, nil
	}
	return path.Match(pattern, path.Base(relative))
}

func sensitiveFileName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case ".env", ".netrc", "id_rsa", "id_ed25519", "kubeconfig":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".key", ".pem", ".p12", ".pfx", ".kubeconfig":
		return true
	default:
		return false
	}
}

func fileSHA256(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maximumFileBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[^\s]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|password|passwd|token|secret)\s*[:=]\s*)[^\s,;]+`),
}

var defaultNoisePatterns = []struct {
	name       string
	expression *regexp.Regexp
}{
	{name: "http-probes:/metrics", expression: regexp.MustCompile(`(?i)(?:^|[\s\"])/(?:metrics)(?:[?\s\"]|$)`)},
	{name: "http-probes:/health*", expression: regexp.MustCompile(`(?i)(?:^|[\s\"])/(?:health|healthz|readyz|livez)(?:[/?\s\"]|$)`)},
}

var anomalyPattern = regexp.MustCompile(`(?i)\b(?:error|warn(?:ing)?|fatal|panic|traceback|exception|failed|timeout|unhealthy)\b`)

type lineFilter struct {
	expression      *regexp.Regexp
	preserveAnomaly bool
}

func compileFilters(includeNoise bool, excludes []string) ([]lineFilter, []string, error) {
	filters := []lineFilter{}
	names := []string{}
	if !includeNoise {
		for _, rule := range defaultNoisePatterns {
			filters = append(filters, lineFilter{expression: rule.expression, preserveAnomaly: true})
			names = append(names, rule.name)
		}
	}
	for _, raw := range excludes {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if len(raw) > 512 {
			return nil, nil, fmt.Errorf("exclude pattern exceeds 512 characters")
		}
		expression, err := regexp.Compile(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("compile exclude pattern %q: %w", raw, err)
		}
		filters = append(filters, lineFilter{expression: expression})
		names = append(names, "operator:"+raw)
	}
	return filters, names, nil
}

func filtered(line string, filters []lineFilter) bool {
	for _, filter := range filters {
		if filter.expression.MatchString(line) {
			// Default probe rules must never suppress a line that also carries an
			// anomaly. Explicit operator rules are applied exactly as requested.
			if filter.preserveAnomaly && anomalyPattern.MatchString(line) {
				continue
			}
			return true
		}
	}
	return false
}

func redact(value string) string {
	for _, expression := range credentialPatterns {
		value = expression.ReplaceAllString(value, `${1}[REDACTED]`)
	}
	return value
}

func bounded(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "...[truncated]"
}

func cleanHeading(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}
