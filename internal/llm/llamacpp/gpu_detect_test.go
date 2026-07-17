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

func TestDetectLinuxGPU_RX7800XT_viaRocmSmi(t *testing.T) {
	run := fakeGPUTools(map[string]string{
		"rocm-smi": "" + // both rocm-smi invocations hit this; product-name + vram both parse from a superset
			"GPU[0]\t\t: Card Series: \t\tRadeon RX 7800 XT\n" +
			"device,VRAM Total Memory (B),VRAM Total Used Memory (B)\n" +
			"card0,17163091968,1048576\n",
	})
	g := detectLinuxGPU(context.Background(), run)
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
	g := detectLinuxGPU(context.Background(), run)
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
	g := detectLinuxGPU(context.Background(), run)
	if g.Vendor != "amd" || g.Backend != GPUBackendVulkan {
		t.Fatalf("expected AMD/vulkan fallback, got %+v", g)
	}
	if g.HasUsableGPU() {
		t.Errorf("lspci-only detection has no VRAM, so it must not report a usable (offload-admissible) GPU")
	}
}

func TestDetectLinuxGPU_NoneWhenNoTools(t *testing.T) {
	g := detectLinuxGPU(context.Background(), fakeGPUTools(nil))
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
	rec := RecommendRuntime(host, 5_800_000_000, 900_000_000, 0)

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
