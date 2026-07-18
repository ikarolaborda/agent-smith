package llamacpp

import (
	"fmt"
	"math"
)

const (
	byteGiB                 = uint64(1024 * 1024 * 1024)
	defaultContextTokens    = 4096
	defaultParallelRequests = 1
	defaultKVBytesPerToken  = uint64(512 * 1024)
)

/*
KVCacheType selects the llama.cpp KV cache element type (--cache-type-k/-v). An
empty value means the f16 default, so the zero value preserves current behavior.
Quantizing the KV cache is a Stage-3 shrink-to-fit lever: it lowers the per-token
KV reserve so a larger context — or a model that would otherwise be refused —
fits the memory budget, at a small quality cost.
*/
type KVCacheType string

const (
	KVCacheF16  KVCacheType = "f16"
	KVCacheQ8_0 KVCacheType = "q8_0"
	KVCacheQ4_0 KVCacheType = "q4_0"
)

/*
kvCacheSixteenths returns the per-element KV size of a cache type as a fraction
of f16, in sixteenths. Derived from llama.cpp block sizes (context7-verified):
f16 is 2 B/elem; q8_0 is 34 B/32 = 1.0625 B/elem (0.531×); q4_0 is 18 B/32 =
0.5625 B/elem (0.281×). Each ratio is rounded UP to the nearest sixteenth so the
safety gate can only over-reserve, never under-reserve: q8_0→9/16, q4_0→5/16.
*/
func kvCacheSixteenths(t KVCacheType) uint64 {
	switch t {
	case KVCacheQ8_0:
		return 9
	case KVCacheQ4_0:
		return 5
	default:
		/* f16 and the empty (unset) value both use the full-precision baseline. */
		return 16
	}
}

/*
kvBytesPerToken scales the combined K+V f16 per-token reserve (base) by the cache
type. Callers MUST feed the same KVCacheType here that they pass to buildArgs, or
the fit estimate and the launched server would disagree on KV size.
*/
func kvBytesPerToken(base uint64, t KVCacheType) uint64 {
	return base * kvCacheSixteenths(t) / 16
}

// FitDecision is deliberately binary. Unknown sizes or host capacity are a
// rejection; callers must never interpret missing data as permission to run.
type FitDecision string

const (
	FitDecisionFit    FitDecision = "fit"
	FitDecisionReject FitDecision = "reject"
)

// FitRequest contains the inputs that materially affect conservative local
// inference sizing. DownloadBytes is the number of artifact bytes not already
// present in a size-validated cache.
type FitRequest struct {
	ModelBytes    uint64 `json:"model_bytes"`
	MMProjBytes   uint64 `json:"mmproj_bytes,omitempty"`
	DownloadBytes uint64 `json:"download_bytes,omitempty"`
	ContextTokens int    `json:"context_tokens"`
	Parallel      int    `json:"parallel"`
	/*
		GPULayers > 0 means weights/KV are offloaded to a discrete GPU, so they
		are admitted against VRAMBytes instead of only system RAM. A full offload
		(GPULayers >= fullOffloadLayers) must fit weights+KV in the VRAM budget.
		GPUUnified marks Apple-style shared memory, where the GPU has no separate
		bank and the existing system-RAM path already applies.
	*/
	GPULayers  int    `json:"gpu_layers,omitempty"`
	VRAMBytes  uint64 `json:"vram_bytes,omitempty"`
	GPUUnified bool   `json:"gpu_unified,omitempty"`
	/*
		KVCacheType selects the KV cache element type used both for the reserve
		estimate here and for the launched --cache-type-k/-v. Empty means f16.
	*/
	KVCacheType KVCacheType `json:"kv_cache_type,omitempty"`
	/*
		ReclaimableSelfBytes is memory the agent can free from its own prior model
		before this one loads (Stage 2). It is credited to the available budget
		because it is currently resident (not counted as available) yet is part of
		total RAM. It is a PREVIEW / model-swap-before-reclaim input: the live
		Start path reclaims and then re-measures, so it passes zero here to avoid
		double-counting freed memory the fresh measurement already reflects.
	*/
	ReclaimableSelfBytes uint64 `json:"reclaimable_self_bytes,omitempty"`
}

