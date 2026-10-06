package domainstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"golang.org/x/sys/unix"
)

var (
	errUnsupported = errors.New("secure local filesystem primitive is unsupported")
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type platformOps interface {
	CheckFilesystem(path string) error
	Lock(ctx context.Context, file *os.File, wait time.Duration) error
	Unlock(file *os.File) error
	WriteFile(path string, contents []byte, ownerUID int) error
	RenameNoReplace(oldPath, newPath string) error
	SyncDir(path string) error
}

func (s *Store) initializeScope() error {
	path := filepath.Join(s.root, "scope.json")
	existing, err := readRegular(path, s.ownerUID, maxManifestBytes)
	if err == nil {
		var sf scopeFile
		if decodeErr := decodeStrict(existing, &sf); decodeErr != nil || sf != (scopeFile{SchemaVersion: storeSchema, Scope: s.scope, OwnerUID: s.ownerUID}) {
			if decodeErr == nil {
				decodeErr = errors.New("existing scope binding differs")
			}
			return storeError(CodeConflict, "init", decodeErr)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return storeError(CodeStorage, "init", err)
	}
	b, err := json.Marshal(scopeFile{SchemaVersion: storeSchema, Scope: s.scope, OwnerUID: s.ownerUID})
	if err != nil {
		return storeError(CodeStorage, "init", err)
	}
	if err = s.ops.WriteFile(path, b, s.ownerUID); err != nil {
		if errors.Is(err, os.ErrExist) {
			return storeError(CodeConflict, "init", errors.New("scope binding was created concurrently"))
		}
		return storeError(CodeStorage, "init", err)
	}
	return nil
}

func (s *Store) ensureLayout() error {
	for _, path := range []string{filepath.Join(s.root, "records"), filepath.Join(s.root, "records", string(domain.KindEnvironment)), filepath.Join(s.root, "records", string(domain.KindInventorySnapshot))} {
		if err := ensurePrivateDir(path, s.ownerUID); err != nil {
			return storeError(CodeStorage, "init", err)
		}
	}
	lock, err := openLockFile(filepath.Join(s.root, ".writer.lock"), s.ownerUID)
	if err != nil {
		return storeError(CodeStorage, "init", err)
	}
	if err = lock.Close(); err != nil {
		return storeError(CodeStorage, "init", err)
	}
	if err = s.ops.SyncDir(s.root); err != nil {
		return classifyPlatform("sync store root", err)
	}
	return nil
}

func (s *Store) publish(ctx context.Context, record domain.Record, connection *Connection, expectedRevision int64) (domain.RecordRef, error) {
	ref := record.Reference()
	if err := s.authorize(ref.TenantScope); err != nil {
		return domain.RecordRef{}, err
	}
	if err := validateRef(ref); err != nil {
		return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", err)
	}
	if err := domain.VerifyRecord(record); err != nil {
		return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", err)
	}
	lock, err := openLockFile(filepath.Join(s.root, ".writer.lock"), s.ownerUID)
	if err != nil {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	defer lock.Close()
	if err = s.ops.Lock(ctx, lock, lockWait); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.RecordRef{}, storeError(CodeCanceled, "publish lock", err)
		}
		if errors.Is(err, errLockBusy) {
			return domain.RecordRef{}, storeError(CodeBusy, "publish lock", err)
		}
		return domain.RecordRef{}, classifyPlatform("publish lock", err)
	}
	defer s.ops.Unlock(lock)
	if err = contextErr(ctx); err != nil {
		return domain.RecordRef{}, err
	}

	if ref.Kind == domain.KindEnvironment {
		if connection == nil {
			return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", errors.New("Environment connection is required"))
		}
		if err = s.checkEnvironmentCAS(ref, expectedRevision); err != nil {
			return domain.RecordRef{}, err
		}
	} else {
		if connection != nil {
			return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", errors.New("snapshot cannot contain a connection"))
		}
		if err = s.checkSnapshotEnvironment(record.(*domain.InventorySnapshot)); err != nil {
			return domain.RecordRef{}, err
		}
		if existing, _, readErr := s.readBundle(ref); readErr == nil {
			existingSnapshot, ok := existing.(*domain.InventorySnapshot)
			incoming := record.(*domain.InventorySnapshot)
			if ok && existingSnapshot.Digest == incoming.Digest {
				return ref, nil
			}
			return domain.RecordRef{}, storeError(CodeConflict, "publish", errors.New("snapshot ID already has different content"))
		} else if !IsCode(readErr, CodeNotFound) {
			return domain.RecordRef{}, readErr
		}
	}

	recordBytes, err := json.Marshal(record)
	if err != nil {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	if len(recordBytes) > domain.MaxSnapshotBytes {
		return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", errors.New("record size limit exceeded"))
	}
	files := map[string][]byte{"record.json": recordBytes}
	if connection != nil {
		connectionBytes, marshalErr := json.Marshal(connection)
		if marshalErr != nil || len(connectionBytes) > maxConnectionBytes {
			if marshalErr == nil {
				marshalErr = errors.New("connection size limit exceeded")
			}
			return domain.RecordRef{}, storeError(CodeInvalidArgument, "publish", marshalErr)
		}
		files["connection.json"] = connectionBytes
	}
	manifestBytes, err := makeManifest(files)
	if err != nil {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	files["manifest.json"] = manifestBytes

	idDir := filepath.Join(s.root, "records", string(ref.Kind), ref.ID)
	if err = ensurePrivateDir(idDir, s.ownerUID); err != nil {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	if err = s.ops.SyncDir(filepath.Dir(idDir)); err != nil {
		return domain.RecordRef{}, classifyPlatform("sync record kind", err)
	}
	pending, err := os.MkdirTemp(idDir, ".domain-pending-")
	if err != nil {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	if err = os.Chmod(pending, 0700); err != nil {
		os.RemoveAll(pending)
		return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
	}
	removePending := true
	defer func() {
		if removePending {
			_ = os.RemoveAll(pending)
		}
	}()
	for _, name := range []string{"record.json", "connection.json", "manifest.json"} {
		contents, ok := files[name]
		if !ok {
			continue
		}
		if err = contextErr(ctx); err != nil {
			return domain.RecordRef{}, err
		}
		if err = s.ops.WriteFile(filepath.Join(pending, name), contents, s.ownerUID); err != nil {
			return domain.RecordRef{}, storeError(CodeStorage, "publish", err)
		}
	}
	if err = s.ops.SyncDir(pending); err != nil {
		return domain.RecordRef{}, classifyPlatform("sync pending", err)
	}
	if err = contextErr(ctx); err != nil {
		return domain.RecordRef{}, err
	}
	final := filepath.Join(idDir, strconv.FormatInt(ref.Revision, 10))
	if _, statErr := os.Lstat(final); statErr == nil {
		return domain.RecordRef{}, storeError(CodeConflict, "publish", errors.New("record revision already exists"))
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return domain.RecordRef{}, storeError(CodeStorage, "publish", statErr)
	}
	// From this call onward cancellation cannot prove that publication did not
	// occur. RenameNoReplace is the transaction's atomic publication point.
	if err = s.ops.RenameNoReplace(pending, final); err != nil {
		if errors.Is(err, errUnsupported) {
			return domain.RecordRef{}, storeError(CodeUnsupported, "publish rename", err)
		}
		if errors.Is(err, os.ErrExist) {
			return domain.RecordRef{}, storeError(CodeConflict, "publish rename", err)
		}
		return domain.RecordRef{}, storeError(CodeStorage, "publish rename", err)
	}
	removePending = false
	if err = s.ops.SyncDir(idDir); err != nil {
		return ref, &Error{Code: CodeOutcomeUnknown, Op: "sync published parent", Err: err, Committed: true}
	}
	return ref, nil
}

func (s *Store) checkEnvironmentCAS(ref domain.RecordRef, expected int64) error {
	latest, found, err := s.latestEnvironment(ref.ID)
	if err != nil {
		return err
	}
	if expected == 0 {
		if found || ref.Revision != 1 {
			return storeError(CodeConflict, "environment CAS", errors.New("Environment already exists or initial revision is not 1"))
		}
		return nil
	}
	if !found || latest.Revision != expected || ref.Revision != expected+1 {
		return storeError(CodeConflict, "environment CAS", errors.New("expected revision is stale"))
	}
	if _, _, err = s.readBundle(latest); err != nil {
		return err
	}
	return nil
}

func (s *Store) checkSnapshotEnvironment(snapshot *domain.InventorySnapshot) error {
	ref := snapshot.Spec.EnvironmentRef
	if err := domain.AuthorizeScope(s.scope, ref.TenantScope); err != nil {
		return storeError(CodeUnauthorized, "snapshot environment", err)
	}
	latest, found, err := s.latestEnvironment(ref.ID)
	if err != nil {
		return err
	}
	if !found || latest != ref {
		return storeError(CodeConflict, "snapshot environment", errors.New("Environment revision changed before save"))
	}
	record, connection, err := s.readBundle(latest)
	if err != nil {
		return err
	}
	env := record.(*domain.Environment)
	if !env.Spec.Enabled {
		return storeError(CodeUnauthorized, "snapshot environment", errors.New("Environment is disabled"))
	}
	for _, entity := range snapshot.Spec.Entities {
		identity := entity.Identity
		if identity.Runtime != env.Spec.Runtime {
			return storeError(CodeConflict, "snapshot environment", errors.New("entity runtime differs from registered Environment"))
		}
		switch env.Spec.Runtime {
		case domain.RuntimeDocker:
			if identity.HostID != env.Spec.HostID || connection == nil || identity.DaemonID != connection.ExpectedDaemonID {
				return storeError(CodeConflict, "snapshot environment", errors.New("entity Docker identity differs from registered Environment"))
			}
		case domain.RuntimeKubernetes:
			if identity.ClusterID != env.Spec.ClusterID {
				return storeError(CodeConflict, "snapshot environment", errors.New("entity Kubernetes identity differs from registered Environment"))
			}
		}
	}
	if snapshot.Spec.PreviousSnapshotRef != nil {
		previous, _, readErr := s.readBundle(*snapshot.Spec.PreviousSnapshotRef)
		if readErr != nil {
			return storeError(CodeConflict, "snapshot lineage", errors.New("previous snapshot is unavailable or corrupt"))
		}
		previousSnapshot, ok := previous.(*domain.InventorySnapshot)
		if !ok || previousSnapshot.Spec.EnvironmentRef.ID != ref.ID {
			return storeError(CodeConflict, "snapshot lineage", errors.New("previous snapshot belongs to another Environment"))
		}
	}
	return nil
}

func (s *Store) latestEnvironment(id string) (domain.RecordRef, bool, error) {
	if !validUUID(id) {
		return domain.RecordRef{}, false, storeError(CodeInvalidArgument, "latest environment", errors.New("environment ID is invalid"))
	}
	dir := filepath.Join(s.root, "records", string(domain.KindEnvironment), id)
	if err := verifyPrivateDir(dir, s.ownerUID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.RecordRef{}, false, nil
		}
		return domain.RecordRef{}, false, storeError(CodeCorrupt, "latest environment", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return domain.RecordRef{}, false, storeError(CodeStorage, "latest environment", err)
	}
	var latest int64
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".domain-pending-") {
			continue
		}
		revision, parseErr := strconv.ParseInt(entry.Name(), 10, 64)
		if parseErr != nil || revision < 1 || !entry.IsDir() {
			continue
		}
		if revision > latest {
			latest = revision
		}
	}
	if latest == 0 {
		return domain.RecordRef{}, false, nil
	}
	return domain.RecordRef{TenantScope: s.scope, Kind: domain.KindEnvironment, ID: id, Revision: latest}, true, nil
}

func (s *Store) readBundle(ref domain.RecordRef) (domain.Record, *Connection, error) {
	dir := s.recordDir(ref)
	if err := s.verifyRecordDirectories(ref); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, storeError(CodeNotFound, "read", errors.New("record was not found"))
		}
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, storeError(CodeNotFound, "read", errors.New("record was not found"))
	}
	if err != nil {
		return nil, nil, storeError(CodeStorage, "read", err)
	}
	expected := []string{"manifest.json", "record.json"}
	if ref.Kind == domain.KindEnvironment {
		expected = []string{"connection.json", "manifest.json", "record.json"}
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil, nil, storeError(CodeCorrupt, "read", errors.New("bundle contains a non-regular file"))
		}
		actual = append(actual, entry.Name())
	}
	sort.Strings(actual)
	if !equalStrings(actual, expected) {
		return nil, nil, storeError(CodeCorrupt, "read", errors.New("bundle file list is invalid"))
	}
	manifestBytes, err := readRegular(filepath.Join(dir, "manifest.json"), s.ownerUID, maxManifestBytes)
	if err != nil {
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	var m manifest
	if err = decodeStrict(manifestBytes, &m); err != nil || m.SchemaVersion != storeSchema {
		if err == nil {
			err = errors.New("manifest schema is invalid")
		}
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	wantManifestFiles := expected[:0]
	for _, name := range expected {
		if name != "manifest.json" {
			wantManifestFiles = append(wantManifestFiles, name)
		}
	}
	if len(m.Files) != len(wantManifestFiles) {
		return nil, nil, storeError(CodeCorrupt, "read", errors.New("manifest file count is invalid"))
	}
	contents := map[string][]byte{}
	for i, name := range wantManifestFiles {
		if m.Files[i].Name != name {
			return nil, nil, storeError(CodeCorrupt, "read", errors.New("manifest file order or name is invalid"))
		}
		limit := int64(domain.MaxSnapshotBytes)
		if name == "connection.json" {
			limit = maxConnectionBytes
		}
		b, readErr := readRegular(filepath.Join(dir, name), s.ownerUID, limit)
		if readErr != nil || digestBytes(b) != m.Files[i].SHA256 {
			if readErr == nil {
				readErr = errors.New("manifest digest mismatch")
			}
			return nil, nil, storeError(CodeCorrupt, "read", readErr)
		}
		contents[name] = b
	}
	record, err := domain.DecodeRecord(contents["record.json"])
	if err != nil || record.Reference() != ref {
		if err == nil {
			err = errors.New("record reference does not match its path")
		}
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	if err = domain.AuthorizeScope(s.scope, record.Reference().TenantScope); err != nil {
		return nil, nil, storeError(CodeUnauthorized, "read", err)
	}
	if ref.Kind == domain.KindInventorySnapshot {
		if _, ok := record.(*domain.InventorySnapshot); !ok {
			return nil, nil, storeError(CodeCorrupt, "read", errors.New("snapshot path contains another kind"))
		}
		return record, nil, nil
	}
	env, ok := record.(*domain.Environment)
	if !ok {
		return nil, nil, storeError(CodeCorrupt, "read", errors.New("Environment path contains another kind"))
	}
	connection, err := DecodeConnection(contents["connection.json"])
	if err != nil {
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	expectedFingerprint, err := connectionFingerprint(connection)
	if err != nil || env.Spec.Runtime != connection.Runtime || env.Spec.IdentityFingerprint != expectedFingerprint || env.Spec.HostID != connection.HostID || env.Spec.ClusterID != connection.ClusterID || env.Spec.NetworkPolicy != connection.NetworkPolicy || !equalStrings(env.Spec.AllowedNamespaces, connection.Namespaces) {
		if err == nil {
			err = errors.New("connection does not match Environment")
		}
		return nil, nil, storeError(CodeCorrupt, "read", err)
	}
	return record, &connection, nil
}

func (s *Store) verifyRecordDirectories(ref domain.RecordRef) error {
	paths := []string{
		s.root,
		filepath.Join(s.root, "records"),
		filepath.Join(s.root, "records", string(ref.Kind)),
		filepath.Join(s.root, "records", string(ref.Kind), ref.ID),
		s.recordDir(ref),
	}
	for _, path := range paths {
		if err := verifyPrivateDir(path, s.ownerUID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) scanRefs(kind domain.Kind) ([]domain.RecordRef, error) {
	kinds := []domain.Kind{domain.KindEnvironment, domain.KindInventorySnapshot}
	if kind != "" {
		kinds = []domain.Kind{kind}
	}
	refs := []domain.RecordRef{}
	for _, currentKind := range kinds {
		kindDir := filepath.Join(s.root, "records", string(currentKind))
		if err := verifyPrivateDir(kindDir, s.ownerUID); err != nil {
			return nil, storeError(CodeCorrupt, "list", err)
		}
		ids, err := os.ReadDir(kindDir)
		if err != nil {
			return nil, storeError(CodeStorage, "list", err)
		}
		for _, id := range ids {
			if !id.IsDir() || !validUUID(id.Name()) {
				continue
			}
			revisions, readErr := os.ReadDir(filepath.Join(s.root, "records", string(currentKind), id.Name()))
			if readErr != nil {
				return nil, storeError(CodeStorage, "list", readErr)
			}
			for _, revision := range revisions {
				if strings.HasPrefix(revision.Name(), ".domain-pending-") {
					continue
				}
				n, parseErr := strconv.ParseInt(revision.Name(), 10, 64)
				if parseErr != nil || n < 1 {
					continue
				}
				refs = append(refs, domain.RecordRef{TenantScope: s.scope, Kind: currentKind, ID: id.Name(), Revision: n})
			}
		}
	}
	sortRefs(refs)
	return refs, nil
}

func (s *Store) recordDir(ref domain.RecordRef) string {
	return filepath.Join(s.root, "records", string(ref.Kind), ref.ID, strconv.FormatInt(ref.Revision, 10))
}

func createStateRoot(root string, ownerUID int) error {
	if err := rejectSymlinkPath(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storeError(CodeStorage, "init", err)
	}
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return storeError(CodeStorage, "init", err)
	}
	if err := verifyPrivateDir(root, ownerUID); err != nil {
		return storeError(CodeStorage, "init", err)
	}
	return nil
}

func cleanAbsolute(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("state directory must be an absolute clean path")
	}
	return path, nil
}

func ensurePrivateDir(path string, ownerUID int) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return verifyPrivateDir(path, ownerUID)
}

func verifyPrivateDir(path string, ownerUID int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0700 || fileUID(info) != ownerUID {
		return fmt.Errorf("unsafe private directory %q", path)
	}
	return nil
}

func verifyTree(root string, ownerUID int) error {
	if err := rejectSymlinkPath(root); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || fileUID(info) != ownerUID {
			return fmt.Errorf("unsafe owner or symlink at %q", path)
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0700 {
				return fmt.Errorf("directory is not 0700: %q", path)
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("file is not a private regular file: %q", path)
		}
		return nil
	})
}

func rejectSymlinkPath(path string) error {
	current := string(filepath.Separator)
	volume := filepath.VolumeName(path)
	if volume != "" {
		current = volume + string(filepath.Separator)
	}
	trimmed := strings.TrimPrefix(path, current)
	for _, part := range strings.Split(trimmed, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component %q", current)
		}
	}
	return nil
}

func fileUID(info os.FileInfo) int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}

