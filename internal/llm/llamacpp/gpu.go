package llamacpp

import (
	"context"
	"os/exec"
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
detectGPU inspects the local accelerator without retaining state. It is
best-effort and fail-open: any probe that is missing or errors simply advances
to the next candidate, and an undetected GPU returns GPUBackendNone so the
caller falls back to CPU. totalRAM is used only for Apple unified memory, where
the GPU has no separate VRAM bank.
*/
func detectGPU(ctx context.Context, totalRAM uint64) GPUInfo {
	return detectGPUWith(ctx, totalRAM, execGPUTool)
}

func detectGPUWith(ctx context.Context, totalRAM uint64, run gpuToolRunner) GPUInfo {
	switch runtime.GOOS {
	case "darwin":
		return detectDarwinGPU(ctx, totalRAM, run)
	case "linux":
		return detectLinuxGPU(ctx, run)
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
vendor/name identification (no VRAM) plus a Vulkan capability probe. Every tool
is invoked through run and a failure (including a missing binary) falls through
to the next candidate.
*/
func detectLinuxGPU(ctx context.Context, run gpuToolRunner) GPUInfo {
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
			return g
		}
	}
	return GPUInfo{Backend: GPUBackendNone}
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
