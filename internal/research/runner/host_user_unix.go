//go:build !windows

package runner

import (
	"os"
	"strconv"
)

func hostContainerUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}
