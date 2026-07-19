//go:build darwin || linux

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

type storeFileLock struct {
	file *os.File
}

func acquireStoreFileLock(path string) (*storeFileLock, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("research store: open instance lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("research store: invalid instance lock descriptor")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("research store: protect instance lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("research store: instance lock is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("research store: custody directory is already open by another process")
		}
		return nil, fmt.Errorf("research store: acquire instance lock: %w", err)
	}
	return &storeFileLock{file: file}, nil
}

func (lock *storeFileLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
