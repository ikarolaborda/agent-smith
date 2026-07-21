package llamacpp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

/*
fakeGPUTools returns canned stdout for named tools and an error ("not found")
for anything else, so the whole detectLinuxGPU orchestration runs on a host that
has none of the tools installed — exercising the exact code path that will run
on the Linux/AMD box, minus the real exec.
*/
func fakeGPUTools(outputs map[string]string) gpuToolRunner {
	return func(_ context.Context, name string, _ ...string) (string, error) {
		if out, ok := outputs[name]; ok {
			return out, nil
		}
		return "", errors.New("exec: \"" + name + "\": executable file not found")
	}
}

/* noVRAMProbe simulates a host whose driver exposes no VRAM via sysfs. */
func noVRAMProbe() (uint64, bool) { return 0, false }

/* fixedVRAMProbe simulates the amdgpu sysfs node reporting a known VRAM size. */
func fixedVRAMProbe(bytes uint64) vramProbe {
	return func() (uint64, bool) { return bytes, true }
}

func TestDetectLinuxGPU_RX7800XT_viaRocmSmi(t *testing.T) {
	run := fakeGPUTools(map[string]string{
		"rocm-smi": "" + // both rocm-smi invocations hit this; product-name + vram both parse from a superset
			"GPU[0]\t\t: Card Series: \t\tRadeon RX 7800 XT\n" +
			"device,VRAM Total Memory (B),VRAM Total Used Memory (B)\n" +
			"card0,17163091968,1048576\n",
	})
	g := detectLinuxGPU(context.Background(), run, noVRAMProbe)
	if g.Backend != GPUBackendROCm || g.Vendor != "amd" {
		t.Fatalf("expected AMD/rocm, got %+v", g)
	}
	if g.VRAMBytes != 17163091968 {
		t.Errorf("VRAM = %d, want 17163091968", g.VRAMBytes)
	}
	if !g.HasUsableGPU() {
		t.Errorf("RX 7800 XT with detected VRAM must be a usable GPU")
	}
}

func TestDetectLinuxGPU_NvidiaWins(t *testing.T) {
	run := fakeGPUTools(map[string]string{
		"nvidia-smi": "NVIDIA GeForce RTX 4090, 24564\n",
	})
	g := detectLinuxGPU(context.Background(), run, noVRAMProbe)
	if g.Backend != GPUBackendCUDA || g.VRAMBytes == 0 {
		t.Fatalf("expected CUDA with VRAM, got %+v", g)
	}
}

func TestDetectLinuxGPU_LspciVulkanFallback(t *testing.T) {
	/* No vendor VRAM tool, but lspci identifies the card and vulkaninfo exists. */
	run := fakeGPUTools(map[string]string{
		"lspci":      "03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 32 [Radeon RX 7800 XT]\n",
		"vulkaninfo": "GPU0: apiVersion = 1.3\n",
	})
	g := detectLinuxGPU(context.Background(), run, noVRAMProbe)
	if g.Vendor != "amd" || g.Backend != GPUBackendVulkan {
		t.Fatalf("expected AMD/vulkan fallback, got %+v", g)
	}
	if g.HasUsableGPU() {
		t.Errorf("lspci-only detection with no sysfs VRAM must not report a usable (offload-admissible) GPU")
	}
}

func TestDetectLinuxGPU_LspciVulkanWithSysfsVRAM(t *testing.T) {
	/*
		The real Linux/AMD box: no nvidia-smi/rocm-smi, lspci names the card,
		vulkaninfo confirms the backend, and the amdgpu sysfs node supplies the
		16 GiB VRAM budget the tuner needs to actually offload.
	*/
	run := fakeGPUTools(map[string]string{
		"lspci":      "03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 32 [Radeon RX 7800 XT]\n",
		"vulkaninfo": "GPU0: apiVersion = 1.3\n",
	})
	const vram = 16 * 1024 * 1024 * 1024
	g := detectLinuxGPU(context.Background(), run, fixedVRAMProbe(vram))
	if g.Vendor != "amd" || g.Backend != GPUBackendVulkan {
		t.Fatalf("expected AMD/vulkan, got %+v", g)
	}
	if g.VRAMBytes != vram {
		t.Errorf("VRAM = %d, want %d (from sysfs)", g.VRAMBytes, uint64(vram))
	}
	if g.Detection != "lspci+amdgpu-sysfs" {
		t.Errorf("detection = %q, want lspci+amdgpu-sysfs", g.Detection)
	}
	if !g.HasUsableGPU() {
		t.Errorf("Vulkan backend + sysfs VRAM must be an offload-admissible GPU")
	}
}

