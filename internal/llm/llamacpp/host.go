package llamacpp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// HostProfile is the conservative, point-in-time view used by model preflight.
// AvailableMemoryBytes reflects cgroup limits on Linux when they are tighter
// than the physical host. AppleUnifiedMemory is true on Darwin because model
// weights, KV cache, and GPU allocations all compete for the same memory pool.
type HostProfile struct {
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	TotalMemoryBytes     uint64 `json:"total_memory_bytes"`
	AvailableMemoryBytes uint64 `json:"available_memory_bytes"`
	CgroupLimitBytes     uint64 `json:"cgroup_limit_bytes,omitempty"`
	CgroupUsageBytes     uint64 `json:"cgroup_usage_bytes,omitempty"`
	FreeDiskBytes        uint64 `json:"free_disk_bytes"`
	DiskPath             string `json:"disk_path"`
	AppleUnifiedMemory   bool   `json:"apple_unified_memory"`
	GPU                  GPUInfo `json:"gpu"`
}

/*
GPUInfo is a best-effort, point-in-time view of the accelerator the local
llama.cpp build can offload to. Detection is advisory: a zero-value GPU (Backend
GPUBackendNone) means "no usable accelerator was detected", never an error — the
CPU path always remains available. VRAMBytes is the dedicated device memory for
a discrete GPU; on Apple unified-memory hosts Unified is true and the GPU shares
the system memory pool, so VRAMBytes mirrors total RAM rather than a separate
bank.
*/
type GPUInfo struct {
	Vendor    string     `json:"vendor,omitempty"` // nvidia | amd | apple | intel
	Name      string     `json:"name,omitempty"`
	VRAMBytes uint64     `json:"vram_bytes"`
	Backend   GPUBackend `json:"backend"`
	Unified   bool       `json:"unified"`
	Detection string     `json:"detection,omitempty"` // tool that produced the reading
}

/* GPUBackend names the llama.cpp offload backend a host can use. */
type GPUBackend string

const (
	GPUBackendNone   GPUBackend = "none"
	GPUBackendMetal  GPUBackend = "metal"
	GPUBackendCUDA   GPUBackend = "cuda"
	GPUBackendROCm   GPUBackend = "rocm"
	GPUBackendVulkan GPUBackend = "vulkan"
)

/* HasUsableGPU reports whether an accelerator with known VRAM was detected. */
func (g GPUInfo) HasUsableGPU() bool {
	return g.Backend != "" && g.Backend != GPUBackendNone && g.VRAMBytes > 0
}

// Profiler makes host inspection injectable. Production callers normally use
// SystemProfiler; tests can supply a small deterministic implementation.
type Profiler interface {
	Profile(ctx context.Context, diskPath string) (HostProfile, error)
}

// ProfilerFunc adapts a function into a Profiler.
type ProfilerFunc func(context.Context, string) (HostProfile, error)

func (f ProfilerFunc) Profile(ctx context.Context, path string) (HostProfile, error) {
	return f(ctx, path)
}

// SystemProfiler reads the local Darwin or Linux host without retaining state.
// Other operating systems fail closed rather than guessing memory capacity.
type SystemProfiler struct{}

func (SystemProfiler) Profile(ctx context.Context, diskPath string) (HostProfile, error) {
	if err := ctx.Err(); err != nil {
		return HostProfile{}, err
	}
	p := HostProfile{OS: runtime.GOOS, Arch: runtime.GOARCH, AppleUnifiedMemory: runtime.GOOS == "darwin"}
	var err error
	switch runtime.GOOS {
	case "linux":
		p.TotalMemoryBytes, p.AvailableMemoryBytes, err = linuxMemory()
		if err == nil {
			err = applyLinuxCgroupLimit(&p)
		}
	case "darwin":
		p.TotalMemoryBytes, p.AvailableMemoryBytes, err = darwinMemory(ctx)
	default:
		err = fmt.Errorf("unsupported operating system %q", runtime.GOOS)
	}
	if err != nil {
		return HostProfile{}, fmt.Errorf("llamacpp: inspect host memory: %w", err)
	}
	if p.TotalMemoryBytes == 0 || p.AvailableMemoryBytes == 0 {
		return HostProfile{}, errors.New("llamacpp: host reported zero total or available memory")
	}

	p.DiskPath, p.FreeDiskBytes, err = freeDisk(diskPath)
	if err != nil {
		return HostProfile{}, fmt.Errorf("llamacpp: inspect free disk: %w", err)
	}
	if p.FreeDiskBytes == 0 {
		return HostProfile{}, errors.New("llamacpp: host reported zero free disk space")
	}

	/*
		GPU detection is advisory and must never fail the profile: a host with no
		accelerator still runs on CPU. On Apple the GPU shares unified memory, so
		mirror the system pool rather than reporting a separate VRAM bank.
	*/
	p.GPU = detectGPU(ctx, p.TotalMemoryBytes)
	return p, nil
}

