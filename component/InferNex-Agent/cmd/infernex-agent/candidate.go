/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	defaultCandidateTarget = "/opt/infernex-agent/bin/infernex-agent"
	defaultCandidateState  = "/var/lib/infernex-agent/candidates"
	maxCandidateBytes      = 512 * 1024 * 1024
	maxCommandOutputBytes  = 64 * 1024
)

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

var candidateRestartAndWait = restartAndWait

type candidateMetadata struct {
	Path    string      `json:"path"`
	Size    int64       `json:"size"`
	SHA256  string      `json:"sha256"`
	Machine string      `json:"machine"`
	Static  bool        `json:"static"`
	Build   versionInfo `json:"build"`
}

type candidateRecord struct {
	ID             string `json:"id"`
	Target         string `json:"target"`
	PreviousFile   string `json:"previousFile"`
	PreviousSHA256 string `json:"previousSha256"`
	CandidateSHA   string `json:"candidateSha256"`
	CandidateBuild string `json:"candidateVersion"`
	Service        string `json:"service"`
	HealthURL      string `json:"healthUrl"`
	Status         string `json:"status"`
	CreatedUTC     string `json:"createdUtc"`
	UpdatedUTC     string `json:"updatedUtc"`
	Failure        string `json:"failure,omitempty"`
}

type candidateActivation struct {
	service   string
	healthURL string
	timeout   time.Duration
	restart   bool
}

func runCandidate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: infernex-agent candidate <verify|apply|rollback> [options]")
	}
	switch args[0] {
	case "verify":
		return runCandidateVerify(args[1:])
	case "apply":
		return runCandidateApply(args[1:])
	case "rollback":
		return runCandidateRollback(args[1:])
	default:
		return fmt.Errorf("unknown candidate action %q; expected verify, apply, or rollback", args[0])
	}
}

func runCandidateVerify(args []string) error {
	flags := flag.NewFlagSet("infernex-agent candidate verify", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	file := flags.String("file", "", "candidate Linux binary")
	expectedSHA := flags.String("expect-sha256", "", "optional expected lowercase SHA256")
	jsonOutput := flags.Bool("json", false, "print machine-readable metadata")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	metadata, err := inspectCandidate(*file, *expectedSHA)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(metadata)
	}
	fmt.Fprintf(
		os.Stdout,
		"candidate verified: version=%s commit=%s arch=%s static=%t sha256=%s\n",
		metadata.Build.Version,
		metadata.Build.Commit,
		metadata.Build.Arch,
		metadata.Static,
		metadata.SHA256,
	)
	return nil
}

func runCandidateApply(args []string) error {
	flags := flag.NewFlagSet("infernex-agent candidate apply", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	file := flags.String("file", "", "candidate Linux binary")
	expectedSHA := flags.String("expect-sha256", "", "optional expected lowercase SHA256")
	target := flags.String("target", defaultCandidateTarget, "installed Agent binary to replace")
	stateDir := flags.String("state-dir", defaultCandidateState, "protected candidate backup directory")
	service := flags.String("service", "infernex-agent.service", "systemd service to restart")
	healthURL := flags.String("health-url", "http://127.0.0.1:8080/healthz", "health endpoint required after restart")
	timeout := flags.Duration("timeout", 60*time.Second, "activation health timeout")
	noRestart := flags.Bool("no-restart", false, "replace the binary without restarting or health checking")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	metadata, err := inspectCandidate(*file, *expectedSHA)
	if err != nil {
		return err
	}
	activation, err := validateCandidateActivation(*service, *healthURL, *timeout, !*noRestart)
	if err != nil {
		return err
	}
	if activation.restart && runtime.GOOS == "linux" && os.Geteuid() != 0 {
		return fmt.Errorf("candidate activation requires root; rerun with sudo")
	}
	record, err := applyCandidate(context.Background(), metadata, *target, *stateDir, activation)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		os.Stdout,
		"candidate %s: version=%s sha256=%s backup=%s\n",
		record.Status,
		record.CandidateBuild,
		record.CandidateSHA,
		filepath.Join(*stateDir, record.PreviousFile),
	)
	return nil
}

