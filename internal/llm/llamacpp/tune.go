package llamacpp

import "fmt"

/*
Recommendation is an auto-tuned launch profile derived from the detected host
and a model's artifact sizes. It is advisory: the fit gate still independently
admits or rejects the resulting configuration. The goal is to make the most of
the hardware without the operator hand-editing YAML — full GPU offload with a
generous context when the accelerator has room, a partial offload when it does
not, and a RAM-bounded CPU profile when there is no usable GPU.
*/
type Recommendation struct {
	GPULayers   int         `json:"gpu_layers"`
	CtxSize     int         `json:"ctx_size"`
	Backend     GPUBackend  `json:"backend"`
	FullGPU     bool        `json:"full_gpu_offload"`
	KVCacheType KVCacheType `json:"kv_cache_type,omitempty"`
	Rationale   []string    `json:"rationale"`
}

/*
fullOffloadLayers is the sentinel "offload every layer" value. llama.cpp clamps
-ngl to the model's actual layer count, so a large number means "all layers".
*/
const fullOffloadLayers = 99

/* ctxLadder is the set of context sizes the tuner will pick from, largest-first. */
var ctxLadder = []int{32768, 16384, 8192, 4096, 2048}

/*
kvTypeLadder is tried least-aggressive first so full-precision KV is preferred;
the cache is only quantized when a higher-precision type cannot reach the minimum
context within the budget. Quality-first, and f16-first keeps the zero value and
all existing hosts on their current behavior.
*/
var kvTypeLadder = []KVCacheType{KVCacheF16, KVCacheQ8_0, KVCacheQ4_0}

/* minRecommendedCtx is the floor the tuner will not drop below; a config that
cannot reach it is left for the fit gate to refuse with a clear reason rather
than silently served at an unusably small context. */
const minRecommendedCtx = 2048

/*
fitCtxAndKV picks the least-aggressive KV cache type whose largest fitting ladder
context still meets minRecommendedCtx, then returns that context. base is the
fixed cost (weights+scratch, or weights alone on a GPU where KV lives in VRAM).
It returns (0, f16) when nothing fits, leaving the final refusal to the fit gate.
*/
func fitCtxAndKV(budget, base uint64, ceiling int, policy FitPolicy) (int, KVCacheType) {
	for _, kt := range kvTypeLadder {
		perToken := kvBytesPerToken(policy.KVBytesPerToken, kt)
		for _, c := range ctxLadder {
			if c > ceiling {
				continue
			}
			if base+uint64(c)*perToken <= budget {
				if c >= minRecommendedCtx {
					return c, kt
				}
				break /* ladder is largest-first: a sub-minimum first fit means this type cannot do better */
			}
		}
	}
	return 0, KVCacheF16
}

