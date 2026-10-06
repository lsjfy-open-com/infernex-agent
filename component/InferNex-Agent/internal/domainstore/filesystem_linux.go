//go:build linux

package domainstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

var errLockBusy = errors.New("writer lock wait expired")

type linuxOps struct{}

func productionOps() platformOps { return linuxOps{} }

func (linuxOps) CheckFilesystem(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	if uint64(stat.Type) == uint64(unix.NFS_SUPER_MAGIC) {
		return fmt.Errorf("%w: NFS state directories", errUnsupported)
	}
	return nil
}

func (linuxOps) Lock(ctx context.Context, file *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if time.Now().After(deadline) {
			return errLockBusy
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (linuxOps) Unlock(file *os.File) error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }

func (linuxOps) WriteFile(path string, contents []byte, ownerUID int) error {
	return writeNewFile(path, contents, ownerUID)
}

func (linuxOps) RenameNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return errUnsupported
	}
	return err
}

func (linuxOps) SyncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}