func runCandidateRollback(args []string) error {
	flags := flag.NewFlagSet("infernex-agent candidate rollback", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	stateDir := flags.String("state-dir", defaultCandidateState, "protected candidate backup directory")
	target := flags.String("target", defaultCandidateTarget, "installed Agent binary to restore")
	service := flags.String("service", "infernex-agent.service", "systemd service to restart")
	healthURL := flags.String("health-url", "http://127.0.0.1:8080/healthz", "health endpoint required after restart")
	timeout := flags.Duration("timeout", 60*time.Second, "rollback health timeout")
	noRestart := flags.Bool("no-restart", false, "restore the binary without restarting or health checking")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	activation, err := validateCandidateActivation(*service, *healthURL, *timeout, !*noRestart)
	if err != nil {
		return err
	}
	if activation.restart && runtime.GOOS == "linux" && os.Geteuid() != 0 {
		return fmt.Errorf("candidate rollback requires root; rerun with sudo")
	}
	record, err := rollbackCandidate(context.Background(), *target, *stateDir, activation)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "candidate rolled back: restored sha256=%s\n", record.PreviousSHA256)
	return nil
}

func inspectCandidate(path, expectedSHA string) (candidateMetadata, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return candidateMetadata{}, fmt.Errorf("--file is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return candidateMetadata{}, fmt.Errorf("resolve candidate path: %w", err)
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return candidateMetadata{}, fmt.Errorf("stat candidate: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return candidateMetadata{}, fmt.Errorf("candidate must be a regular file, not a symlink")
	}
	if info.Size() <= 0 || info.Size() > maxCandidateBytes {
		return candidateMetadata{}, fmt.Errorf("candidate size must be between 1 and %d bytes", maxCandidateBytes)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return candidateMetadata{}, fmt.Errorf("candidate is not executable; run chmod +x %s", absolutePath)
	}

	digest, err := fileSHA256(absolutePath)
	if err != nil {
		return candidateMetadata{}, err
	}
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	if expectedSHA != "" {
		if len(expectedSHA) != sha256.Size*2 || expectedSHA != digest {
			return candidateMetadata{}, fmt.Errorf("candidate SHA256 mismatch: got %s", digest)
		}
	}

	elfFile, err := elf.Open(absolutePath)
	if err != nil {
		return candidateMetadata{}, fmt.Errorf("candidate is not a supported Linux ELF binary: %w", err)
	}
	defer elfFile.Close()
	wantedMachine, err := elfMachineForArchitecture(runtime.GOARCH)
	if err != nil {
		return candidateMetadata{}, err
	}
	if elfFile.Machine != wantedMachine {
		return candidateMetadata{}, fmt.Errorf("candidate architecture is %s; this host requires %s", elfFile.Machine, runtime.GOARCH)
	}
	static := true
	for _, program := range elfFile.Progs {
		if program.Type == elf.PT_INTERP {
			static = false
			break
		}
	}
	if !static {
		return candidateMetadata{}, fmt.Errorf("candidate has a dynamic interpreter; use the official CGO_ENABLED=0 binary")
	}

	build, err := candidateVersion(absolutePath)
	if err != nil {
		return candidateMetadata{}, err
	}
	if build.OS != "linux" || build.Arch != runtime.GOARCH || build.CGO != "0" {
		return candidateMetadata{}, fmt.Errorf(
			"candidate build metadata is incompatible: os=%s arch=%s cgo=%s",
			build.OS,
			build.Arch,
			build.CGO,
		)
	}
	return candidateMetadata{
		Path:    absolutePath,
		Size:    info.Size(),
		SHA256:  digest,
		Machine: elfFile.Machine.String(),
		Static:  static,
		Build:   build,
	}, nil
}

func elfMachineForArchitecture(architecture string) (elf.Machine, error) {
	switch architecture {
	case "amd64":
		return elf.EM_X86_64, nil
	case "arm64":
		return elf.EM_AARCH64, nil
	default:
		return elf.EM_NONE, fmt.Errorf("candidate workflow does not support host architecture %s", architecture)
	}
}

func candidateVersion(path string) (versionInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "version", "--json")
	output := &limitedBuffer{limit: maxCommandOutputBytes}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return versionInfo{}, fmt.Errorf("candidate version probe failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	result := versionInfo{}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return versionInfo{}, fmt.Errorf("decode candidate version metadata: %w", err)
	}
	if strings.TrimSpace(result.Version) == "" {
		return versionInfo{}, fmt.Errorf("candidate version metadata is empty")
	}
	return result, nil
}

