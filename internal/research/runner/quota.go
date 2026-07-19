package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const writableBudgetPollInterval = 20 * time.Millisecond

var errWritableBudget = errors.New("research runner: writable resource budget exceeded")

type writableTreeUsage struct {
	bytes  int64
	inodes int64
}

type writableMonitorResult struct {
	peakBytes  int64
	peakInodes int64
	err        error
}

// monitorWritableBudget bounds the live writable footprint of the per-run
// output directory and every writable job mount. Read-only source/build inputs
// are excluded. Kernel/filesystem quotas remain preferable, but this monitor
// fails the run and cancels the backend as soon as the declared campaign
// footprint, inode ceiling, or regular-file-only invariant is violated.
func monitorWritableBudget(ctx context.Context, cancel context.CancelCauseFunc, job domain.WorkerJob, staging string, stop <-chan struct{}) (<-chan writableMonitorResult, error) {
	roots, err := writableRoots(job, staging)
	if err != nil {
		return nil, err
	}
	baseline, err := measureWritableTrees(roots)
	if err != nil {
		return nil, err
	}
	if baseline.bytes > job.Budget.MaxDiskBytes {
		return nil, fmt.Errorf("%w: writable footprint %d bytes exceeds %d", errWritableBudget, baseline.bytes, job.Budget.MaxDiskBytes)
	}
	if baseline.inodes > job.Budget.MaxInodes {
		return nil, fmt.Errorf("%w: writable footprint %d inodes exceeds %d", errWritableBudget, baseline.inodes, job.Budget.MaxInodes)
	}
	done := make(chan writableMonitorResult, 1)
	go func() {
		defer close(done)
		result := writableMonitorResult{}
		measure := func() bool {
			current, measureErr := measureWritableTrees(roots)
			if measureErr != nil {
				result.err = measureErr
				cancel(measureErr)
				return false
			}
			growthBytes := max(current.bytes-baseline.bytes, 0)
			growthInodes := max(current.inodes-baseline.inodes, 0)
			result.peakBytes = max(result.peakBytes, growthBytes)
			result.peakInodes = max(result.peakInodes, growthInodes)
			if current.bytes > job.Budget.MaxDiskBytes {
				result.err = fmt.Errorf("%w: writable footprint %d bytes exceeds %d", errWritableBudget, current.bytes, job.Budget.MaxDiskBytes)
				cancel(result.err)
				return false
			}
			if current.inodes > job.Budget.MaxInodes {
				result.err = fmt.Errorf("%w: writable footprint %d inodes exceeds %d", errWritableBudget, current.inodes, job.Budget.MaxInodes)
				cancel(result.err)
				return false
			}
			return true
		}
		if !measure() {
			done <- result
			return
		}
		ticker := time.NewTicker(writableBudgetPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				measure()
				done <- result
				return
			case <-ctx.Done():
				done <- result
				return
			case <-ticker.C:
				if !measure() {
					done <- result
					return
				}
			}
		}
	}()
	return done, nil
}

func writableRoots(job domain.WorkerJob, staging string) ([]string, error) {
	values := []string{staging}
	for _, mount := range job.Mounts {
		if !mount.ReadOnly {
			values = append(values, mount.HostPath)
		}
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("research runner: resolve writable root: %w", err)
		}
		info, err := os.Lstat(real)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("research runner: writable root must be a real directory")
		}
		if !seen[real] {
			seen[real] = true
			result = append(result, real)
		}
	}
	return result, nil
}

func measureWritableTrees(roots []string) (writableTreeUsage, error) {
	var usage writableTreeUsage
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if path == root {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("research runner: writable tree contains symlink %q", filepath.Base(path))
			}
			if entry.IsDir() {
				usage.inodes++
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("research runner: writable tree contains non-regular entry %q", filepath.Base(path))
			}
			if info.Size() < 0 || usage.bytes > math.MaxInt64-info.Size() {
				return errors.New("research runner: writable footprint overflow")
			}
			usage.bytes += info.Size()
			usage.inodes++
			return nil
		})
		if err != nil {
			return usage, err
		}
	}
	return usage, nil
}
