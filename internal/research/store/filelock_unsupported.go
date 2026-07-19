//go:build !darwin && !linux

package store

import "errors"

type storeFileLock struct{}

func acquireStoreFileLock(string) (*storeFileLock, error) {
	return nil, errors.New("research store: safe cross-process custody locking is unsupported on this platform")
}

func (*storeFileLock) Close() error { return nil }
