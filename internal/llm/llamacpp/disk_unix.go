//go:build darwin || linux

package llamacpp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func freeDisk(path string) (string, uint64, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	probe := abs
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", 0, fmt.Errorf("no existing ancestor for %q", abs)
		}
		probe = parent
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(probe, &stat); err != nil {
		return "", 0, err
	}
	return probe, uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
