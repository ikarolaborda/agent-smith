package llamacpp

import "testing"

/*
partialHost builds a discrete-GPU host whose VRAM is too small for the model
under test, forcing the partial-offload branch.
*/
func partialHost(totalGiB, availGiB, vramGiB uint64) HostProfile {
	return HostProfile{
		OS: "linux", Arch: "amd64",
		TotalMemoryBytes:     totalGiB * byteGiB,
		AvailableMemoryBytes: availGiB * byteGiB,
		FreeDiskBytes:        500 * byteGiB,
		GPU:                  GPUInfo{Vendor: "amd", VRAMBytes: vramGiB * byteGiB, Backend: GPUBackendVulkan},
	}
}

func TestPartialOffloadCtxIsHostRAMBounded(t *testing.T) {
	/*
		The RX 7800 XT class scenario: 128 GiB RAM, 16 GiB VRAM, ~19 GiB model
		with a 131072-native window. The host budget seats the full 131072 f16
		KV reserve, so the branch must no longer flatline at the static 4096.
	*/
	rec := RecommendRuntime(partialHost(128, 100, 16), 19*byteGiB, 0, 0, 131072)
	if rec.FullGPU {
		t.Fatalf("19GiB model must not fully offload into 16GiB VRAM: %v", rec.Rationale)
	}
	if rec.CtxSize != 131072 {
		t.Fatalf("big-RAM host must get the native-capped ladder max, got %d (%v)", rec.CtxSize, rec.Rationale)
	}
	if rec.KVCacheType != KVCacheF16 {
		t.Fatalf("budget seats f16; quantizing was unnecessary, got %s", rec.KVCacheType)
	}
}

func TestPartialOffloadCtxRespectsNativeAndOperatorCeiling(t *testing.T) {
	/* Same big host, 8k-native model: the ceiling, not the budget, binds. */
	rec := RecommendRuntime(partialHost(128, 100, 16), 19*byteGiB, 0, 0, 8192)
	if rec.CtxSize > 8192 {
		t.Fatalf("native context must cap the partial path, got %d", rec.CtxSize)
	}
	/* Operator pin below native binds harder. */
	rec = RecommendRuntime(partialHost(128, 100, 16), 19*byteGiB, 0, 4096, 8192)
	if rec.CtxSize > 4096 {
		t.Fatalf("operator cap must bind, got %d", rec.CtxSize)
	}
}

func TestPartialOffloadTightRAMTakesFloorInsteadOfDoomedDefault(t *testing.T) {
	/*
		Host RAM only seats the 2048 floor: the old static 4096 would have been
		refused outright by the fit gate; the dynamic branch launches at the
		floor instead.
	*/
	rec := RecommendRuntime(partialHost(20, 8, 4), 6*byteGiB, 0, 0, 32768)
	if rec.FullGPU {
		t.Fatal("6GiB model must not fully offload into 4GiB VRAM")
	}
	if rec.CtxSize != 2048 {
		t.Fatalf("tight host should land on the ladder floor, got %d (%v)", rec.CtxSize, rec.Rationale)
	}
}

func TestPartialOffloadNothingFitsFallsBackToLegacyDefault(t *testing.T) {
	/*
		Host RAM cannot even seat the floor: the branch must fall back to the
		exact pre-dynamic behavior (ctx 4096) and leave the refusal to the fit
		gate, never silently invent a new default.
	*/
	rec := RecommendRuntime(partialHost(16, 2, 4), 14*byteGiB, 0, 0, 32768)
	if rec.CtxSize != 4096 {
		t.Fatalf("legacy fallback must remain 4096, got %d (%v)", rec.CtxSize, rec.Rationale)
	}
}