func validateCandidateActivation(service, healthURL string, timeout time.Duration, restart bool) (candidateActivation, error) {
	if timeout < time.Second || timeout > 10*time.Minute {
		return candidateActivation{}, fmt.Errorf("--timeout must be between 1s and 10m")
	}
	service = strings.TrimSpace(service)
	if restart && !serviceNamePattern.MatchString(service) {
		return candidateActivation{}, fmt.Errorf("invalid systemd service name %q", service)
	}
	if restart {
		parsed, err := url.Parse(healthURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return candidateActivation{}, fmt.Errorf("--health-url must be an http(s) URL without credentials")
		}
	}
	return candidateActivation{service: service, healthURL: healthURL, timeout: timeout, restart: restart}, nil
}

func applyCandidate(
	ctx context.Context,
	metadata candidateMetadata,
	target string,
	stateDir string,
	activation candidateActivation,
) (candidateRecord, error) {
	target, stateDir, err := prepareCandidatePaths(target, stateDir)
	if err != nil {
		return candidateRecord{}, err
	}
	unlock, err := candidateLock(stateDir)
	if err != nil {
		return candidateRecord{}, err
	}
	defer unlock()

	previousInfo, err := os.Lstat(target)
	if err != nil {
		return candidateRecord{}, fmt.Errorf("stat installed Agent binary: %w", err)
	}
	if previousInfo.Mode()&os.ModeSymlink != 0 || !previousInfo.Mode().IsRegular() {
		return candidateRecord{}, fmt.Errorf("installed Agent target must be a regular file, not a symlink")
	}
	if previousInfo.Size() <= 0 || previousInfo.Size() > maxCandidateBytes {
		return candidateRecord{}, fmt.Errorf("installed Agent size must be between 1 and %d bytes", maxCandidateBytes)
	}
	previousSHA, err := fileSHA256(target)
	if err != nil {
		return candidateRecord{}, err
	}
	if previousSHA == metadata.SHA256 {
		return candidateRecord{}, fmt.Errorf("candidate is already installed (sha256 %s)", previousSHA)
	}

	now := time.Now().UTC()
	id := now.Format("20060102T150405.000000000Z")
	previousFile := id + "-previous-" + previousSHA[:12] + ".bin"
	previousPath := filepath.Join(stateDir, previousFile)
	if err := copyFileAtomic(target, previousPath, 0o700); err != nil {
		return candidateRecord{}, fmt.Errorf("backup installed Agent: %w", err)
	}
	backupSHA, err := fileSHA256(previousPath)
	if err != nil || backupSHA != previousSHA {
		return candidateRecord{}, fmt.Errorf("verify installed Agent backup: checksum mismatch")
	}
	record := candidateRecord{
		ID:             id,
		Target:         target,
		PreviousFile:   previousFile,
		PreviousSHA256: previousSHA,
		CandidateSHA:   metadata.SHA256,
		CandidateBuild: metadata.Build.Version,
		Service:        activation.service,
		HealthURL:      activation.healthURL,
		Status:         "prepared",
		CreatedUTC:     now.Format(time.RFC3339Nano),
		UpdatedUTC:     now.Format(time.RFC3339Nano),
	}
	if err := persistCandidateRecord(stateDir, &record); err != nil {
		return candidateRecord{}, err
	}
	if err := copyFileAtomic(metadata.Path, target, 0o755); err != nil {
		return candidateRecord{}, fmt.Errorf("activate candidate binary: %w", err)
	}
	installedSHA, err := fileSHA256(target)
	if err != nil || installedSHA != metadata.SHA256 {
		restoreErr := copyFileAtomic(previousPath, target, 0o755)
		record.Status = "rolled_back"
		record.Failure = "candidate changed while it was being activated"
		if restoreErr != nil {
			record.Status = "rollback_failed"
			record.Failure += "; restoring previous binary failed: " + boundedCandidateFailure(restoreErr)
		}
		record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
		_ = persistCandidateRecord(stateDir, &record)
		if restoreErr != nil {
			return record, fmt.Errorf("candidate checksum changed during activation and previous binary restore failed: %w", restoreErr)
		}
		return record, fmt.Errorf("candidate checksum changed during activation; previous binary restored")
	}
	if !activation.restart {
		record.Status = "staged"
		record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
		return record, persistCandidateRecord(stateDir, &record)
	}

	activationErr := candidateRestartAndWait(ctx, activation)
	if activationErr == nil {
		record.Status = "active"
		record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
		return record, persistCandidateRecord(stateDir, &record)
	}
	restoreErr := copyFileAtomic(previousPath, target, 0o755)
	if restoreErr == nil {
		restoreErr = candidateRestartAndWait(ctx, activation)
	}
	record.Status = "rolled_back"
	record.Failure = boundedCandidateFailure(activationErr)
	if restoreErr != nil {
		record.Status = "rollback_failed"
		record.Failure += "; restoring previous binary also failed: " + boundedCandidateFailure(restoreErr)
	}
	record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	_ = persistCandidateRecord(stateDir, &record)
	if restoreErr != nil {
		return record, fmt.Errorf("candidate activation failed and automatic rollback failed: %v; rollback: %v", activationErr, restoreErr)
	}
	return record, fmt.Errorf("candidate activation failed; previous binary restored automatically: %w", activationErr)
}