// FitPolicy holds deliberately conservative approximation constants. GGUF
// metadata is not sufficient to predict every backend allocation: graph
// scratch space, KV data type, flash attention, offload split, and llama.cpp
// version all matter. These estimates therefore reserve headroom and are a
// safety gate, not a performance or throughput promise.
type FitPolicy struct {
	KVBytesPerToken       uint64
	MinimumOSReserveBytes uint64
	MinimumFreeHeadroom   uint64
	MinimumScratchBytes   uint64
	MinimumDiskReserve    uint64
	/*
		SuggestFallback, when true, makes a rejecting report carry an advisory
		smaller-abliterated-model suggestion. FallbackLadder overrides the built-in
		ladder; nil uses defaultAbliteratedLadder. The nested fit re-check disables
		this so suggestion evaluation cannot recurse.
	*/
	SuggestFallback bool
	FallbackLadder  []AbliteratedModel
}

// DefaultFitPolicy returns the production fail-safe sizing policy.
func DefaultFitPolicy() FitPolicy {
	return FitPolicy{
		KVBytesPerToken:       defaultKVBytesPerToken,
		MinimumOSReserveBytes: 4 * byteGiB,
		MinimumFreeHeadroom:   1 * byteGiB,
		MinimumScratchBytes:   1 * byteGiB,
		MinimumDiskReserve:    2 * byteGiB,
		SuggestFallback:       true,
	}
}

// FitReport explains both the decision and every capacity term used to make
// it, so a CLI or UI can show why a model was refused before downloading it.
type FitReport struct {
	Decision              FitDecision `json:"decision"`
	Fits                  bool        `json:"fits"`
	Host                  HostProfile `json:"host"`
	ModelBytes            uint64      `json:"model_bytes"`
	MMProjBytes           uint64      `json:"mmproj_bytes,omitempty"`
	EstimatedKVBytes      uint64      `json:"estimated_kv_bytes"`
	EstimatedScratchBytes uint64      `json:"estimated_scratch_bytes"`
	EstimatedRuntimeBytes uint64      `json:"estimated_runtime_bytes"`
	OSReserveBytes        uint64      `json:"os_reserve_bytes"`
	AvailableMemoryBudget uint64      `json:"available_memory_budget"`
	ReclaimableSelfBytes  uint64      `json:"reclaimable_self_bytes,omitempty"`
	KVCacheType           KVCacheType `json:"kv_cache_type,omitempty"`
	VRAMBudgetBytes       uint64      `json:"vram_budget_bytes,omitempty"`
	GPUOffload            bool        `json:"gpu_offload,omitempty"`
	RequiredDownloadBytes uint64      `json:"required_download_bytes"`
	RequiredDiskBytes     uint64      `json:"required_disk_bytes"`
	FreeDiskBytes         uint64      `json:"free_disk_bytes"`
	ContextTokens         int         `json:"context_tokens"`
	Parallel              int         `json:"parallel"`
	Reasons               []string    `json:"reasons,omitempty"`
	EstimationLimitations []string    `json:"estimation_limitations"`
	/*
		Suggestion is set only on a rejecting report when the policy enables it: a
		smaller abliterated model the fit re-check believes will run here. Advisory —
		re-validated by the fit gate and downloader at pull.
	*/
	Suggestion *Suggestion `json:"suggestion,omitempty"`
}

// EstimateFit applies DefaultFitPolicy.
func EstimateFit(host HostProfile, req FitRequest) FitReport {
	return EstimateFitWithPolicy(host, req, DefaultFitPolicy())
}

