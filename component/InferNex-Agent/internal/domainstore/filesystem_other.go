//go:build !linux

package domainstore

import (
	"context"
	"errors"
	"os"
	"time"
)

var errLockBusy = errors.New("writer lock wait expired")

type unsupportedOps struct{}

func productionOps() platformOps                                           { return unsupportedOps{} }
func (unsupportedOps) CheckFilesystem(string) error                        { return errUnsupported }
func (unsupportedOps) Lock(context.Context, *os.File, time.Duration) error { return errUnsupported }
func (unsupportedOps) Unlock(*os.File) error                               { return nil }
func (unsupportedOps) WriteFile(string, []byte, int) error                 { return errUnsupported }
func (unsupportedOps) RenameNoReplace(string, string) error                { return errUnsupported }
func (unsupportedOps) SyncDir(string) error                                { return errUnsupported }