func rollbackCandidate(
	ctx context.Context,
	target string,
	stateDir string,
	activation candidateActivation,
) (candidateRecord, error) {
	target, stateDir, err := prepareCandidatePaths(target, stateDir)
	if err != nil {
		return candidateRecord{}, err
	}
	unlock, err := candidateLock(stateDir)
	if err != nil {
		return candidateRecord{}, err
	}
	defer unlock()
	record, err := loadCurrentCandidateRecord(stateDir)
	if err != nil {
		return candidateRecord{}, err
	}
	if record.Status != "active" && record.Status != "staged" {
		return candidateRecord{}, fmt.Errorf("current candidate status %q cannot be rolled back", record.Status)
	}
	if filepath.Clean(record.Target) != target {
		return candidateRecord{}, fmt.Errorf("candidate record target %s does not match %s", record.Target, target)
	}
	if filepath.Base(record.PreviousFile) != record.PreviousFile {
		return candidateRecord{}, fmt.Errorf("candidate record contains an unsafe backup path")
	}
	previousPath := filepath.Join(stateDir, record.PreviousFile)
	previousSHA, err := fileSHA256(previousPath)
	if err != nil {
		return candidateRecord{}, fmt.Errorf("verify previous binary: %w", err)
	}
	if previousSHA != record.PreviousSHA256 {
		return candidateRecord{}, fmt.Errorf("previous binary checksum mismatch")
	}

	currentFile := record.ID + "-rollback-current.bin"
	currentPath := filepath.Join(stateDir, currentFile)
	if err := copyFileAtomic(target, currentPath, 0o700); err != nil {
		return candidateRecord{}, fmt.Errorf("backup current candidate before rollback: %w", err)
	}
	if err := copyFileAtomic(previousPath, target, 0o755); err != nil {
		return candidateRecord{}, fmt.Errorf("restore previous binary: %w", err)
	}
	if activation.restart {
		if rollbackErr := candidateRestartAndWait(ctx, activation); rollbackErr != nil {
			restoreErr := copyFileAtomic(currentPath, target, 0o755)
			if restoreErr == nil {
				restoreErr = candidateRestartAndWait(ctx, activation)
			}
			record.Status = "rollback_failed"
			record.Failure = boundedCandidateFailure(rollbackErr)
			if restoreErr != nil {
				record.Failure += "; restoring candidate also failed: " + boundedCandidateFailure(restoreErr)
			}
			record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
			_ = persistCandidateRecord(stateDir, &record)
			return record, fmt.Errorf("candidate rollback failed: %w", rollbackErr)
		}
	}
	record.Status = "rolled_back"
	record.Failure = ""
	record.UpdatedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	if err := persistCandidateRecord(stateDir, &record); err != nil {
		return record, err
	}
	return record, nil
}

