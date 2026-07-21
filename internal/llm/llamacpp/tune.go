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
	/*
		NativeCtx echoes the model's trained context length when it was known to
		the tuner (0 = unknown), so callers can warn when an operator pin exceeds
		what the model was trained for.
	*/
	NativeCtx int      `json:"native_ctx,omitempty"`
	Rationale []string `json:"rationale"`
}

/*
fullOffloadLayers is the sentinel "offload every layer" value. llama.cpp clamps
-ngl to the model's actual layer count, so a large number means "all layers".
*/
const fullOffloadLayers = 99

/* ctxLadder is the set of context sizes the tuner will pick from, largest-first. */
var ctxLadder = []int{131072, 65536, 32768, 16384, 8192, 4096, 2048}

/*
fallbackCtxCeiling caps auto-tuning when the model's native context length is
unknown. It preserves the pre-dynamic-ctx behavior exactly: without evidence
that the model was trained beyond 32k, recommending more would risk serving a
window the model cannot actually attend over.
*/
const fallbackCtxCeiling = 32768

/*
effectiveCtxCeiling resolves the context ceiling the ladder search may reach
and names the binding constraint for the rationale. Precedence: an explicit
operator cap always wins when it is the smallest; a known native context caps
the taller ladder; an unknown native context falls back to the conservative
pre-dynamic ceiling instead of the full ladder.
*/
func effectiveCtxCeiling(operatorMax, nativeCtx int) (int, string) {
	ceiling := ctxLadder[0]
	constraint := "resource budget"
	if nativeCtx <= 0 {
		ceiling = fallbackCtxCeiling
		constraint = "unknown native context, conservative cap"
	} else if nativeCtx < ceiling {
		ceiling = nativeCtx
		constraint = "model native context"
	}
	if operatorMax > 0 && operatorMax < ceiling {
		ceiling = operatorMax
		constraint = "operator context cap"
	}
	return ceiling, constraint
}

/*
kvTypeLadder is tried least-aggressive first so full-precision KV is preferred;
the cache is only quantized when a higher-precision type cannot reach the minimum
context within the budget. Quality-first, and f16-first keeps the zero value and
all existing hosts on their current behavior.
*/
var kvTypeLadder = []KVCacheType{KVCacheF16, KVCacheQ8_0, KVCacheQ4_0}

/*
	minRecommendedCtx is the floor the tuner will not drop below; a config that

cannot reach it is left for the fit gate to refuse with a clear reason rather
than silently served at an unusably small context.
*/
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
(0 = no operator cap); nativeCtx is the model's trained context length when
known (0 = unknown, which keeps the conservative pre-dynamic ceiling). It never
returns a config it believes cannot launch — the fit gate is the final
authority, but the recommendation aims to already satisfy it.
*/
func RecommendRuntime(host HostProfile, modelBytes, mmprojBytes uint64, maxCtx, nativeCtx int) Recommendation {
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

	ceiling, ctxConstraint := effectiveCtxCeiling(maxCtx, nativeCtx)

	rec := Recommendation{Backend: host.GPU.Backend, NativeCtx: nativeCtx, Rationale: []string{}}
	rec.Rationale = append(rec.Rationale, fmt.Sprintf("context ceiling %d (%s)", ceiling, ctxConstraint))

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
		fits (leaving room for a small KV slice). The context is bounded by the
		HOST RAM budget like every other path — the weights that miss VRAM plus
		the KV cache land in system memory, so that budget is the real
		constraint. The full per-token KV cost is deliberately charged to host
		RAM even though a slice lives in the VRAM carve-out above: double-counting
		that slice can only over-reserve, and this tuner must never under-reserve.
		Performance moderation is intentionally not encoded here — the tuner's
		contract is admission safety plus the largest usable window, and
		operators who want a smaller window for latency reasons pin ctx_size.
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
	hostBase := saturatingSub(weights, weightVRAM) + scratch
	ctx, kt := fitCtxAndKV(ramSafeBudget(host, policy), hostBase, ceiling, policy)
	if ctx == 0 {
		/* legacy fallback, unchanged from the static era: never worse than before */
		ctx, kt = 4096, KVCacheF16
		if ctx > ceiling {
			ctx = ceiling
		}
	}
	rec.GPULayers = layers
	rec.CtxSize = ctx
	rec.KVCacheType = kt
	rec.Rationale = append(rec.Rationale, fmt.Sprintf("%s GPU with %s VRAM is smaller than the model: partial offload ~%d layers, ctx=%d%s bounded by the host-RAM KV budget (KV split host/device)", host.GPU.Backend, humanGiB(host.GPU.VRAMBytes), layers, ctx, kvNote(kt)))
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
