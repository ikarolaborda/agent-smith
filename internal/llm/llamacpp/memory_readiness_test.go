package llamacpp

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
)

func gb(n uint64) uint64 { return n * byteGiB }

func rejectingHost() HostProfile {
	/* Total 16 GiB, only 6 GiB available: osReserve=4, headroom=1 -> budget 5 GiB. */
	return HostProfile{
		OS: "test", Arch: "test",
		TotalMemoryBytes:     gb(16),
		AvailableMemoryBytes: gb(6),
		FreeDiskBytes:        gb(100),
	}
}

/* Stage 2: a model refused on current availability is admitted once the memory
the agent will reclaim from its own prior model is credited. */
func it_credits_reclaimable_self_memory(t *testing.T) {
	req := FitRequest{ModelBytes: gb(3), ContextTokens: 4096, Parallel: 1}

	base := EstimateFit(rejectingHost(), req)
	if base.Fits {
		t.Fatalf("model should be refused without self-reclaim credit: %+v", base.Reasons)
	}

	req.ReclaimableSelfBytes = gb(4)
	credited := EstimateFit(rejectingHost(), req)
	if !credited.Fits {
		t.Fatalf("model should fit once 4 GiB of reclaimable-self is credited: %+v", credited.Reasons)
	}
	if credited.ReclaimableSelfBytes != gb(4) {
		t.Errorf("report should echo the credit, got %d", credited.ReclaimableSelfBytes)
	}
}

func TestFitCreditsReclaimableSelf(t *testing.T) { it_credits_reclaimable_self_memory(t) }

/* Stage 2 safety: the credit can never push the budget above the physical
Total-osReserve ceiling, even with an absurd reclaimable claim. */
func TestReclaimableSelfNeverExceedsTotalBudget(t *testing.T) {
	req := FitRequest{ModelBytes: gb(1), ContextTokens: 2048, Parallel: 1, ReclaimableSelfBytes: gb(100)}
	r := EstimateFit(rejectingHost(), req)
	/* totalBudget = 16 - max(4, 16/8=2) = 12 GiB. */
	if r.AvailableMemoryBudget != gb(12) {
		t.Fatalf("budget must be capped at Total-osReserve (12 GiB), got %d GiB", r.AvailableMemoryBudget/byteGiB)
	}
}

/* Stage 2 safety: an overflowing reclaimable claim saturates instead of wrapping
to a tiny number, and the Total-osReserve cap still holds. */
func TestReclaimableSelfOverflowSaturates(t *testing.T) {
	req := FitRequest{ModelBytes: gb(1), ContextTokens: 2048, Parallel: 1, ReclaimableSelfBytes: math.MaxUint64}
	r := EstimateFit(rejectingHost(), req)
	if r.AvailableMemoryBudget != gb(12) {
		t.Fatalf("overflow credit must saturate and stay capped at 12 GiB, got %d", r.AvailableMemoryBudget/byteGiB)
	}
}