func prepareCandidatePaths(target, stateDir string) (string, string, error) {
	if strings.TrimSpace(target) == "" || strings.TrimSpace(stateDir) == "" {
		return "", "", fmt.Errorf("candidate target and state directory are required")
	}
	target, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return "", "", fmt.Errorf("resolve candidate target: %w", err)
	}
	stateDir, err = filepath.Abs(strings.TrimSpace(stateDir))
	if err != nil {
		return "", "", fmt.Errorf("resolve candidate state directory: %w", err)
	}
	if target == stateDir || strings.HasPrefix(target, stateDir+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("candidate target must not be inside its backup directory")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create candidate state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return "", "", fmt.Errorf("protect candidate state directory: %w", err)
	}
	return target, stateDir, nil
}

func candidateLock(stateDir string) (func(), error) {
	path := filepath.Join(stateDir, ".operation.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("another candidate operation is active; remove %s only after confirming no operation is running", path)
		}
		return nil, fmt.Errorf("create candidate operation lock: %w", err)
	}
	_, _ = fmt.Fprintf(file, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	_ = file.Close()
	return func() { _ = os.Remove(path) }, nil
}

func persistCandidateRecord(stateDir string, record *candidateRecord) error {
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode candidate record: %w", err)
	}
	payload = append(payload, '\n')
	recordPath := filepath.Join(stateDir, record.ID+".json")
	if err := writeFileAtomic(recordPath, payload, 0o600); err != nil {
		return fmt.Errorf("write candidate record: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(stateDir, "current.json"), payload, 0o600); err != nil {
		return fmt.Errorf("write current candidate record: %w", err)
	}
	return nil
}

func loadCurrentCandidateRecord(stateDir string) (candidateRecord, error) {
	payload, err := os.ReadFile(filepath.Join(stateDir, "current.json"))
	if err != nil {
		return candidateRecord{}, fmt.Errorf("read current candidate record: %w", err)
	}
	record := candidateRecord{}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return candidateRecord{}, fmt.Errorf("decode current candidate record: %w", err)
	}
	return record, nil
}

func restartAndWait(ctx context.Context, activation candidateActivation) error {
	restartCtx, cancel := context.WithTimeout(ctx, activation.timeout)
	defer cancel()
	command := exec.CommandContext(restartCtx, "systemctl", "restart", activation.service)
	output := &limitedBuffer{limit: maxCommandOutputBytes}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("restart %s: %w: %s", activation.service, err, strings.TrimSpace(output.String()))
	}
	client := &http.Client{Timeout: 3 * time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(restartCtx, http.MethodGet, activation.healthURL, nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
		} else {
			lastErr = requestErr
		}
		select {
		case <-restartCtx.Done():
			return fmt.Errorf("health check %s did not pass: %w", activation.healthURL, lastErr)
		case <-ticker.C:
		}
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for checksum: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxCandidateBytes+1))
	if err != nil {
		return "", fmt.Errorf("checksum %s: %w", path, err)
	}
	if written > maxCandidateBytes {
		return "", fmt.Errorf("checksum %s: file exceeds %d bytes", path, maxCandidateBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFileAtomic(source, target string, mode os.FileMode) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".infernex-agent-candidate-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	written, err := io.Copy(temporary, io.LimitReader(sourceFile, maxCandidateBytes+1))
	if err != nil {
		return err
	}
	if written > maxCandidateBytes {
		return fmt.Errorf("file exceeds %d bytes", maxCandidateBytes)
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return err
	}
	committed = true
	return nil
}

func writeFileAtomic(path string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".infernex-agent-record-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func boundedCandidateFailure(err error) string {
	if err == nil {
		return ""
	}
	runes := []rune(strings.TrimSpace(err.Error()))
	if len(runes) > 2048 {
		runes = runes[:2048]
	}
	return string(runes)
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *limitedBuffer) Write(payload []byte) (int, error) {
	original := len(payload)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(payload) > remaining {
			payload = payload[:remaining]
		}
		_, _ = buffer.buffer.Write(payload)
	}
	return original, nil
}

func (buffer *limitedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *limitedBuffer) String() string { return buffer.buffer.String() }
