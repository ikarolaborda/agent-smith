package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
)

/*
writeTinyGGUF fabricates a minimal spec-valid GGUF whose only metadata is the
architecture and a native context_length, so the tuner path can be exercised
offline against a real file.
*/
func writeTinyGGUF(t *testing.T, dir string, nativeCtx uint32) string {
	t.Helper()
	var b []byte
	u32 := func(v uint32) { b = binary.LittleEndian.AppendUint32(b, v) }
	u64 := func(v uint64) { b = binary.LittleEndian.AppendUint64(b, v) }
	str := func(s string) { u64(uint64(len(s))); b = append(b, s...) }
	b = append(b, "GGUF"...)
	u32(3)
	u64(0)
	u64(2)
	str("general.architecture")
	u32(8) /* string */
	str("qwen2")
	str("qwen2.context_length")
	u32(4) /* uint32 */
	u32(nativeCtx)
	path := filepath.Join(dir, "tiny.gguf")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPinnedCtxAboveNativeWarnsWithoutClamping(t *testing.T) {
	/*
		Explicit config wins: a ctx_size pinned above the model's native context
		must survive untouched — the tuner only warns. Clamping here would break
		deliberate rope-scaling setups.
	*/
	dir := t.TempDir()
	modelPath := writeTinyGGUF(t, dir, 4096)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	lc := &config.LlamaCppConfig{ModelPath: modelPath, CtxSize: 16384}
	rc := llamacpp.RuntimeConfig{ModelPath: modelPath, CtxSize: 16384}

	applyLlamaAutoTune(context.Background(), lc, &rc, logger)

	if rc.CtxSize != 16384 {
		t.Fatalf("pinned ctx must never be clamped, got %d", rc.CtxSize)
	}
	if !strings.Contains(logBuf.String(), "exceeds the model's native context") {
		t.Fatalf("expected a native-context warning, logs: %s", logBuf.String())
	}
}