/*
sampleVulkaninfo mimics the multi-device layout of the real Linux/AMD box: a
discrete GPU that advertises a large host-visible (GTT) heap WITHOUT the
device-local flag plus the real 15.98 GiB VRAM heap WITH it, an integrated GPU
whose device-local heap is system RAM, and the llvmpipe CPU device. Only the
discrete device-local heap is the true VRAM.
*/
const sampleVulkaninfo = "" +
	"GPU id : 0 (AMD Radeon RX 7800 XT (RADV NAVI32)):\n" +
	"\tdeviceType        = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n" +
	"\tmemoryHeaps[0]:\n" +
	"\t\tsize   = 66237911040 (0xf6c165000) (61.69 GiB)\n" +
	"\t\tflags: count = 0\n" +
	"\tmemoryHeaps[1]:\n" +
	"\t\tsize   = 17163091968 (0x3ff000000) (15.98 GiB)\n" +
	"\t\tflags: count = 1\n" +
	"\t\t\tMEMORY_HEAP_DEVICE_LOCAL_BIT\n" +
	"GPU id : 1 (AMD Ryzen 9 9950X 16-Core Processor (RADV RAPHAEL_MENDOCINO)):\n" +
	"\tdeviceType        = PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU\n" +
	"\tmemoryHeaps[0]:\n" +
	"\t\tsize   = 45590265856 (0xa9d644000) (42.46 GiB)\n" +
	"\t\tflags: count = 1\n" +
	"\t\t\tMEMORY_HEAP_DEVICE_LOCAL_BIT\n" +
	"GPU id : 2 (llvmpipe (LLVM 20.1.2, 256 bits)):\n" +
	"\tdeviceType        = PHYSICAL_DEVICE_TYPE_CPU\n" +
	"\tmemoryHeaps[0]:\n" +
	"\t\tsize   = 132475826176 (0x1ed82cb000) (123.38 GiB)\n" +
	"\t\tflags: count = 1\n" +
	"\t\t\tMEMORY_HEAP_DEVICE_LOCAL_BIT\n"

func TestParseVulkaninfoDiscreteVRAM_PicksDiscreteDeviceLocalHeap(t *testing.T) {
	vram, ok := parseVulkaninfoDiscreteVRAM(sampleVulkaninfo)
	if !ok {
		t.Fatal("expected a discrete VRAM reading")
	}
	if vram != 17163091968 {
		t.Errorf("VRAM = %d, want 17163091968 (discrete device-local heap, not the 61 GiB GTT heap nor the iGPU/CPU heaps)", vram)
	}
}

func TestParseVulkaninfoDiscreteVRAM_NoDiscreteYieldsNothing(t *testing.T) {
	integratedOnly := "" +
		"GPU id : 0 (Intel iGPU):\n" +
		"\tdeviceType        = PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU\n" +
		"\tmemoryHeaps[0]:\n" +
		"\t\tsize   = 45590265856 (0xa9d644000) (42.46 GiB)\n" +
		"\t\tflags: count = 1\n" +
		"\t\t\tMEMORY_HEAP_DEVICE_LOCAL_BIT\n"
	if vram, ok := parseVulkaninfoDiscreteVRAM(integratedOnly); ok {
		t.Errorf("integrated-only host must not yield a VRAM budget, got %d", vram)
	}
}

func TestDetectLinuxGPU_VulkaninfoVRAMFallback(t *testing.T) {
	/*
		Vendor-agnostic path: no vendor tool, no amdgpu sysfs (noVRAMProbe), but
		lspci names the card and vulkaninfo supplies the discrete VRAM budget.
	*/
	run := fakeGPUTools(map[string]string{
		"lspci":      "03:00.0 VGA compatible controller: NVIDIA Corporation Device 2684 (rev a1)\n",
		"vulkaninfo": sampleVulkaninfo,
	})
	g := detectLinuxGPU(context.Background(), run, noVRAMProbe)
	if g.Backend != GPUBackendVulkan {
		t.Fatalf("expected vulkan backend, got %+v", g)
	}
	if g.VRAMBytes != 17163091968 {
		t.Errorf("VRAM = %d, want 17163091968 (from vulkaninfo)", g.VRAMBytes)
	}
	if g.Detection != "lspci+vulkaninfo" {
		t.Errorf("detection = %q, want lspci+vulkaninfo", g.Detection)
	}
	if !g.HasUsableGPU() {
		t.Errorf("vulkan backend + vulkaninfo VRAM must be offload-admissible")
	}
}

func TestDetectLinuxGPU_NoneWhenNoTools(t *testing.T) {
	g := detectLinuxGPU(context.Background(), fakeGPUTools(nil), noVRAMProbe)
	if g.Backend != GPUBackendNone {
		t.Fatalf("no tools present must yield none, got %+v", g)
	}
}

/*
TestAutoTuneToArgs_DiscreteGPUFullOffload runs the full make-the-most-of-the-GPU
flow end to end: a detected discrete AMD GPU -> RecommendRuntime -> RuntimeConfig
-> the llama-server argument vector, asserting the offload actually reaches the
command line. This is the one piece that was never exercised for a discrete GPU
on the dev machine.
*/
func TestAutoTuneToArgs_DiscreteGPUFullOffload(t *testing.T) {
	host := HostProfile{
		OS: "linux", Arch: "amd64",
		TotalMemoryBytes:     128 * gib,
		AvailableMemoryBytes: 120 * gib,
		FreeDiskBytes:        8000 * gib,
		GPU:                  GPUInfo{Vendor: "amd", Name: "Radeon RX 7800 XT", VRAMBytes: 16 * gib, Backend: GPUBackendROCm},
	}
	rec := RecommendRuntime(host, 5_800_000_000, 900_000_000, 0, 0)

	rt := NewRuntime(RuntimeConfig{
		Binary:    "llama-server",
		ModelPath: "/models/m.gguf",
		GPULayers: rec.GPULayers,
		CtxSize:   rec.CtxSize,
		Parallel:  1,
	})
	args := rt.buildArgs("/models/m.gguf", "127.0.0.1", 8080)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-ngl "+strconv.Itoa(fullOffloadLayers)) {
		t.Fatalf("expected full offload -ngl %d in args: %v", fullOffloadLayers, args)
	}
	if !strings.Contains(joined, "--ctx-size "+strconv.Itoa(rec.CtxSize)) || rec.CtxSize < 8192 {
		t.Fatalf("expected a generous --ctx-size (got %d) in args: %v", rec.CtxSize, args)
	}
}
