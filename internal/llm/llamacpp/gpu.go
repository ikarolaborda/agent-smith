package llamacpp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

/*
gpuToolRunner runs an external detection tool and returns its stdout. A missing
binary surfaces as an error, which the detection logic treats the same as "not
present". Injecting it lets the whole detection orchestration — not just the
leaf parsers — run under test with real tool output on a host that has no such
tool.
*/
type gpuToolRunner func(ctx context.Context, name string, args ...string) (string, error)

func execGPUTool(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

/*
vramProbe reads discrete-GPU VRAM without an external tool, returning (bytes,
true) when a real reading is found. It exists so the sysfs fallback — like the
tool runner above — is injectable and the whole detection orchestration stays
hermetic under test on a host that does (or does not) have a real GPU.
*/
type vramProbe func() (uint64, bool)

/*
detectGPU inspects the local accelerator without retaining state. It is
best-effort and fail-open: any probe that is missing or errors simply advances
to the next candidate, and an undetected GPU returns GPUBackendNone so the
caller falls back to CPU. totalRAM is used only for Apple unified memory, where
the GPU has no separate VRAM bank.
*/
func detectGPU(ctx context.Context, totalRAM uint64) GPUInfo {
	return detectGPUWith(ctx, totalRAM, execGPUTool, readAMDGPUVRAMSysfs)
}

func detectGPUWith(ctx context.Context, totalRAM uint64, run gpuToolRunner, probeVRAM vramProbe) GPUInfo {
	switch runtime.GOOS {
	case "darwin":
		return detectDarwinGPU(ctx, totalRAM, run)
	case "linux":
		return detectLinuxGPU(ctx, run, probeVRAM)
	default:
		return GPUInfo{Backend: GPUBackendNone}
	}
}

func detectDarwinGPU(ctx context.Context, totalRAM uint64, run gpuToolRunner) GPUInfo {
	/*
		Every modern Mac (Apple Silicon and the AMD-equipped Intel Macs llama.cpp
		targets) offloads through Metal, and Apple Silicon shares one unified
		memory pool with the CPU, so the GPU budget is the system RAM the fit gate
		already accounts for.
	*/
	g := GPUInfo{Vendor: "apple", Backend: GPUBackendMetal, Unified: true, VRAMBytes: totalRAM, Detection: "darwin"}
	if out, err := run(ctx, "system_profiler", "SPDisplaysDataType"); err == nil {
		if name := parseSystemProfilerGPUName(out); name != "" {
			g.Name = name
		}
	}
	return g
}

/*
detectLinuxGPU prefers the vendor tool that also reports VRAM (nvidia-smi,
rocm-smi) so auto-tuning has a real budget, then falls back to lspci for
vendor/name identification plus a Vulkan capability probe. On that fallback the
amdgpu driver still exposes VRAM through sysfs even when rocm-smi is absent, so
probeVRAM backfills the budget the fit gate and tuner need. Every tool is
invoked through run and a failure (including a missing binary) falls through to
the next candidate.
*/
func detectLinuxGPU(ctx context.Context, run gpuToolRunner, probeVRAM vramProbe) GPUInfo {
	if out, err := run(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"); err == nil {
		if g, ok := parseNvidiaSMI(out); ok {
			return g
		}
	}
	name := ""
	if out, err := run(ctx, "rocm-smi", "--showproductname"); err == nil {
		name = parseRocmProductName(out)
	}
	if out, err := run(ctx, "rocm-smi", "--showmeminfo", "vram", "--csv"); err == nil {
		if g, ok := parseRocmVRAM(out); ok {
			g.Name = name
			return g
		}
	}
	if out, err := run(ctx, "lspci"); err == nil {
		if g, ok := parseLspciGPU(out); ok {
			if _, verr := run(ctx, "vulkaninfo", "--summary"); verr == nil {
				g.Backend = GPUBackendVulkan
			}
			/*
				lspci reports no VRAM. Without a budget HasUsableGPU stays false and
				the tuner refuses to offload, so backfill it — the reading only becomes
				offload-admissible once a real backend (e.g. Vulkan above) is present.
				Try the cheap AMD-specific sysfs node first, then fall back to the
				vendor-agnostic vulkaninfo heap probe (AMD/NVIDIA/Intel) which requires
				enumerating every device and so is the more expensive path.
			*/
			if g.VRAMBytes == 0 && probeVRAM != nil {
				if vram, ok := probeVRAM(); ok {
					g.VRAMBytes = vram
					g.Detection = "lspci+amdgpu-sysfs"
				}
			}
			if g.VRAMBytes == 0 && g.Backend == GPUBackendVulkan {
				if out, verr := run(ctx, "vulkaninfo"); verr == nil {
					if vram, ok := parseVulkaninfoDiscreteVRAM(out); ok {
						g.VRAMBytes = vram
						g.Detection = "lspci+vulkaninfo"
					}
				}
			}
			return g
		}
	}
	return GPUInfo{Backend: GPUBackendNone}
}

/*
parseVulkaninfoDiscreteVRAM extracts the discrete GPU's VRAM from full
`vulkaninfo` text, vendor-agnostically (AMD, NVIDIA, Intel all expose it the same
way). It counts only the MEMORY_HEAP_DEVICE_LOCAL_BIT heap of a device whose
deviceType is DISCRETE_GPU, taking the max across discrete devices. This matters
because a discrete GPU also advertises a large host-visible (GTT) heap without the
device-local flag, and integrated GPUs / the llvmpipe CPU device advertise huge
device-local heaps backed by system RAM — trusting either would make the tuner
over-offload. No discrete GPU yields (0, false) so the caller keeps the CPU path.
*/
func parseVulkaninfoDiscreteVRAM(out string) (uint64, bool) {
	var best, pendingSize uint64
	curDiscrete, inHeap := false, false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "GPU id :"):
			curDiscrete, inHeap, pendingSize = false, false, 0
		case strings.HasPrefix(trimmed, "deviceType"):
			curDiscrete = strings.Contains(line, "DISCRETE_GPU")
			inHeap, pendingSize = false, 0
		case strings.HasPrefix(trimmed, "memoryHeaps["):
			inHeap, pendingSize = true, 0
		case inHeap && strings.HasPrefix(trimmed, "size"):
			if i := strings.Index(trimmed, "="); i >= 0 {
				if fields := strings.Fields(trimmed[i+1:]); len(fields) > 0 {
					if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
						pendingSize = v
					}
				}
			}
		case inHeap && strings.Contains(line, "MEMORY_HEAP_DEVICE_LOCAL_BIT"):
			if curDiscrete && pendingSize > best {
				best = pendingSize
			}
			inHeap, pendingSize = false, 0
		}
	}
	return best, best > 0
}

