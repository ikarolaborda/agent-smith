//go:build !darwin && !linux

package llamacpp

import "fmt"

func freeDisk(path string) (string, uint64, error) {
	return "", 0, fmt.Errorf("disk profiling is unsupported on this platform")
}