/*
RecommendRuntime picks GPU layers and context size for a model on this host.
modelBytes+mmprojBytes are the on-disk artifact sizes; maxCtx caps the context
(0 = use the tuner's own ceiling). It never returns a config it believes cannot
launch — the fit gate is the final authority, but the recommendation aims to
already satisfy it.
*/
func RecommendRuntime(host HostProfile, modelBytes, mmprojBytes uint64, maxCtx int) Recommendation {
	/*
		Never speculate on missing evidence: with an unknown model size or host
		memory, fall back to a conservative CPU profile rather than an aggressive
		offload the fit gate might then reject or that could OOM. The caller
		treats gpu_layers=0 as "no offload".
	*/
	if modelBytes == 0 || host.TotalMemoryBytes == 0 || host.AvailableMemoryBytes == 0 {
		return Recommendation{
			GPULayers: 0,
			CtxSize:   defaultContextTokens,
			Backend:   GPUBackendNone,
			Rationale: []string{"insufficient host or model information; conservative CPU default (no auto-offload)"},
		}
	}

	policy := DefaultFitPolicy()
	artifacts := modelBytes + mmprojBytes
	weights := artifacts + artifacts/100*15 // +15% mapping/runtime overhead
	scratch := max(policy.MinimumScratchBytes, artifacts/10)

	ceiling := 32768
	if maxCtx > 0 && maxCtx < ceiling {
		ceiling = maxCtx
	}

	rec := Recommendation{Backend: host.GPU.Backend, Rationale: []string{}}

	kvAt := func(ctx int) uint64 { return uint64(ctx) * policy.KVBytesPerToken }

	/* largestCtxWithin returns the biggest ladder ctx whose weights+scratch+KV fit budget. */
	largestCtxWithin := func(budget uint64, base uint64) int {
		for _, c := range ctxLadder {
			if c > ceiling {
				continue
			}
			if base+kvAt(c) <= budget {
				return c
			}
		}
		return 0
	}

	/* CPU-only or no usable GPU: bound context by the system-RAM safe budget. */
	if !host.GPU.HasUsableGPU() {
		ramBudget := ramSafeBudget(host, policy)
		ctx, kt := fitCtxAndKV(ramBudget, weights+scratch, ceiling, policy)
		if ctx == 0 {
			ctx, kt = 2048, KVCacheF16
		}
		rec.GPULayers = 0
		rec.CtxSize = ctx
		rec.Backend = GPUBackendNone
		rec.KVCacheType = kt
		rec.Rationale = append(rec.Rationale, fmt.Sprintf("no usable GPU detected; CPU inference with ctx=%d%s bounded by the system-RAM safe budget", ctx, kvNote(kt)))
		return rec
	}

	/* Apple / unified memory: offload fully; the GPU shares the RAM budget. */
	if host.GPU.Unified {
		ramBudget := ramSafeBudget(host, policy)
		ctx, kt := fitCtxAndKV(ramBudget, weights+scratch, ceiling, policy)
		if ctx == 0 {
			ctx, kt = 2048, KVCacheF16
		}
		rec.GPULayers = fullOffloadLayers
		rec.CtxSize = ctx
		rec.FullGPU = true
		rec.KVCacheType = kt
		rec.Rationale = append(rec.Rationale, fmt.Sprintf("unified-memory GPU (%s): full offload, ctx=%d%s bounded by shared RAM budget", host.GPU.Backend, ctx, kvNote(kt)))
		return rec
	}

	/* Discrete GPU: weights + KV must fit dedicated VRAM to offload fully. */
	vramReserve := max(512*1024*1024, host.GPU.VRAMBytes/16) // leave headroom for the driver/compute
	vramBudget := saturatingSub(host.GPU.VRAMBytes, vramReserve)
	if vramBudget >= weights+kvAt(2048) {
		ctx := largestCtxWithin(vramBudget, weights)
		if ctx == 0 {
			ctx = 2048
		}
		rec.GPULayers = fullOffloadLayers
		rec.CtxSize = ctx
		rec.FullGPU = true
		rec.Rationale = append(rec.Rationale, fmt.Sprintf("%s GPU with %s VRAM: full offload, ctx=%d fits weights+KV in VRAM", host.GPU.Backend, humanGiB(host.GPU.VRAMBytes), ctx))
		return rec
	}

	/*
		Not enough VRAM for the whole model: offload the fraction of weights that
		fits (leaving room for a small KV slice), and keep the context modest since
		the KV cache is split across host and device.
	*/
	weightVRAM := saturatingSub(vramBudget, kvAt(2048))
	frac := 0.0
	if weights > 0 {
		frac = float64(weightVRAM) / float64(weights)
	}
	layers := int(frac * float64(fullOffloadLayers))
	if layers < 1 {
		layers = 1
	}
	if layers > fullOffloadLayers-1 {
		layers = fullOffloadLayers - 1
	}
	ctx := 4096
	if ctx > ceiling {
		ctx = ceiling
	}
	rec.GPULayers = layers
	rec.CtxSize = ctx
	rec.Rationale = append(rec.Rationale, fmt.Sprintf("%s GPU with %s VRAM is smaller than the model: partial offload ~%d layers, ctx=%d (KV split host/device)", host.GPU.Backend, humanGiB(host.GPU.VRAMBytes), layers, ctx))
	return rec
}

/* ramSafeBudget mirrors the fit gate's system-memory budget for recommendations. */
func ramSafeBudget(host HostProfile, policy FitPolicy) uint64 {
	osReserve := max(policy.MinimumOSReserveBytes, host.TotalMemoryBytes/8)
	totalBudget := saturatingSub(host.TotalMemoryBytes, osReserve)
	headroom := max(policy.MinimumFreeHeadroom, host.TotalMemoryBytes/32)
	availBudget := saturatingSub(host.AvailableMemoryBytes, headroom)
	return min(totalBudget, availBudget)
}

func humanGiB(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/float64(byteGiB))
}

/* kvNote annotates the rationale only when the KV cache was quantized to fit. */
func kvNote(t KVCacheType) string {
	if t == "" || t == KVCacheF16 {
		return ""
	}
	return fmt.Sprintf(" with KV cache %s", t)
}
