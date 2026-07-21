package llamacpp

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

/*
ggufBuilder assembles a minimal valid GGUF byte stream for parser tests, so
every layout case (value widths, arrays, ordering, truncation) is exercised
against real wire format rather than mocks.
*/
type ggufBuilder struct{ b []byte }

func (g *ggufBuilder) u32(v uint32) *ggufBuilder {
	g.b = binary.LittleEndian.AppendUint32(g.b, v)
	return g
}

func (g *ggufBuilder) u64(v uint64) *ggufBuilder {
	g.b = binary.LittleEndian.AppendUint64(g.b, v)
	return g
}

func (g *ggufBuilder) str(s string) *ggufBuilder {
	g.u64(uint64(len(s)))
	g.b = append(g.b, s...)
	return g
}

func (g *ggufBuilder) header(kvCount uint64) *ggufBuilder {
	g.b = append(g.b, "GGUF"...)
	g.u32(3) /* version */
	g.u64(0) /* tensor count */
	g.u64(kvCount)
	return g
}

func (g *ggufBuilder) kvString(key, val string) *ggufBuilder {
	return g.str(key).u32(ggufTypeString).str(val)
}

func (g *ggufBuilder) kvU32(key string, val uint32) *ggufBuilder {
	return g.str(key).u32(ggufTypeUint32).u32(val)
}

func (g *ggufBuilder) kvU64(key string, val uint64) *ggufBuilder {
	return g.str(key).u32(ggufTypeUint64).u64(val)
}

