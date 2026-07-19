//go:build windows

package runner

// Windows-hosted Linux containers do not expose a numeric host UID/GID.
func hostContainerUser() string { return "0:0" }
