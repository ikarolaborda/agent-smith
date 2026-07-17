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
	GPULayers int        `json:"gpu_layers"`
	CtxSize   int        `json:"ctx_size"`
	Backend   GPUBackend `json:"backend"`
	FullGPU   bool       `json:"full_gpu_offload"`
	Rationale []string   `json:"rationale"`
}

/*
fullOffloadLayers is the sentinel "offload every layer" value. llama.cpp clamps
-ngl to the model's actual layer count, so a large number means "all layers".
*/
const fullOffloadLayers = 99

/* ctxLadder is the set of context sizes the tuner will pick from, largest-first. */
var ctxLadder = []int{32768, 16384, 8192, 4096, 2048}

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
	scratch := max64(policy.MinimumScratchBytes, artifacts/10)

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
		ctx := largestCtxWithin(ramBudget, weights+scratch)
		if ctx == 0 {
			ctx = 2048
		}
		rec.GPULayers = 0
		rec.CtxSize = ctx
		rec.Backend = GPUBackendNone
		rec.Rationale = append(rec.Rationale, fmt.Sprintf("no usable GPU detected; CPU inference with ctx=%d bounded by the system-RAM safe budget", ctx))
		return rec
	}

	/* Apple / unified memory: offload fully; the GPU shares the RAM budget. */
	if host.GPU.Unified {
		ramBudget := ramSafeBudget(host, policy)
		ctx := largestCtxWithin(ramBudget, weights+scratch)
		if ctx == 0 {
			ctx = 2048
		}
		rec.GPULayers = fullOffloadLayers
		rec.CtxSize = ctx
		rec.FullGPU = true
		rec.Rationale = append(rec.Rationale, fmt.Sprintf("unified-memory GPU (%s): full offload, ctx=%d bounded by shared RAM budget", host.GPU.Backend, ctx))
		return rec
	}

	/* Discrete GPU: weights + KV must fit dedicated VRAM to offload fully. */
	vramReserve := max64(512*1024*1024, host.GPU.VRAMBytes/16) // leave headroom for the driver/compute
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
	osReserve := max64(policy.MinimumOSReserveBytes, host.TotalMemoryBytes/8)
	totalBudget := saturatingSub(host.TotalMemoryBytes, osReserve)
	headroom := max64(policy.MinimumFreeHeadroom, host.TotalMemoryBytes/32)
	availBudget := saturatingSub(host.AvailableMemoryBytes, headroom)
	return min64(totalBudget, availBudget)
}

func humanGiB(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/float64(byteGiB))
}
