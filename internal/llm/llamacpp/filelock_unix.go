//go:build darwin || linux

package llamacpp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// fileLock is an advisory, cross-process exclusive lock. The lock file is
// deliberately never removed: unlinking it while another process is waiting
// could create two independently locked inodes for the same logical resource.
type fileLock struct {
	file *os.File
}

// acquireFileLock opens path without following a terminal symlink and waits for
// an exclusive flock. Non-blocking attempts keep acquisition cancellable.
func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("llamacpp: open lock file %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("llamacpp: open lock file %q: invalid descriptor", path)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("llamacpp: secure lock file %q: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("llamacpp: inspect lock file %q: %w", path, err)
		}
		return nil, fmt.Errorf("llamacpp: lock path %q is not a regular file", path)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &fileLock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("llamacpp: acquire lock %q: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("llamacpp: wait for lock %q: %w", path, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (l *fileLock) File() *os.File {
	if l == nil {
		return nil
	}
	return l.file
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
