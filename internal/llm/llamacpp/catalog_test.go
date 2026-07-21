package llamacpp

import "testing"

/*
hostWith builds a synthetic host with generous disk so only the memory gate
decides fit, isolating the suggestion behaviour under test.
*/
func hostWith(totalGiB, availGiB uint64) HostProfile {
	return HostProfile{
		OS:                   "linux",
		Arch:                 "amd64",
		TotalMemoryBytes:     totalGiB * byteGiB,
		AvailableMemoryBytes: availGiB * byteGiB,
		FreeDiskBytes:        500 * byteGiB,
		DiskPath:             "/",
	}
}

func TestFitRejectSuggestsLargestFittingSmallerModel(t *testing.T) {
	/*
		Requested ~12 GiB model on a 20/12 GiB host: the budget (~11 GiB) seats the
		9B (~6 GiB) but not the larger 14B ladder entry (~9 GiB), so the suggestion
		must SKIP the too-big candidate and pick the largest one that actually fits.
	*/
	host := hostWith(20, 12)
	rep := EstimateFit(host, FitRequest{ModelBytes: 12 * byteGiB, ContextTokens: 4096})
	if rep.Fits {
		t.Fatalf("expected reject on a 20GiB host for a ~12GiB model")
	}
	if rep.Suggestion == nil {
		t.Fatal("expected a smaller-abliterated suggestion on reject")
	}
	if !rep.Suggestion.Fits {
		t.Fatalf("expected the suggestion to fit, got note %q", rep.Suggestion.Note)
	}
	if rep.Suggestion.Model.ApproxBytes >= 12*byteGiB {
		t.Fatalf("suggestion must be strictly smaller than the rejected model, got %d bytes", rep.Suggestion.Model.ApproxBytes)
	}
	if got := rep.Suggestion.Model.ParamsB; got != 9 {
		t.Fatalf("expected the largest fitting smaller model (9B), got %.1fB (%s)", got, rep.Suggestion.Model.Ref)
	}
}

func TestFitRejectTinyHostSuggestsSmallestWithHonestNote(t *testing.T) {
	/* A host too small for even the 1.7B tier: best-effort smallest, Fits=false. */
	host := hostWith(6, 1)
	rep := EstimateFit(host, FitRequest{ModelBytes: 9 * byteGiB, ContextTokens: 4096})
	if rep.Fits {
		t.Fatal("expected reject on a 6GiB host")
	}
	if rep.Suggestion == nil {
		t.Fatal("expected a best-effort suggestion even when nothing fits")
	}
	if rep.Suggestion.Fits {
		t.Fatal("expected Fits=false when even the smallest candidate does not fit")
	}
	if rep.Suggestion.Model.ParamsB != 1.5 {
		t.Fatalf("expected the smallest candidate (1.5B coder) as best effort, got %.1fB", rep.Suggestion.Model.ParamsB)
	}
}

func TestFitAcceptCarriesNoSuggestion(t *testing.T) {
	host := hostWith(64, 48)
	rep := EstimateFit(host, FitRequest{ModelBytes: 5 * byteGiB, ContextTokens: 4096})
	if !rep.Fits {
		t.Fatalf("expected a 5GiB model to fit a 64GiB host, reasons: %v", rep.Reasons)
	}
	if rep.Suggestion != nil {
		t.Fatal("a fitting model must not carry a downgrade suggestion")
	}
}

func TestSuggestionNeverExceedsRejectedFootprint(t *testing.T) {
	/* Rejecting a model already smaller than the whole ladder yields no suggestion. */
	host := hostWith(4, 1)
	rep := EstimateFit(host, FitRequest{ModelBytes: 1 * byteGiB, ContextTokens: 4096})
	if rep.Fits {
		t.Fatal("expected reject on a 4GiB host")
	}
	if rep.Suggestion != nil {
		t.Fatalf("no ladder entry is smaller than a 1GiB model; got %s", rep.Suggestion.Model.Ref)
	}
}

func TestSuggestFallbackPolicyOff(t *testing.T) {
	host := hostWith(16, 9)
	policy := DefaultFitPolicy()
	policy.SuggestFallback = false
	rep := EstimateFitWithPolicy(host, FitRequest{ModelBytes: 9 * byteGiB, ContextTokens: 4096}, policy)
	if rep.Fits {
		t.Fatal("expected reject")
	}
	if rep.Suggestion != nil {
		t.Fatal("suggestion must be absent when the policy disables it")
	}
}

func TestAutoPickPrefersCodeOptimizedOverLargerGeneralist(t *testing.T) {
	/*
		Budget seats both the 9B generalist (~6 GiB) and the 7B coder (~5 GiB).
		Advisory suggestion picks purely by size (9B), but AutoPick must prefer
		the code-optimized 7B even though it is smaller, because the pick is
		launched for a coding workload with no human in the loop.
	*/
	host := hostWith(20, 12)
	rep := EstimateFit(host, FitRequest{ModelBytes: 12 * byteGiB, ContextTokens: 4096})
	if rep.Fits {
		t.Fatal("expected reject")
	}
	pick, ok := AutoPick(rep, nil, DefaultFitPolicy())
	if !ok {
		t.Fatal("expected an auto-pick on a host that seats several candidates")
	}
	if !pick.Fits {
		t.Fatal("AutoPick must never return a non-fitting candidate")
	}
	if !pick.Model.CodeOptimized {
		t.Fatalf("expected a code-optimized pick, got %s", pick.Model.Ref)
	}
	if pick.Model.ParamsB != 7 {
		t.Fatalf("expected the largest fitting coder (7B), got %.1fB (%s)", pick.Model.ParamsB, pick.Model.Ref)
	}
}