/*
readAMDGPUVRAMSysfs returns the largest mem_info_vram_total the amdgpu driver
exposes under /sys/class/drm. This is AMD-specific: the attribute is an amdgpu
convention, so NVIDIA (covered by nvidia-smi) and Intel Arc do not populate it —
a vendor-agnostic VRAM source would parse vulkaninfo's device-local heap instead.
The maximum picks the discrete card over a small integrated GPU (whose sysfs node
reports a token amount), which is the device a llama.cpp offload targets. A
missing or unreadable node yields (0, false) so the caller keeps the CPU path.
*/
func readAMDGPUVRAMSysfs() (uint64, bool) {
	matches, err := filepath.Glob("/sys/class/drm/card*/device/mem_info_vram_total")
	if err != nil {
		return 0, false
	}
	var maxBytes uint64
	for _, path := range matches {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		bytes, parseErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if parseErr != nil {
			continue
		}
		if bytes > maxBytes {
			maxBytes = bytes
		}
	}
	return maxBytes, maxBytes > 0
}

/* parseNvidiaSMI reads "name, memory.total" CSV (MiB) from nvidia-smi. */
func parseNvidiaSMI(out string) (GPUInfo, bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		mib, err := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
		if err != nil || mib == 0 {
			continue
		}
		return GPUInfo{Vendor: "nvidia", Name: name, VRAMBytes: mib * 1024 * 1024, Backend: GPUBackendCUDA, Detection: "nvidia-smi"}, true
	}
	return GPUInfo{}, false
}

/* parseRocmVRAM reads the total VRAM (bytes) from `rocm-smi --showmeminfo vram --csv`. */
func parseRocmVRAM(out string) (GPUInfo, bool) {
	var header []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if header == nil && strings.Contains(strings.ToLower(line), "vram") {
			for _, f := range fields {
				header = append(header, strings.ToLower(strings.TrimSpace(f)))
			}
			continue
		}
		if header == nil {
			continue
		}
		for i, f := range fields {
			if i >= len(header) {
				break
			}
			if strings.Contains(header[i], "vram total memory") || (strings.Contains(header[i], "vram") && strings.Contains(header[i], "total")) {
				if b, err := strconv.ParseUint(strings.TrimSpace(f), 10, 64); err == nil && b > 0 {
					return GPUInfo{Vendor: "amd", VRAMBytes: b, Backend: GPUBackendROCm, Detection: "rocm-smi"}, true
				}
			}
		}
	}
	return GPUInfo{}, false
}

/* parseRocmProductName extracts a card name from `rocm-smi --showproductname`. */
func parseRocmProductName(out string) string {
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "card series") || strings.Contains(low, "card model") || strings.Contains(low, "product name") {
			if i := strings.LastIndex(line, ":"); i >= 0 {
				if name := strings.TrimSpace(line[i+1:]); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

/* parseSystemProfilerGPUName extracts the first "Chipset Model:" from system_profiler. */
func parseSystemProfilerGPUName(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Chipset Model:") {
			if i := strings.Index(line, ":"); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

/* parseLspciGPU finds a VGA/3D/Display controller line and infers vendor/name. */
func parseLspciGPU(out string) (GPUInfo, bool) {
	for _, line := range strings.Split(out, "\n") {
		low := strings.ToLower(line)
		if !strings.Contains(low, "vga compatible controller") && !strings.Contains(low, "3d controller") && !strings.Contains(low, "display controller") {
			continue
		}
		desc := line
		if i := strings.Index(line, ":"); i >= 0 {
			/* strip the "00:02.0 VGA compatible controller:" prefix */
			if j := strings.Index(line[i+1:], ":"); j >= 0 {
				desc = strings.TrimSpace(line[i+1+j+1:])
			}
		}
		vendor := ""
		switch {
		case strings.Contains(low, "nvidia"):
			vendor = "nvidia"
		case strings.Contains(low, "amd") || strings.Contains(low, "advanced micro devices") || strings.Contains(low, "radeon"):
			vendor = "amd"
		case strings.Contains(low, "intel"):
			vendor = "intel"
		}
		if vendor == "" {
			continue
		}
		return GPUInfo{Vendor: vendor, Name: desc, Backend: GPUBackendNone, Detection: "lspci"}, true
	}
	return GPUInfo{}, false
}