func (g *ggufBuilder) write(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, g.b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadGGUFContextLengthU32(t *testing.T) {
	g := (&ggufBuilder{}).header(2).
		kvString("general.architecture", "qwen2").
		kvU32("qwen2.context_length", 32768)
	n, err := ReadGGUFContextLength(g.write(t))
	if err != nil || n != 32768 {
		t.Fatalf("want 32768, got %d (err %v)", n, err)
	}
}

func TestReadGGUFContextLengthU64AndNonLlamaArch(t *testing.T) {
	g := (&ggufBuilder{}).header(2).
		kvString("general.architecture", "qwen3moe").
		kvU64("qwen3moe.context_length", 262144)
	n, err := ReadGGUFContextLength(g.write(t))
	if err != nil || n != 262144 {
		t.Fatalf("want 262144, got %d (err %v)", n, err)
	}
}

func TestReadGGUFContextLengthArchAfterValue(t *testing.T) {
	/* Key order is not guaranteed by the spec: value first, architecture later. */
	g := (&ggufBuilder{}).header(3).
		kvU32("llama.context_length", 8192).
		kvString("general.name", "x").
		kvString("general.architecture", "llama")
	n, err := ReadGGUFContextLength(g.write(t))
	if err != nil || n != 8192 {
		t.Fatalf("want 8192, got %d (err %v)", n, err)
	}
}

func TestReadGGUFContextLengthWrongArchNotGuessed(t *testing.T) {
	/*
		The architecture is known but has no context_length, and a foreign
		prefix carries one: refusing to guess beats returning a wrong ceiling.
	*/
	g := (&ggufBuilder{}).header(2).
		kvString("general.architecture", "qwen2").
		kvU32("llama.context_length", 999)
	n, err := ReadGGUFContextLength(g.write(t))
	if err != nil || n != 0 {
		t.Fatalf("want 0 (no guess), got %d (err %v)", n, err)
	}
}

func TestReadGGUFContextLengthSkipsArraysAndScalars(t *testing.T) {
	g := (&ggufBuilder{}).header(4).
		kvString("general.name", "m")
	/* array of two strings must be skipped structurally */
	g.str("tokenizer.ggml.tokens").u32(ggufTypeArray).u32(ggufTypeString).u64(2).str("a").str("bb")
	g.kvString("general.architecture", "phi3").
		kvU32("phi3.context_length", 4096)
	n, err := ReadGGUFContextLength(g.write(t))
	if err != nil || n != 4096 {
		t.Fatalf("want 4096, got %d (err %v)", n, err)
	}
}

func TestReadGGUFContextLengthRejectsGarbage(t *testing.T) {
	badMagic := filepath.Join(t.TempDir(), "bad.gguf")
	if err := os.WriteFile(badMagic, []byte("NOPEnotgguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ReadGGUFContextLength(badMagic); err == nil || n != 0 {
		t.Fatalf("bad magic must error, got %d (err %v)", n, err)
	}
	truncated := (&ggufBuilder{}).header(2).kvString("general.architecture", "llama")
	if n, err := ReadGGUFContextLength(truncated.write(t)); err == nil || n != 0 {
		t.Fatalf("truncated kv section must error, got %d (err %v)", n, err)
	}
}

func TestNativeContextFromPlanPrefersLocalFile(t *testing.T) {
	/*
		The committed local file must beat a stale remote hint: it is the exact
		artifact that will be served.
	*/
	dir := t.TempDir()
	g := (&ggufBuilder{}).header(2).
		kvString("general.architecture", "qwen2").
		kvU32("qwen2.context_length", 32768)
	if err := os.WriteFile(filepath.Join(dir, "m.gguf"), g.b, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := DownloadPlan{
		CacheDir: dir,
		Manifest: Manifest{
			ContextLength:  999999,
			ModelArtifacts: []Artifact{{Filename: "m.gguf"}},
		},
	}
	if n := NativeContextFromPlan(plan); n != 32768 {
		t.Fatalf("local file must win over remote hint, got %d", n)
	}
	/* without the local file, the remote hint is the best available answer */
	plan.CacheDir = t.TempDir()
	if n := NativeContextFromPlan(plan); n != 999999 {
		t.Fatalf("missing local file must fall back to the hint, got %d", n)
	}
}

func TestReadGGUFContextLengthRealCachedModel(t *testing.T) {
	/*
		Tier-2 check against a real artifact when this host has one cached:
		cross-validates the parser against Hugging Face's own gguf metadata
		(context_length=32768 for this repo). Skipped where absent, so CI
		without cached models is unaffected.
	*/
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	matches, _ := filepath.Glob(filepath.Join(home,
		".agent-smith", "models", "bartowski_Qwen2.5-Coder-3B-Instruct-abliterated-GGUF", "*", "*.gguf"))
	if len(matches) == 0 {
		t.Skip("no cached 3B coder artifact on this host")
	}
	n, err := ReadGGUFContextLength(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if n != 32768 {
		t.Fatalf("real 3B coder GGUF should report native ctx 32768, got %d", n)
	}
}

func TestEffectiveCtxCeiling(t *testing.T) {
	max := ctxLadder[0]
	cases := []struct {
		name     string
		operator int
		native   int
		want     int
	}{
		{"unknown native keeps pre-dynamic cap", 0, 0, fallbackCtxCeiling},
		{"native below ladder max binds", 0, 8192, 8192},
		{"native above ladder max leaves ladder max", 0, 262144, max},
		{"operator below native binds", 4096, 32768, 4096},
		{"operator above native loses to native", 65536, 32768, 32768},
		{"operator below unknown-native fallback binds", 16384, 0, 16384},
	}
	for _, c := range cases {
		got, _ := effectiveCtxCeiling(c.operator, c.native)
		if got != c.want {
			t.Fatalf("%s: want %d, got %d", c.name, c.want, got)
		}
	}
}

func TestRecommendRuntimeHonorsNativeCap(t *testing.T) {
	/* Unified host with a budget that could seat far more KV than 8k. */
	host := hostWith(256, 200)
	host.AppleUnifiedMemory = true
	host.GPU = GPUInfo{Backend: GPUBackendMetal, Unified: true, VRAMBytes: 200 * byteGiB}
	rec := RecommendRuntime(host, 4*byteGiB, 0, 0, 8192)
	if rec.CtxSize > 8192 {
		t.Fatalf("ctx must not exceed the native context, got %d", rec.CtxSize)
	}
	if rec.NativeCtx != 8192 {
		t.Fatalf("recommendation must echo the native ctx, got %d", rec.NativeCtx)
	}

	/* Unknown native on the same huge host: conservative pre-dynamic ceiling. */
	rec = RecommendRuntime(host, 4*byteGiB, 0, 0, 0)
	if rec.CtxSize > fallbackCtxCeiling {
		t.Fatalf("unknown native must cap at %d, got %d", fallbackCtxCeiling, rec.CtxSize)
	}

	/* Known large native on the huge host: the taller ladder opens up. */
	rec = RecommendRuntime(host, 4*byteGiB, 0, 0, 131072)
	if rec.CtxSize <= fallbackCtxCeiling {
		t.Fatalf("large native + large budget should exceed the old 32k cap, got %d", rec.CtxSize)
	}
}
