package llamacpp

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

/*
detectGPU inspects the local accelerator without retaining state. It is
best-effort and fail-open: any probe that is missing or errors simply advances
to the next candidate, and an undetected GPU returns GPUBackendNone so the
caller falls back to CPU. totalRAM is used only for Apple unified memory, where
the GPU has no separate VRAM bank.

Ordering on Linux prefers the vendor tool that also reports VRAM (nvidia-smi,
rocm-smi) so auto-tuning has a real budget; it falls back to lspci for
vendor/name identification (no VRAM) and to a Vulkan capability probe.
*/
func detectGPU(ctx context.Context, totalRAM uint64) GPUInfo {
	switch runtime.GOOS {
	case "darwin":
		return detectDarwinGPU(ctx, totalRAM)
	case "linux":
		return detectLinuxGPU(ctx)
	default:
		return GPUInfo{Backend: GPUBackendNone}
	}
}

func detectDarwinGPU(ctx context.Context, totalRAM uint64) GPUInfo {
	/*
		Every modern Mac (Apple Silicon and the AMD-equipped Intel Macs llama.cpp
		targets) offloads through Metal, and Apple Silicon shares one unified
		memory pool with the CPU, so the GPU budget is the system RAM the fit gate
		already accounts for.
	*/
	g := GPUInfo{Vendor: "apple", Backend: GPUBackendMetal, Unified: true, VRAMBytes: totalRAM, Detection: "darwin"}
	if out, err := runTool(ctx, "system_profiler", "SPDisplaysDataType"); err == nil {
		if name := parseSystemProfilerGPUName(out); name != "" {
			g.Name = name
		}
	}
	return g
}

func detectLinuxGPU(ctx context.Context) GPUInfo {
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		if out, err := runTool(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits"); err == nil {
			if g, ok := parseNvidiaSMI(out); ok {
				return g
			}
		}
	}
	if _, err := exec.LookPath("rocm-smi"); err == nil {
		name := ""
		if out, err := runTool(ctx, "rocm-smi", "--showproductname"); err == nil {
			name = parseRocmProductName(out)
		}
		if out, err := runTool(ctx, "rocm-smi", "--showmeminfo", "vram", "--csv"); err == nil {
			if g, ok := parseRocmVRAM(out); ok {
				g.Name = name
				return g
			}
		}
	}
	/*
		No vendor VRAM tool: identify the card via lspci (vendor/name only, no
		VRAM) and mark Vulkan as the offload backend when the loader is present,
		since llama.cpp's Vulkan build runs on any modern GPU without a vendor SDK.
	*/
	if out, err := runTool(ctx, "lspci"); err == nil {
		if g, ok := parseLspciGPU(out); ok {
			if _, verr := exec.LookPath("vulkaninfo"); verr == nil {
				g.Backend = GPUBackendVulkan
			}
			return g
		}
	}
	return GPUInfo{Backend: GPUBackendNone}
}

func runTool(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
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