func linuxMemory() (uint64, uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	values := make(map[string]uint64)
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		v, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = v * 1024
	}
	if err := s.Err(); err != nil {
		return 0, 0, err
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if available == 0 {
		available = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	if total == 0 || available == 0 {
		return 0, 0, errors.New("MemTotal or MemAvailable missing from /proc/meminfo")
	}
	return total, available, nil
}

func applyLinuxCgroupLimit(p *HostProfile) error {
	limitPath, usagePath, err := linuxCgroupFiles()
	if err != nil {
		return err
	}
	if limitPath == "" {
		return nil
	}
	limit, unlimited, err := readByteLimit(limitPath, true)
	if err != nil {
		return fmt.Errorf("read cgroup memory limit %q: %w", limitPath, err)
	}
	if unlimited || limit >= (uint64(1)<<62) {
		return nil
	}
	if limit == 0 {
		return errors.New("finite cgroup memory limit is zero")
	}
	usage, usageUnlimited, err := readByteLimit(usagePath, false)
	if err != nil {
		return fmt.Errorf("finite cgroup memory limit found but current usage is unreadable: %w", err)
	}
	if usageUnlimited {
		return errors.New("finite cgroup memory limit found but current usage is unlimited")
	}
	p.CgroupLimitBytes = limit
	p.CgroupUsageBytes = usage
	if limit < p.TotalMemoryBytes {
		p.TotalMemoryBytes = limit
	}
	var remaining uint64
	if usage < limit {
		remaining = limit - usage
	}
	if remaining < p.AvailableMemoryBytes {
		p.AvailableMemoryBytes = remaining
	}
	return nil
}

func linuxCgroupFiles() (string, string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		rel := strings.TrimPrefix(filepath.Clean("/"+parts[2]), "/")
		if parts[0] == "0" && parts[1] == "" {
			base := filepath.Join("/sys/fs/cgroup", rel)
			return filepath.Join(base, "memory.max"), filepath.Join(base, "memory.current"), nil
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				base := filepath.Join("/sys/fs/cgroup/memory", rel)
				return filepath.Join(base, "memory.limit_in_bytes"), filepath.Join(base, "memory.usage_in_bytes"), nil
			}
		}
	}
	return "", "", nil
}

func readByteLimit(path string, allowMax bool) (uint64, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	return parseByteLimit(raw, allowMax)
}

func parseByteLimit(raw []byte, allowMax bool) (uint64, bool, error) {
	s := strings.TrimSpace(string(raw))
	if s == "max" && allowMax {
		return 0, true, nil
	}
	if s == "" || s == "max" {
		return 0, false, errors.New("empty or unexpected unlimited value")
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return v, false, nil
}

func darwinMemory(ctx context.Context) (uint64, uint64, error) {
	total, err := darwinTotalMemory(ctx)
	if err != nil {
		return 0, 0, err
	}
	vmRaw, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("vm_stat: %w", err)
	}
	pageSize := uint64(4096)
	var pages uint64
	for i, line := range strings.Split(string(vmRaw), "\n") {
		if i == 0 {
			if fields := strings.Fields(line); len(fields) >= 8 {
				if v, parseErr := strconv.ParseUint(fields[7], 10, 64); parseErr == nil {
					pageSize = v
				}
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Pages free", "Pages inactive", "Pages speculative":
			v := strings.TrimSuffix(strings.TrimSpace(value), ".")
			if n, parseErr := strconv.ParseUint(v, 10, 64); parseErr == nil {
				pages += n
			}
		}
	}
	available := pages * pageSize
	if total == 0 || available == 0 {
		return 0, 0, errors.New("unable to determine total or available Darwin memory")
	}
	return total, available, nil
}

func darwinTotalMemory(ctx context.Context) (uint64, error) {
	totalRaw, sysctlErr := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if sysctlErr == nil {
		total, err := strconv.ParseUint(strings.TrimSpace(string(totalRaw)), 10, 64)
		if err == nil && total > 0 {
			return total, nil
		}
	}
	// Sandboxed Darwin processes can be denied sysctl while system_profiler is
	// still available. Parse only its JSON hardware field and never include the
	// command output in an error or log (it may contain identifying metadata).
	raw, fallbackErr := exec.CommandContext(ctx, "system_profiler", "-json", "SPHardwareDataType").Output()
	if fallbackErr == nil && len(raw) <= 2<<20 {
		var payload map[string][]map[string]any
		if json.Unmarshal(raw, &payload) == nil {
			for _, row := range payload["SPHardwareDataType"] {
				if value, ok := row["physical_memory"].(string); ok {
					if total, ok := parseHumanBytes(value); ok {
						return total, nil
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("sysctl hw.memsize unavailable and restricted hardware fallback failed")
}

func parseHumanBytes(value string) (uint64, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 {
		return 0, false
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	var multiplier float64
	switch strings.ToUpper(fields[1]) {
	case "KB":
		multiplier = 1 << 10
	case "MB":
		multiplier = 1 << 20
	case "GB":
		multiplier = 1 << 30
	case "TB":
		multiplier = 1 << 40
	default:
		return 0, false
	}
	return uint64(n * multiplier), true
}