/* Stage 3 full path: a quantized recommendation must validate, reach the launch
args, AND be the same type the fit gate reserves against — locking the whole
tune -> config -> validate -> args / estimate chain against divergence. */
func TestKVTypeSurvivesRecommendationToLaunch(t *testing.T) {
	host := HostProfile{OS: "t", Arch: "t", TotalMemoryBytes: gb(16), AvailableMemoryBytes: gb(7) + byteGiB/10*3, FreeDiskBytes: gb(100)}
	rec := RecommendRuntime(host, gb(4), 0, 0, 0)
	if rec.KVCacheType == "" || rec.KVCacheType == KVCacheF16 {
		t.Fatalf("expected a quantized recommendation for this tight host, got %q", rec.KVCacheType)
	}

	cfg := RuntimeConfig{ModelPath: "/tmp/m.gguf", CtxSize: rec.CtxSize, Parallel: 1, GPULayers: rec.GPULayers, KVCacheType: rec.KVCacheType}
	if err := validateRuntimeConfig(cfg); err != nil {
		t.Fatalf("recommended config must validate: %v", err)
	}
	args := strings.Join(NewRuntime(cfg).buildArgs("/tmp/m.gguf", "127.0.0.1", 8080), " ")
	if !strings.Contains(args, "--cache-type-k "+string(rec.KVCacheType)) {
		t.Fatalf("recommended KV type %q must reach the launch args: %s", rec.KVCacheType, args)
	}

	quant := EstimateFit(host, FitRequest{ModelBytes: gb(4), ContextTokens: rec.CtxSize, Parallel: 1, KVCacheType: rec.KVCacheType}).EstimatedKVBytes
	f16 := EstimateFit(host, FitRequest{ModelBytes: gb(4), ContextTokens: rec.CtxSize, Parallel: 1}).EstimatedKVBytes
	if quant >= f16 {
		t.Fatalf("the fit gate must reserve less KV (%d) at the quantized type than at f16 (%d)", quant, f16)
	}
}

/* Stage 3: quantizing the KV cache reduces the reserved KV bytes by the
context7-derived, rounded-up fractions (q8_0=9/16, q4_0=5/16 of f16). */
func TestKVCacheTypeShrinksReserve(t *testing.T) {
	host := HostProfile{OS: "t", Arch: "t", TotalMemoryBytes: gb(64), AvailableMemoryBytes: gb(48), FreeDiskBytes: gb(100)}
	base := FitRequest{ModelBytes: gb(4), ContextTokens: 8192, Parallel: 1}

	f16 := EstimateFit(host, base).EstimatedKVBytes
	base.KVCacheType = KVCacheQ8_0
	q8 := EstimateFit(host, base).EstimatedKVBytes
	base.KVCacheType = KVCacheQ4_0
	q4 := EstimateFit(host, base).EstimatedKVBytes

	if q8 != f16*9/16 {
		t.Errorf("q8_0 KV = %d, want %d (9/16 of f16)", q8, f16*9/16)
	}
	if q4 != f16*5/16 {
		t.Errorf("q4_0 KV = %d, want %d (5/16 of f16)", q4, f16*5/16)
	}
}

/* Backward compatibility: empty KV type must equal the f16 default exactly. */
func TestEmptyKVTypeEqualsF16(t *testing.T) {
	host := HostProfile{OS: "t", Arch: "t", TotalMemoryBytes: gb(64), AvailableMemoryBytes: gb(48), FreeDiskBytes: gb(100)}
	empty := EstimateFit(host, FitRequest{ModelBytes: gb(4), ContextTokens: 8192, Parallel: 1})
	f16 := EstimateFit(host, FitRequest{ModelBytes: gb(4), ContextTokens: 8192, Parallel: 1, KVCacheType: KVCacheF16})
	if empty.EstimatedKVBytes != f16.EstimatedKVBytes {
		t.Fatalf("empty KV type (%d) must equal f16 (%d)", empty.EstimatedKVBytes, f16.EstimatedKVBytes)
	}
}

/* Stage 3: the tuner keeps full-precision KV when RAM is ample (quality-first). */
func TestRecommendKeepsF16WhenAmple(t *testing.T) {
	host := HostProfile{OS: "t", Arch: "t", TotalMemoryBytes: gb(64), AvailableMemoryBytes: gb(48), FreeDiskBytes: gb(100)}
	rec := RecommendRuntime(host, gb(4), 0, 0, 0)
	if rec.KVCacheType != KVCacheF16 {
		t.Fatalf("ample RAM should keep f16 KV, got %q", rec.KVCacheType)
	}
	if rec.CtxSize < minRecommendedCtx {
		t.Errorf("ample RAM should give a generous ctx, got %d", rec.CtxSize)
	}
}

