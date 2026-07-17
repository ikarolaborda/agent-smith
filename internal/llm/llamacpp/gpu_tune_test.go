package llamacpp

import "testing"

const gib = uint64(1024 * 1024 * 1024)

func TestParseNvidiaSMI(t *testing.T) {
	g, ok := parseNvidiaSMI("NVIDIA GeForce RTX 4090, 24564\n")
	if !ok || g.Vendor != "nvidia" || g.Backend != GPUBackendCUDA {
		t.Fatalf("nvidia parse: %+v ok=%v", g, ok)
	}
	if g.VRAMBytes != 24564*1024*1024 {
		t.Errorf("vram = %d, want %d", g.VRAMBytes, 24564*1024*1024)
	}
}

func TestParseRocmVRAM(t *testing.T) {
	/* rocm-smi --showmeminfo vram --csv shape */
	csv := "device,VRAM Total Memory (B),VRAM Total Used Memory (B)\n" +
		"card0,17163091968,1048576\n"
	g, ok := parseRocmVRAM(csv)
	if !ok || g.Vendor != "amd" || g.Backend != GPUBackendROCm {
		t.Fatalf("rocm parse: %+v ok=%v", g, ok)
	}
	if g.VRAMBytes != 17163091968 {
		t.Errorf("vram = %d, want 17163091968", g.VRAMBytes)
	}
}

func TestParseLspciGPU_AMD(t *testing.T) {
	line := "03:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 32 [Radeon RX 7800 XT]\n"
	g, ok := parseLspciGPU(line)
	if !ok || g.Vendor != "amd" {
		t.Fatalf("lspci parse: %+v ok=%v", g, ok)
	}
}

/*
TestRecommendRuntime_RX7800XT is the target machine: a 16 GB AMD RX 7800 XT with
128 GB RAM should fully offload a ~5.8 GB Q4 9B model and pick a generous
context — "make the most of the hardware".
*/
func TestRecommendRuntime_RX7800XT(t *testing.T) {
	host := HostProfile{
		OS: "linux", Arch: "amd64",
		TotalMemoryBytes:     128 * gib,
		AvailableMemoryBytes: 120 * gib,
		FreeDiskBytes:        8000 * gib,
		GPU:                  GPUInfo{Vendor: "amd", Name: "Radeon RX 7800 XT", VRAMBytes: 16 * gib, Backend: GPUBackendROCm},
	}
	rec := RecommendRuntime(host, 5_800_000_000, 900_000_000, 0)
	if !rec.FullGPU || rec.GPULayers != fullOffloadLayers {
		t.Fatalf("expected full GPU offload, got layers=%d full=%v: %v", rec.GPULayers, rec.FullGPU, rec.Rationale)
	}
	if rec.CtxSize < 8192 {
		t.Errorf("expected a generous context on a 16GB GPU, got %d", rec.CtxSize)
	}
	if rec.Backend != GPUBackendROCm {
		t.Errorf("backend = %s, want rocm", rec.Backend)
	}
}

func TestRecommendRuntime_SmallVRAMPartialOffload(t *testing.T) {
	host := HostProfile{
		OS: "linux", TotalMemoryBytes: 32 * gib, AvailableMemoryBytes: 28 * gib, FreeDiskBytes: 500 * gib,
		GPU: GPUInfo{Vendor: "nvidia", VRAMBytes: 4 * gib, Backend: GPUBackendCUDA},
	}
	rec := RecommendRuntime(host, 12_000_000_000, 0, 0) // 12 GB model, 4 GB VRAM
	if rec.FullGPU {
		t.Fatalf("12GB model must not fully offload to 4GB VRAM: %v", rec.Rationale)
	}
	if rec.GPULayers < 1 || rec.GPULayers >= fullOffloadLayers {
		t.Errorf("expected partial offload, got %d", rec.GPULayers)
	}
}

func TestRecommendRuntime_CPUOnly(t *testing.T) {
	host := HostProfile{
		OS: "linux", TotalMemoryBytes: 32 * gib, AvailableMemoryBytes: 24 * gib, FreeDiskBytes: 500 * gib,
		GPU: GPUInfo{Backend: GPUBackendNone},
	}
	rec := RecommendRuntime(host, 5_000_000_000, 0, 0)
	if rec.GPULayers != 0 || rec.Backend != GPUBackendNone {
		t.Fatalf("no GPU must recommend CPU (ngl 0), got layers=%d backend=%s", rec.GPULayers, rec.Backend)
	}
	if rec.CtxSize <= 0 {
		t.Errorf("expected a positive CPU ctx, got %d", rec.CtxSize)
	}
}

func TestRecommendRuntime_NoInfoIsConservative(t *testing.T) {
	host := HostProfile{OS: "linux", TotalMemoryBytes: 0, AvailableMemoryBytes: 0}
	rec := RecommendRuntime(host, 0, 0, 0)
	if rec.GPULayers != 0 || rec.Backend != GPUBackendNone {
		t.Fatalf("unknown host/model must not auto-offload, got layers=%d backend=%s", rec.GPULayers, rec.Backend)
	}
}

func TestEstimateFit_DiscreteFullOffloadWithUnknownVRAMRejects(t *testing.T) {
	host := HostProfile{
		OS: "linux", TotalMemoryBytes: 128 * gib, AvailableMemoryBytes: 120 * gib, FreeDiskBytes: 8000 * gib,
		GPU: GPUInfo{Vendor: "amd", Backend: GPUBackendVulkan}, // detected card, no VRAM reading
	}
	r := EstimateFit(host, FitRequest{
		ModelBytes: 6 * gib, ContextTokens: 4096, Parallel: 1,
		GPULayers: fullOffloadLayers, VRAMBytes: 0, // unknown VRAM
	})
	if r.Fits {
		t.Fatalf("full offload with unknown VRAM on a discrete GPU must fail closed: %v", r.Reasons)
	}
}

func TestEstimateFit_DiscreteGPUAdmitsWhatRAMWouldReject(t *testing.T) {
	/*
		A model whose weights exceed system RAM but fit VRAM must be admitted for
		full offload (VRAM gate), and rejected when VRAM is too small.
	*/
	host := HostProfile{
		OS: "linux", Arch: "amd64",
		TotalMemoryBytes: 128 * gib, AvailableMemoryBytes: 120 * gib, FreeDiskBytes: 8000 * gib,
		GPU: GPUInfo{Vendor: "amd", VRAMBytes: 16 * gib, Backend: GPUBackendROCm},
	}
	fits := EstimateFit(host, FitRequest{
		ModelBytes: 6 * gib, ContextTokens: 8192, Parallel: 1,
		GPULayers: fullOffloadLayers, VRAMBytes: host.GPU.VRAMBytes,
	})
	if !fits.Fits || !fits.GPUOffload {
		t.Fatalf("6GB model on 16GB VRAM should fit full offload: %v", fits.Reasons)
	}
	tooBig := EstimateFit(host, FitRequest{
		ModelBytes: 20 * gib, ContextTokens: 8192, Parallel: 1,
		GPULayers: fullOffloadLayers, VRAMBytes: host.GPU.VRAMBytes,
	})
	if tooBig.Fits {
		t.Fatalf("20GB weights must not fit a 16GB VRAM full offload")
	}
}
