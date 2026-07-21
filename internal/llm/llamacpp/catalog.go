/*
catalog.go holds a curated, offline fallback ladder of abliterated GGUF models
ordered by memory footprint. Abliterated (refusal-removed) models are this app's
primary target, so when the fit gate refuses the requested model on this host,
the agent should still be able to point the user at a SMALLER abliterated model
that does fit, rather than only reporting failure.

The ladder is deliberately static and offline: suggestion happens on the failure
path, where a network round-trip would be the wrong thing to do. Every entry is
ADVISORY — the normal fit gate and downloader re-validate the suggested model
(existence, real artifact sizes, live host memory) when the user actually pulls
it, so a stale or renamed ref fails safely with the ordinary error instead of
silently doing the wrong thing.

Footprint math is trusted only for DENSE, text-only models: mixture-of-experts
weights and vision mmproj projectors do not follow the params->bytes rule below,
so such models are excluded from this ladder to avoid mis-ranking. Users can
extend the ladder with their own abliterated GGUF refs via config.
*/
package llamacpp

import "sort"

/*
Modality guards suggestions: a text rejection must not be answered with a vision
model (or vice versa). The v1 ladder is text-only; the field exists so a future
vision ladder cannot be cross-suggested by accident.
*/
type Modality string

const (
	ModalityText   Modality = "text"
	ModalityVision Modality = "vision"
)

/*
AbliteratedModel is one curated fallback candidate. ApproxBytes is a conservative
Q4_K_M on-disk estimate (~0.6 GiB per billion dense params, i.e. ~4.8 bits per
weight) used only for RANKING and for the fit re-check; ParamsB is shown to the
user, who thinks in parameter count. Ref is a downloader ref ([hf.co/]user/name).
*/
type AbliteratedModel struct {
	Ref         string   `json:"ref"`
	ParamsB     float64  `json:"params_b"`
	ApproxBytes uint64   `json:"approx_bytes"`
	Modality    Modality `json:"modality"`
	/*
		CodeOptimized marks models trained/tuned for coding. Auto-pick prefers
		them over same-size generalists because this agent's primary workload is
		software engineering; the advisory suggestion path ignores the flag and
		keeps recommending purely by size.
	*/
	CodeOptimized bool   `json:"code_optimized,omitempty"`
	Note          string `json:"note,omitempty"`
}

