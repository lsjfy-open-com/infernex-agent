/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

// Package skills loads bounded, operator-installed diagnostic knowledge.
// Skills are instructions and references only: the registry never executes
// bundled scripts or grants additional cluster/host permissions.
package skills

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxSkillBytes     = 256 << 10
	maxReferenceBytes = 512 << 10
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

type Registry struct {
	skills map[string]Skill
}

type Skill struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Origin      string      `json:"origin"`
	Digest      string      `json:"sha256"`
	References  []Reference `json:"references,omitempty"`
	path        string
}

type Reference struct {
	Name   string `json:"name"`
	Digest string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Content struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Origin       string      `json:"origin"`
	Digest       string      `json:"sha256"`
	Instructions string      `json:"instructions"`
	References   []Reference `json:"references,omitempty"`
}

type ReferenceContent struct {
	Skill   string `json:"skill"`
	Name    string `json:"name"`
	Digest  string `json:"sha256"`
	Content string `json:"content"`
}

func NewRegistry(directories []string) (*Registry, error) {
	registry := &Registry{skills: map[string]Skill{}}
	for _, directory := range directories {
		directory = strings.TrimSpace(directory)
		if directory == "" {
			continue
		}
		entries, err := os.ReadDir(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skill directory %s: %w", directory, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			skill, err := Load(filepath.Join(directory, entry.Name()), filepath.Base(filepath.Clean(directory)))
			if err != nil {
				return nil, fmt.Errorf("load skill %s: %w", entry.Name(), err)
			}
			if _, exists := registry.skills[skill.Name]; exists {
				return nil, fmt.Errorf("duplicate skill name %q", skill.Name)
			}
			registry.skills[skill.Name] = skill
		}
	}
	return registry, nil
}

func Load(directory, origin string) (Skill, error) {
	resolved, err := filepath.Abs(directory)
	if err != nil {
		return Skill{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return Skill{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Skill{}, fmt.Errorf("skill path must be a real directory")
	}
	path := filepath.Join(resolved, "SKILL.md")
	payload, digest, err := readRegularFile(path, maxSkillBytes)
	if err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	name, description, _, err := parseSkill(payload)
	if err != nil {
		return Skill{}, err
	}
	if filepath.Base(resolved) != name {
		return Skill{}, fmt.Errorf("directory name %q must equal skill name %q", filepath.Base(resolved), name)
	}
	references, err := loadReferences(filepath.Join(resolved, "references"))
	if err != nil {
		return Skill{}, err
	}
	return Skill{Name: name, Description: description, Origin: origin, Digest: digest, References: references, path: resolved}, nil
}

func (r *Registry) List() []Skill {
	result := make([]Skill, 0, len(r.skills))
	for _, skill := range r.skills {
		skill.path = ""
		result = append(result, skill)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Read(name string) (Content, error) {
	skill, ok := r.skills[name]
	if !ok {
		return Content{}, fmt.Errorf("skill %q not found", name)
	}
	payload, _, err := readRegularFile(filepath.Join(skill.path, "SKILL.md"), maxSkillBytes)
	if err != nil {
		return Content{}, err
	}
	_, _, body, err := parseSkill(payload)
	if err != nil {
		return Content{}, err
	}
	return Content{Name: skill.Name, Description: skill.Description, Origin: skill.Origin, Digest: skill.Digest, Instructions: body, References: skill.References}, nil
}

func (r *Registry) ReadReference(skillName, referenceName string) (ReferenceContent, error) {
	skill, ok := r.skills[skillName]
	if !ok {
		return ReferenceContent{}, fmt.Errorf("skill %q not found", skillName)
	}
	clean := filepath.Clean(referenceName)
	if clean != referenceName || filepath.IsAbs(clean) || clean == "." || strings.Contains(clean, string(filepath.Separator)) || !strings.HasSuffix(strings.ToLower(clean), ".md") {
		return ReferenceContent{}, fmt.Errorf("reference must be one Markdown filename returned by the skill")
	}
	known := false
	for _, reference := range skill.References {
		if reference.Name == clean {
			known = true
			break
		}
	}
	if !known {
		return ReferenceContent{}, fmt.Errorf("reference %q not found in skill %q", clean, skillName)
	}
	payload, digest, err := readRegularFile(filepath.Join(skill.path, "references", clean), maxReferenceBytes)
	if err != nil {
		return ReferenceContent{}, err
	}
	return ReferenceContent{Skill: skillName, Name: clean, Digest: digest, Content: string(payload)}, nil
}

func parseSkill(payload []byte) (string, string, string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", "", "", fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	name, description := "", ""
	frontmatterClosed := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			frontmatterClosed = true
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return "", "", "", fmt.Errorf("invalid frontmatter line %q", line)
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		default:
			return "", "", "", fmt.Errorf("unsupported frontmatter field %q", strings.TrimSpace(key))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", "", err
	}
	if !frontmatterClosed {
		return "", "", "", fmt.Errorf("unterminated YAML frontmatter")
	}
	if !skillNamePattern.MatchString(name) {
		return "", "", "", fmt.Errorf("invalid skill name %q", name)
	}
	if description == "" || len(description) > 1000 {
		return "", "", "", fmt.Errorf("description is required and must not exceed 1000 bytes")
	}
	bodyLines := []string{}
	for scanner.Scan() {
		bodyLines = append(bodyLines, scanner.Text())
	}
	body := strings.TrimSpace(strings.Join(bodyLines, "\n"))
	if body == "" {
		return "", "", "", fmt.Errorf("skill instructions are empty")
	}
	return name, description, body, nil
}

func loadReferences(directory string) ([]Reference, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read references: %w", err)
	}
	result := make([]Reference, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil, fmt.Errorf("references may contain only regular .md files: %s", entry.Name())
		}
		payload, digest, err := readRegularFile(filepath.Join(directory, entry.Name()), maxReferenceBytes)
		if err != nil {
			return nil, fmt.Errorf("read reference %s: %w", entry.Name(), err)
		}
		result = append(result, Reference{Name: entry.Name(), Digest: digest, Bytes: int64(len(payload))})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func readRegularFile(path string, limit int64) ([]byte, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("not a regular file")
	}
	if info.Size() > limit {
		return nil, "", fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(payload)) > limit {
		return nil, "", fmt.Errorf("file exceeds %d-byte limit", limit)
	}
	sum := sha256.Sum256(payload)
	return payload, hex.EncodeToString(sum[:]), nil
}