func openLockFile(path string, ownerUID int) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot create lock file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || fileUID(info) != ownerUID {
		file.Close()
		if err == nil {
			err = errors.New("writer lock is unsafe")
		}
		return nil, err
	}
	return file, nil
}

func writeNewFile(path string, contents []byte, ownerUID int) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("cannot create file")
	}
	written := 0
	for written < len(contents) {
		n, writeErr := file.Write(contents[written:])
		if writeErr != nil {
			file.Close()
			return writeErr
		}
		written += n
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || fileUID(info) != ownerUID || info.Mode().Perm() != 0600 {
		if err == nil {
			err = errors.New("new file ownership or mode is unsafe")
		}
		return err
	}
	return nil
}

func readRegular(path string, ownerUID int, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || fileUID(before) != ownerUID || before.Size() > limit {
		return nil, errors.New("file is unsafe or oversized")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot open file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Size() > limit {
		return nil, errors.New("file changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(b)) > limit {
		if err == nil {
			err = errors.New("file size limit exceeded")
		}
		return nil, err
	}
	return b, nil
}

func makeManifest(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	m := manifest{SchemaVersion: storeSchema, Files: make([]manifestFile, 0, len(names))}
	for _, name := range names {
		m.Files = append(m.Files, manifestFile{Name: name, SHA256: digestBytes(files[name])})
	}
	return json.Marshal(m)
}

func digestBytes(contents []byte) string {
	sum := sha256.Sum256(contents)
	return domain.DigestPrefix + hex.EncodeToString(sum[:])
}

func validateRef(ref domain.RecordRef) error {
	if ref.TenantScope == "" || (ref.Kind != domain.KindEnvironment && ref.Kind != domain.KindInventorySnapshot) || !validUUID(ref.ID) || ref.Revision < 1 || ref.Revision > domain.MaxSafeInteger {
		return errors.New("record reference is invalid")
	}
	return nil
}

func validUUID(id string) bool { return uuidPattern.MatchString(id) }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
