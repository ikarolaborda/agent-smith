//go:build !darwin && !linux

package llamacpp

import (
	"context"
	"errors"
	"os"
)

type fileLock struct{}

func acquireFileLock(context.Context, string) (*fileLock, error) {
	return nil, errors.New("llamacpp: safe cross-process file locking is unsupported on this platform")
}

func (*fileLock) File() *os.File { return nil }
func (*fileLock) Close() error   { return nil }
