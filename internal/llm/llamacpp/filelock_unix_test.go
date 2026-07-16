//go:build darwin || linux

package llamacpp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const fileLockHelperEnv = "AGENT_SMITH_FILE_LOCK_HELPER"

func TestFileLockSubprocessHelper(t *testing.T) {
	path := os.Getenv(fileLockHelperEnv)
	if path == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := acquireFileLock(ctx, path); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process lock was not held: %v", err)
	}
}

func TestFileLockSerializesAndCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.lock")
	first, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := acquireFileLock(ctx, path); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock did not honor cancellation: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFileLockSubprocessHelper$")
	cmd.Env = append(os.Environ(), fileLockHelperEnv+"="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock helper: %v\n%s", err, out)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("lock remained held after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