// EstimateFitWithPolicy is pure and deterministic, which makes safety
// decisions straightforward to test against synthetic hosts.
func EstimateFitWithPolicy(host HostProfile, req FitRequest, policy FitPolicy) FitReport {
	if policy.KVBytesPerToken == 0 {
		policy = DefaultFitPolicy()
	}
	ctx := req.ContextTokens
	if ctx <= 0 {
		ctx = defaultContextTokens
	}
	parallel := req.Parallel
	if parallel <= 0 {
		parallel = defaultParallelRequests
	}
	r := FitReport{
		Decision:              FitDecisionReject,
		Host:                  host,
		ModelBytes:            req.ModelBytes,
		MMProjBytes:           req.MMProjBytes,
		RequiredDownloadBytes: req.DownloadBytes,
		FreeDiskBytes:         host.FreeDiskBytes,
		ContextTokens:         ctx,
		Parallel:              parallel,
		ReclaimableSelfBytes:  req.ReclaimableSelfBytes,
		KVCacheType:           req.KVCacheType,
		EstimationLimitations: []string{
			"KV size is a conservative architecture-independent estimate; layer count, GQA, KV type, and flash attention can change actual use",
			"llama.cpp graph, allocator, driver, and backend versions can consume more memory than model file size",
			"the report is a launch safety gate, not a guarantee of throughput or model compatibility",
		},
	}

	if req.ModelBytes == 0 {
		r.Reasons = append(r.Reasons, "model size is unknown or zero")
	}
	if host.OS == "" || host.Arch == "" {
		r.Reasons = append(r.Reasons, "host operating system or architecture is unknown")
	}
	if host.TotalMemoryBytes == 0 || host.AvailableMemoryBytes == 0 {
		r.Reasons = append(r.Reasons, "host total or currently available memory is unknown")
	}
	if host.FreeDiskBytes == 0 {
		r.Reasons = append(r.Reasons, "host free disk space is unknown")
	}

	artifacts, overflow := add64(req.ModelBytes, req.MMProjBytes)
	if overflow {
		r.Reasons = append(r.Reasons, "artifact size overflow")
		return r
	}
	// Weight mappings and runtime structures frequently exceed file size. Add
	// 15% before backend scratch and KV allocations.
	weightOverhead := artifacts / 100 * 15
	scratch := max(policy.MinimumScratchBytes, artifacts/10)
	/*
		The KV reserve scales with the cache element type. policy.KVBytesPerToken
		is the combined K+V per-token reserve at f16; kvBytesPerToken applies the
		quantized fraction. This MUST match the --cache-type-k/-v the runtime
		launches with (buildArgsArtifacts) or the gate and server disagree.
	*/
	perTokenKV := kvBytesPerToken(policy.KVBytesPerToken, req.KVCacheType)
	kv, kvOverflow := mul64(uint64(ctx), uint64(parallel))
	if !kvOverflow {
		kv, kvOverflow = mul64(kv, perTokenKV)
	}
	r.EstimatedKVBytes = kv
	r.EstimatedScratchBytes = scratch
	runtimeBytes, runtimeOverflow := sum64(artifacts, weightOverhead, scratch, kv)
	if kvOverflow || runtimeOverflow {
		r.Reasons = append(r.Reasons, "runtime memory estimate overflow")
		return r
	}
	r.EstimatedRuntimeBytes = runtimeBytes

	osReserve := max(policy.MinimumOSReserveBytes, host.TotalMemoryBytes/8)
	r.OSReserveBytes = osReserve
	totalBudget := saturatingSub(host.TotalMemoryBytes, osReserve)
	currentHeadroom := max(policy.MinimumFreeHeadroom, host.TotalMemoryBytes/32)
	/*
		Stage 2: credit memory the agent will free from its own prior model to the
		AVAILABLE term only. That memory is resident now (so absent from Available)
		yet part of Total, so the min(totalBudget, …) cap below still bounds the
		result by Total−osReserve — crediting it can never admit beyond physical
		RAM. Crediting totalBudget instead would exceed physical memory.
	*/
	effectiveAvailable, availOverflow := add64(host.AvailableMemoryBytes, req.ReclaimableSelfBytes)
	if availOverflow {
		effectiveAvailable = math.MaxUint64
	}
	availableBudget := saturatingSub(effectiveAvailable, currentHeadroom)
	r.AvailableMemoryBudget = min(totalBudget, availableBudget)
	if runtimeBytes > r.AvailableMemoryBudget {
		r.Reasons = append(r.Reasons, fmt.Sprintf(
			"estimated runtime memory %d bytes exceeds safe currently available budget %d bytes",
			runtimeBytes, r.AvailableMemoryBudget,
		))
	}

	/*
		Discrete-GPU offload admission. When weights/KV are placed on a dedicated
		GPU (non-unified), a full offload must fit weights + KV inside the VRAM
		budget (device memory minus a driver/compute reserve). This is what makes
		a discrete accelerator usable: the system-RAM gate above still applies to
		host-side scratch and any non-offloaded remainder, but the weights no
		longer have to fit in system RAM.
	*/
	/*
		A full offload requested for a discrete GPU whose VRAM could not be
		detected must fail closed: without a VRAM budget there is no evidence the
		weights fit the device, and admitting it on RAM alone would let a model
		too large for the card start and then thrash or crash at runtime.
	*/
	if req.GPULayers >= fullOffloadLayers && !req.GPUUnified && req.VRAMBytes == 0 {
		r.GPUOffload = true
		r.Reasons = append(r.Reasons, "full GPU offload requested but no VRAM was detected for the discrete GPU; refusing without a VRAM budget")
	}

	if req.GPULayers > 0 && req.VRAMBytes > 0 && !req.GPUUnified {
		r.GPUOffload = true
		vramReserve := max(512*byteGiB/1024, req.VRAMBytes/16) // 512 MiB or 1/16 of VRAM
		r.VRAMBudgetBytes = saturatingSub(req.VRAMBytes, vramReserve)
		if req.GPULayers >= fullOffloadLayers {
			weightOnGPU, wOverflow := add64(artifacts, weightOverhead)
			needVRAM, nOverflow := add64(weightOnGPU, kv)
			if wOverflow || nOverflow {
				r.Reasons = append(r.Reasons, "VRAM requirement overflow")
			} else if needVRAM > r.VRAMBudgetBytes {
				r.Reasons = append(r.Reasons, fmt.Sprintf(
					"full GPU offload needs %d bytes of VRAM (weights+KV) but the safe VRAM budget is %d bytes",
					needVRAM, r.VRAMBudgetBytes,
				))
			}
		}
	}

	if req.DownloadBytes > 0 {
		diskReserve := max(policy.MinimumDiskReserve, artifacts/20)
		requiredDisk, diskOverflow := add64(req.DownloadBytes, diskReserve)
		if diskOverflow {
			r.Reasons = append(r.Reasons, "disk requirement overflow")
		} else {
			r.RequiredDiskBytes = requiredDisk
			if requiredDisk > host.FreeDiskBytes {
				r.Reasons = append(r.Reasons, fmt.Sprintf(
					"download plus disk reserve requires %d bytes but only %d bytes are free",
					requiredDisk, host.FreeDiskBytes,
				))
			}
		}
	}

	if len(r.Reasons) == 0 {
		r.Decision = FitDecisionFit
		r.Fits = true
	}

	/*
		A rejected model gets an advisory smaller-abliterated suggestion so the
		failure path still points somewhere runnable. Gated by policy and disabled
		inside the nested re-check (see suggestSmaller) so it cannot recurse.
	*/
	if !r.Fits && policy.SuggestFallback {
		ladder := policy.FallbackLadder
		if ladder == nil {
			ladder = defaultAbliteratedLadder
		}
		if s, ok := suggestSmaller(r, ladder, policy); ok {
			r.Suggestion = &s
		}
	}
	return r
}

func add64(a, b uint64) (uint64, bool) {
	if math.MaxUint64-a < b {
		return 0, true
	}
	return a + b, false
}

func sum64(values ...uint64) (uint64, bool) {
	var total uint64
	for _, v := range values {
		var overflow bool
		total, overflow = add64(total, v)
		if overflow {
			return 0, true
		}
	}
	return total, false
}

func mul64(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, true
	}
	return a * b, false
}

func saturatingSub(a, b uint64) uint64 {
	if b >= a {
		return 0
	}
	return a - b
}