func TestAutoPickFallsBackToGeneralistWhenNoCoderFits(t *testing.T) {
	/*
		A coder-free ladder slice: with only generalists strictly smaller than the
		rejected model, AutoPick must still return the largest fitting one rather
		than nothing — code preference is a ranking, not a hard filter.
	*/
	ladder := []AbliteratedModel{
		{Ref: "x/general-9b", ParamsB: 9, ApproxBytes: 6 * byteGiB, Modality: ModalityText},
		{Ref: "x/coder-huge", ParamsB: 20, ApproxBytes: 13 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	}
	host := hostWith(20, 12)
	rep := EstimateFit(host, FitRequest{ModelBytes: 12 * byteGiB, ContextTokens: 4096})
	pick, ok := AutoPick(rep, ladder, DefaultFitPolicy())
	if !ok {
		t.Fatal("expected a generalist fallback pick")
	}
	if pick.Model.Ref != "x/general-9b" {
		t.Fatalf("expected the fitting generalist, got %s", pick.Model.Ref)
	}
}

func TestAutoPickRefusesWhenNothingFits(t *testing.T) {
	/*
		Unlike the advisory suggestion (which returns a best-effort Fits=false
		candidate), AutoPick must return ok=false when no candidate passes the
		gate: it must never hand the caller a model to LAUNCH that is already
		known not to fit.
	*/
	host := hostWith(6, 1)
	rep := EstimateFit(host, FitRequest{ModelBytes: 9 * byteGiB, ContextTokens: 4096})
	if _, ok := AutoPick(rep, nil, DefaultFitPolicy()); ok {
		t.Fatal("AutoPick must refuse when even the smallest candidate does not fit")
	}
}

func TestAutoPickRespectsModality(t *testing.T) {
	/* A vision rejection must not be answered by the text-only ladder. */
	host := hostWith(16, 9)
	rep := EstimateFit(host, FitRequest{ModelBytes: 9 * byteGiB, MMProjBytes: 1 * byteGiB, ContextTokens: 4096})
	if _, ok := AutoPick(rep, nil, DefaultFitPolicy()); ok {
		t.Fatal("text-only ladder must not answer a vision rejection")
	}
}

func TestAutoPickSurvivesOperatorContextPinnedForTheRejectedModel(t *testing.T) {
	/*
		Regression for the motivating failure: a 24 GiB host with ~9 GiB available
		rejects an ~18 GiB model at an operator-pinned ctx of 16384 (KV reserve
		alone is 8 GiB). Judged at that inherited context nothing fits — but the
		pin belonged to the rejected model and the tuner re-derives ctx for the
		substitute, so AutoPick must judge candidates at the ctx floor and still
		produce the largest fitting coder instead of giving up.
	*/
	host := hostWith(24, 9)
	rep := EstimateFit(host, FitRequest{ModelBytes: 18 * byteGiB, ContextTokens: 16384})
	if rep.Fits {
		t.Fatal("expected reject")
	}
	pick, ok := AutoPick(rep, nil, DefaultFitPolicy())
	if !ok {
		t.Fatal("expected a pick despite the rejected model's oversized context pin")
	}
	if !pick.Model.CodeOptimized || pick.Model.ParamsB != 7 {
		t.Fatalf("expected the 7B coder, got %.1fB (%s)", pick.Model.ParamsB, pick.Model.Ref)
	}
}

func TestAutoPickOnlyProposesStrictlySmallerModels(t *testing.T) {
	/*
		The retry loop's termination proof rests on monotonicity: every pick is
		strictly smaller than the ref that just failed, so exercise the boundary
		where a same-size candidate exists and must be skipped.
	*/
	ladder := []AbliteratedModel{
		{Ref: "x/same-size", ParamsB: 9, ApproxBytes: 9 * byteGiB, Modality: ModalityText, CodeOptimized: true},
		{Ref: "x/smaller", ParamsB: 3, ApproxBytes: 2 * byteGiB, Modality: ModalityText, CodeOptimized: true},
	}
	host := hostWith(20, 12)
	rep := EstimateFit(host, FitRequest{ModelBytes: 9 * byteGiB, ContextTokens: 4096})
	pick, ok := AutoPick(rep, ladder, DefaultFitPolicy())
	if !ok {
		t.Fatal("expected the strictly smaller candidate to be picked")
	}
	if pick.Model.Ref != "x/smaller" {
		t.Fatalf("a candidate the same size as the rejected model must be skipped, got %s", pick.Model.Ref)
	}
}

func TestSuggestionRespectsModality(t *testing.T) {
	/* A vision rejection (MMProjBytes>0) must not be answered by the text ladder. */
	host := hostWith(16, 9)
	rep := EstimateFit(host, FitRequest{ModelBytes: 9 * byteGiB, MMProjBytes: 1 * byteGiB, ContextTokens: 4096})
	if rep.Fits {
		t.Fatal("expected reject")
	}
	if rep.Suggestion != nil {
		t.Fatalf("text-only ladder must not answer a vision rejection, got %s", rep.Suggestion.Model.Ref)
	}
}