/* Stage 3: when f16 cannot reach the minimum context, the tuner quantizes the
KV cache rather than giving up, and stays at or above the minimum ctx. */
func TestRecommendQuantizesKVToFit(t *testing.T) {
	/* base = weights(4 GiB +15%) + scratch(1 GiB) = 5.6 GiB; budget 6.3 GiB sits
	   between base+q8_0@2048 (6.16) and base+f16@2048 (6.6). */
	host := HostProfile{OS: "t", Arch: "t", TotalMemoryBytes: gb(16), AvailableMemoryBytes: gb(7) + byteGiB/10*3, FreeDiskBytes: gb(100)}
	rec := RecommendRuntime(host, gb(4), 0, 0, 0)
	if rec.KVCacheType != KVCacheQ8_0 {
		t.Fatalf("tight RAM should quantize KV to q8_0 to fit, got %q (ctx=%d)", rec.KVCacheType, rec.CtxSize)
	}
	if rec.CtxSize < minRecommendedCtx {
		t.Errorf("quantized recommendation must still meet the minimum ctx, got %d", rec.CtxSize)
	}
}

/* Stage 3 threading: a quantized KV type reaches the launched -ctk/-ctv args,
and the f16 default emits no cache-type flag (server uses its default). */
func TestKVTypeThreadsToArgs(t *testing.T) {
	q8 := strings.Join(NewRuntime(RuntimeConfig{ModelPath: "/tmp/m.gguf", KVCacheType: KVCacheQ8_0}).buildArgs("/tmp/m.gguf", "127.0.0.1", 8080), " ")
	if !strings.Contains(q8, "--cache-type-k q8_0") || !strings.Contains(q8, "--cache-type-v q8_0") {
		t.Fatalf("q8_0 must reach -ctk/-ctv args: %s", q8)
	}
	f16 := strings.Join(NewRuntime(RuntimeConfig{ModelPath: "/tmp/m.gguf"}).buildArgs("/tmp/m.gguf", "127.0.0.1", 8080), " ")
	if strings.Contains(f16, "cache-type") {
		t.Fatalf("default (f16) must not emit a cache-type flag: %s", f16)
	}
}

/* The live launch preflight structurally cannot credit reclaimable-self memory
(LocalPreflightRequest has no such field), so the measure-after-reclaim Start
path can never double-count freed memory. */
func TestPreflightNeverCreditsReclaimableSelf(t *testing.T) {
	dir := t.TempDir()
	model := dir + "/model.gguf"
	if err := os.WriteFile(model, []byte("GGUF-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := InspectLocal(context.Background(), ampleProfiler(), LocalPreflightRequest{
		ModelFiles: []string{model}, ContextTokens: 4096, Parallel: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.ReclaimableSelfBytes != 0 {
		t.Fatalf("preflight must never credit reclaimable-self, got %d", report.ReclaimableSelfBytes)
	}
}

/* Stage 5: the Darwin parser counts purgeable pages (previously omitted) and
excludes non-reclaimable classes (active, wired, compressor). */
func TestParseDarwinAvailableCountsPurgeable(t *testing.T) {
	vmStat := strings.Join([]string{
		"Mach Virtual Memory Statistics: (page size of 16384 bytes)",
		"Pages free:                       100.",
		"Pages active:                    9000.",
		"Pages inactive:                   200.",
		"Pages speculative:                 50.",
		"Pages wired down:                3000.",
		"Pages purgeable:                   40.",
		"Pages occupied by compressor:    5000.",
		"garbage line without a colon value",
		"Pages malformed:                 notanumber.",
	}, "\n")

	available, pageSize := parseDarwinAvailablePages(vmStat)
	if pageSize != 16384 {
		t.Fatalf("page size = %d, want 16384", pageSize)
	}
	/* free+inactive+speculative+purgeable = 100+200+50+40 = 390 pages. */
	want := uint64(390) * 16384
	if available != want {
		t.Fatalf("available = %d, want %d (must include purgeable, exclude active/wired/compressor)", available, want)
	}
}