/*
defaultAbliteratedLadder is the built-in fallback ladder, dense text-only,
descending by footprint. Refs are grounded on this repo's pinned abliterated
config (Qwythos-9B) and the huihui-ai abliterated GGUF line; all are advisory and
re-validated at pull. Extend via LlamaCppConfig.FallbackModels, not by editing
this list, so local preferences survive upgrades.
*/
var defaultAbliteratedLadder = []AbliteratedModel{
	{Ref: "bartowski/Qwen2.5-Coder-32B-Instruct-abliterated-GGUF", ParamsB: 32, ApproxBytes: 20 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	{Ref: "bartowski/huihui-ai_Qwen3-14B-abliterated-GGUF", ParamsB: 14, ApproxBytes: 9 * byteGiB, Modality: ModalityText},
	{Ref: "bartowski/Qwen2.5-Coder-14B-Instruct-abliterated-GGUF", ParamsB: 14, ApproxBytes: 9 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	{Ref: "huihui-ai/Huihui-Qwythos-9B-Claude-Mythos-5-1M-abliterated-GGUF", ParamsB: 9, ApproxBytes: 6 * byteGiB, Modality: ModalityText},
	{Ref: "DevQuasar/huihui-ai.Qwen3-8B-abliterated-GGUF", ParamsB: 8, ApproxBytes: 5 * byteGiB, Modality: ModalityText},
	{Ref: "bartowski/Qwen2.5-Coder-7B-Instruct-abliterated-GGUF", ParamsB: 7, ApproxBytes: 5 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	{Ref: "huihui-ai/Huihui-Qwen3-4B-Instruct-2507-abliterated-GGUF", ParamsB: 4, ApproxBytes: 3 * byteGiB, Modality: ModalityText, Note: "advisory ref; verified at pull"},
	{Ref: "bartowski/Qwen2.5-Coder-3B-Instruct-abliterated-GGUF", ParamsB: 3, ApproxBytes: 2 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	{Ref: "huihui-ai/Huihui-Qwen3-1.7B-abliterated-GGUF", ParamsB: 1.7, ApproxBytes: 2 * byteGiB, Modality: ModalityText, Note: "advisory ref; verified at pull"},
	{Ref: "bartowski/Qwen2.5-Coder-1.5B-Instruct-abliterated-GGUF", ParamsB: 1.5, ApproxBytes: 1 * byteGiB, Modality: ModalityText, CodeOptimized: true},
}

/*
Suggestion is advisory guidance attached to a rejected FitReport: a smaller
abliterated model that the fit re-check believes will run on this host. Fits is
false when even the smallest candidate did not fit — the suggestion is still the
best available option, and the note says so.
*/
type Suggestion struct {
	Model          AbliteratedModel `json:"model"`
	EstimatedBytes uint64           `json:"estimated_bytes"`
	Fits           bool             `json:"fits"`
	Note           string           `json:"note"`
}

/*
suggestSmaller picks the LARGEST abliterated model from the ladder that (a) is
strictly smaller in footprint than the rejected model and (b) passes the fit gate
on the same host and runtime shape. If none fit, it returns the smallest
candidate with Fits=false so the caller can still offer a best effort. It returns
(_, false) only when the ladder has no strictly-smaller candidate at all.

It re-uses EstimateFit for the fit re-check, disabling further suggestion in that
nested call so the evaluation cannot recurse.
*/
/*
smallerCandidates filters the ladder to entries usable as a replacement for the
rejected model: same modality, strictly smaller footprint, sorted descending so
callers walk from the most capable candidate down.

Conservative modality heuristic: a bundled mmproj means "treat as
vision-capable" and the text-only v1 ladder declines to cross-suggest. This
can suppress a useful text downgrade for a multimodal bundle run text-only
(a false negative), which is preferred over suggesting a wrong-modality
model. Refine to the request's actual text-vs-vision intent if that signal
becomes available.
*/
func smallerCandidates(report FitReport, ladder []AbliteratedModel) []AbliteratedModel {
	wantModality := ModalityText
	if report.MMProjBytes > 0 {
		wantModality = ModalityVision
	}

	candidates := make([]AbliteratedModel, 0, len(ladder))
	for _, m := range ladder {
		if m.Modality != wantModality {
			continue
		}
		if m.ApproxBytes >= report.ModelBytes {
			continue
		}
		candidates = append(candidates, m)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ApproxBytes > candidates[j].ApproxBytes })
	return candidates
}

/* candidateFitRequest rebuilds the rejected request's shape around a candidate. */
func candidateFitRequest(report FitReport, m AbliteratedModel) FitRequest {
	return FitRequest{
		ModelBytes:    m.ApproxBytes,
		ContextTokens: report.ContextTokens,
		Parallel:      report.Parallel,
		KVCacheType:   report.KVCacheType,
	}
}

func suggestSmaller(report FitReport, ladder []AbliteratedModel, policy FitPolicy) (Suggestion, bool) {
	if report.ModelBytes == 0 {
		return Suggestion{}, false
	}
	candidates := smallerCandidates(report, ladder)
	if len(candidates) == 0 {
		return Suggestion{}, false
	}

	probe := policy
	probe.SuggestFallback = false
	for _, m := range candidates {
		rep := EstimateFitWithPolicy(report.Host, candidateFitRequest(report, m), probe)
		if rep.Fits {
			return Suggestion{
				Model:          m,
				EstimatedBytes: rep.EstimatedRuntimeBytes,
				Fits:           true,
				Note:           "smaller abliterated model that fits this host; the fit gate re-checks it at pull",
			}, true
		}
	}

	smallest := candidates[len(candidates)-1]
	rep := EstimateFitWithPolicy(report.Host, candidateFitRequest(report, smallest), probe)
	return Suggestion{
		Model:          smallest,
		EstimatedBytes: rep.EstimatedRuntimeBytes,
		Fits:           false,
		Note:           "smallest curated abliterated model; may still not fit — free memory or lower context",
	}, true
}

/*
AutoPick selects the replacement model the agent should LAUNCH after the fit
gate refused the requested one. Unlike suggestSmaller — advisory, allowed to
return a best-effort candidate that may not fit — AutoPick only returns a
candidate the fit gate passes on the rejected report's host and runtime shape,
because the caller starts it with no human in the loop. Code-optimized
candidates are preferred over generalists regardless of size (this agent's
primary workload is software engineering); within each class the largest
fitting candidate wins. A nil ladder means the built-in one.

The pick stays advisory in the same sense as the suggestion path: the normal
downloader and fit gate re-validate the ref (existence, real artifact sizes,
live host memory) when the caller actually pulls and starts it.
*/
func AutoPick(report FitReport, ladder []AbliteratedModel, policy FitPolicy) (Suggestion, bool) {
	if report.ModelBytes == 0 {
		return Suggestion{}, false
	}
	if ladder == nil {
		ladder = defaultAbliteratedLadder
	}
	candidates := smallerCandidates(report, ladder)

	probe := policy
	probe.SuggestFallback = false
	for _, preferCode := range []bool{true, false} {
		for _, m := range candidates {
			if m.CodeOptimized != preferCode {
				continue
			}
			/*
				Viability is checked at the tuner's context floor, not at the rejected
				report's context: that context was chosen (or operator-pinned) FOR the
				rejected model, and after substitution the tuner re-derives ctx/KV for
				the replacement via the shrink-to-fit ladder whose floor this is. Reusing
				the big model's context here would wrongly disqualify every candidate on
				hosts where only the KV reserve overflowed. The launch-time fit gate
				remains the final authority at the actually-tuned context.
			*/
			req := candidateFitRequest(report, m)
			req.ContextTokens = minRecommendedCtx
			rep := EstimateFitWithPolicy(report.Host, req, probe)
			if rep.Fits {
				return Suggestion{
					Model:          m,
					EstimatedBytes: rep.EstimatedRuntimeBytes,
					Fits:           true,
					Note:           "auto-picked: largest abliterated model that fits this host, code-optimized preferred",
				}, true
			}
		}
	}
	return Suggestion{}, false
}
