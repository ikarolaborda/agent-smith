package server

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
)

func TestCompactBudgetsScaleWithWindow(t *testing.T) {
	cases := []struct {
		ctx         int
		wantTrigger int
		wantTotal   int
	}{
		{0, 2048, 2048},    // unknown -> conservative default (8192) then floored
		{2048, 2048, 2048}, // tiny window floors both
		{8192, 2048, 2048}, // legacy default: 8192-6144=2048, 8192-7168=1024->2048
		{32768, 26624, 25600},
		{131072, 124928, 123904},
	}
	for _, c := range cases {
		if got := compactTriggerTokens(c.ctx); got != c.wantTrigger {
			t.Errorf("compactTriggerTokens(%d)=%d, want %d", c.ctx, got, c.wantTrigger)
		}
		if got := compactTotalBudget(c.ctx); got != c.wantTotal {
			t.Errorf("compactTotalBudget(%d)=%d, want %d", c.ctx, got, c.wantTotal)
		}
	}
}

func TestReadDirBudgetScalesAndCaps(t *testing.T) {
	/* Small window: half the window in bytes; large window: capped at the tool default. */
	if got := readDirBudgetBytes(2048); got != 4096 {
		t.Errorf("readDirBudgetBytes(2048)=%d, want 4096", got)
	}
	big := readDirBudgetBytes(131072)
	if big != readDirBudgetBytes(1<<20) {
		t.Errorf("large windows must clamp to the same tool cap, got %d", big)
	}
}

/* ctxProvider is a minimal llm.Provider that also reports a context window. */
type ctxProvider struct{ w int }

func (ctxProvider) Name() string { return "llamacpp" }

func (ctxProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}

func (ctxProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p ctxProvider) ContextWindow() int { return p.w }

func cfgWithCtx(ctx int) *config.Config {
	return &config.Config{Providers: map[string]config.ProviderConfig{
		"llamacpp": {LlamaCpp: &config.LlamaCppConfig{CtxSize: ctx}},
	}}
}

func TestEffectiveLocalCtxResolutionOrder(t *testing.T) {
	/* Provider's live window wins over a stale config pin. */
	s := &Server{
		providers: map[string]llm.Provider{"llamacpp": ctxProvider{w: 131072}},
		cfg:       cfgWithCtx(16384),
	}
	if got := s.effectiveLocalCtx(); got != 131072 {
		t.Fatalf("live provider window must win, got %d", got)
	}

	/* No provider window (0) -> fall back to the config pin. */
	s = &Server{
		providers: map[string]llm.Provider{"llamacpp": ctxProvider{w: 0}},
		cfg:       cfgWithCtx(16384),
	}
	if got := s.effectiveLocalCtx(); got != 16384 {
		t.Fatalf("config pin must be the fallback, got %d", got)
	}

	/* Neither available -> conservative default. */
	s = &Server{providers: map[string]llm.Provider{}, cfg: &config.Config{}}
	if got := s.effectiveLocalCtx(); got != defaultLocalCtxTokens {
		t.Fatalf("default must be %d, got %d", defaultLocalCtxTokens, got)
	}
}
