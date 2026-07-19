//go:build linux

package llamacpp

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

/*
ReapOrphanedServers frees GPU/host memory that a previous agent-smith left behind:
a llama-server this app launched whose supervising process then died keeps running
(reparented to init) and holds VRAM, which blocks the next runtime from loading. On
startup we SIGKILL any such orphan so admission sees the memory it actually has.

It is deliberately conservative. A candidate is reaped ONLY when both hold:
  - its command line carries our runtime signature (the api-key/lock directory this
    app passes to --api-key-file), so a user's unrelated llama-server is never a
    candidate; and
  - it is orphaned — reparented to init, or parented by a process that is not a live
    agent-smith --serve — so another running instance's healthy server is left alone.

Best-effort and Linux-only (reads /proc); Close/Reclaim still handle in-process
supersession. Other platforms get the no-op in orphan_other.go.
*/
func ReapOrphanedServers(logger *slog.Logger) {
	sig := ourRuntimeSignature()
	if sig == "" {
		return
	}
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	var killed []int
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || pid == self {
			continue
		}
		cmd := procCmdline(pid)
		if !strings.Contains(cmd, "llama-server") || !strings.Contains(cmd, sig) {
			continue
		}
		if !isOrphanedServer(pid) {
			continue
		}
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil {
			logger.Warn("llamacpp: could not reap orphaned llama-server", "pid", pid, "err", killErr)
			continue
		}
		logger.Info("llamacpp: reaped orphaned llama-server holding resources", "pid", pid)
		killed = append(killed, pid)
	}
	waitForExit(killed)
}

/*
	ourRuntimeSignature is the lock/api-key directory this app passes on the

llama-server command line — the marker that identifies a server as ours. It mirrors
Runtime.admissionLockDirectory so the two never drift.
*/
func ourRuntimeSignature() string {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		return ""
	}
	return filepath.Join(cache, "agent-smith", "locks")
}

/* procCmdline reads /proc/<pid>/cmdline with its NUL separators turned into spaces. */
func procCmdline(pid int) string {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(raw), "\x00", " ")
}

/*
isOrphanedServer reports whether pid's supervising agent-smith is gone: reparented
to init (ppid<=1), or a parent that is not a live agent-smith --serve process. A
server still parented by a running agent-smith (another instance) is NOT an orphan.
*/
func isOrphanedServer(pid int) bool {
	ppid := procPPID(pid)
	if ppid <= 1 {
		return true
	}
	return !strings.Contains(procCmdline(ppid), "--serve")
}

/*
procPPID parses the parent PID from /proc/<pid>/stat. The comm field can contain
spaces and parentheses, so it scans past the final ')' before splitting: the field
immediately after is the state and the next is the ppid.
*/
func procPPID(pid int) int {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	return parsePPIDFromStat(string(raw))
}

/* parsePPIDFromStat extracts the ppid (field 4) from /proc/<pid>/stat content,
scanning past the final ')' so a comm value containing spaces or parentheses
cannot shift the field positions. Returns 0 when the line is unparseable. */
func parsePPIDFromStat(s string) int {
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0
	}
	fields := strings.Fields(s[i+2:])
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

/*
	waitForExit blocks briefly until the reaped pids leave /proc, so VRAM is released

before the caller measures it for admission. Bounded so a stuck kill cannot hang startup.
*/
func waitForExit(pids []int) {
	if len(pids) == 0 {
		return
	}
	deadline := 5 * time.Second
	step := 100 * time.Millisecond
	for waited := time.Duration(0); waited < deadline; waited += step {
		alive := false
		for _, pid := range pids {
			if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(step)
	}
}
