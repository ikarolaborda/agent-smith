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
	if rep.Suggestion.Model.ParamsB != 1.7 {
		t.Fatalf("expected the smallest candidate (1.7B) as best effort, got %.1fB", rep.Suggestion.Model.ParamsB)
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
