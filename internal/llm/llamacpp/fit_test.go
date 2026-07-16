package llamacpp

import "testing"

func TestEstimateFitUsesCurrentAvailabilityAndContext(t *testing.T) {
	host := HostProfile{
		OS: "linux", Arch: "amd64", TotalMemoryBytes: 64 * byteGiB,
		AvailableMemoryBytes: 24 * byteGiB, FreeDiskBytes: 100 * byteGiB,
	}
	small := EstimateFit(host, FitRequest{ModelBytes: 4 * byteGiB, ContextTokens: 4096, Parallel: 1})
	if !small.Fits {
		t.Fatalf("expected small model to fit: %v", small.Reasons)
	}
	largeContext := EstimateFit(host, FitRequest{ModelBytes: 4 * byteGiB, ContextTokens: 32768, Parallel: 2})
	if largeContext.Fits || largeContext.EstimatedKVBytes <= small.EstimatedKVBytes {
		t.Fatalf("large context should be rejected with larger KV reserve: %+v", largeContext)
	}
}

func TestEstimateFitFailsClosedOnUnknownHost(t *testing.T) {
	report := EstimateFit(HostProfile{}, FitRequest{ModelBytes: byteGiB})
	if report.Fits || report.Decision != FitDecisionReject || len(report.Reasons) == 0 {
		t.Fatalf("unknown host must reject: %+v", report)
	}
}

func TestParseHumanBytes(t *testing.T) {
	got, ok := parseHumanBytes("64 GB")
	if !ok || got != 64*byteGiB {
		t.Fatalf("parseHumanBytes = %d, %v", got, ok)
	}
}
