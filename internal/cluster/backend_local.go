/*
Local single-node fallback backend. This preserves the agent's current
behavior exactly: it wraps the existing llm.Provider (Ollama, OpenAI, or
Anthropic as already configured) and runs entirely on the coordinator with no
extra process, no cluster, and no network fan-out. It is always Available when a
provider is wired, which is what makes it a dependable fallback target when no
cluster backend can be brought up.
*/
package cluster

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* localBackend adapts an in-process llm.Provider to the InferenceBackend API. */
type localBackend struct {
	provider llm.Provider
	logger   *slog.Logger
	metrics  *Collector
}

/* newLocalBackend wraps an existing provider as the fallback backend. */
func newLocalBackend(p llm.Provider, logger *slog.Logger, m *Collector) *localBackend {
	return &localBackend{provider: p, logger: logger, metrics: m}
}

/* Name implements InferenceBackend. */
func (b *localBackend) Name() string { return BackendLocal }

/* Probe reports availability based purely on whether a provider is wired. */
func (b *localBackend) Probe(ctx context.Context) (*BackendCapabilities, error) {
	if b.provider == nil {
		return &BackendCapabilities{Diagnostic: "no local provider configured"}, nil
	}
	return &BackendCapabilities{Installed: true, Available: true, Endpoint: "in-process:" + b.provider.Name()}, nil
}

/* Start is a no-op: the local provider needs no process management. */
func (b *localBackend) Start(ctx context.Context, cfg BackendConfig) error {
	if b.provider == nil {
		return errors.New("local: no provider configured")
	}
	return nil
}

/* Stop is a no-op for the in-process provider. */
func (b *localBackend) Stop(ctx context.Context) error { return nil }

/* Health reflects whether a provider is present (always healthy if so). */
func (b *localBackend) Health(ctx context.Context) (*BackendHealth, error) {
	if b.provider == nil {
		return &BackendHealth{Backend: BackendLocal, Healthy: false, LastError: "no provider"}, nil
	}
	return &BackendHealth{Backend: BackendLocal, Healthy: true, Endpoint: "in-process:" + b.provider.Name()}, nil
}

/* Chat streams from the wrapped provider, recording the same metrics as HTTP backends. */
func (b *localBackend) Chat(ctx context.Context, req llm.ChatRequest, stream TokenStream) error {
	if b.provider == nil {
		return errors.New("local: no provider configured")
	}
	req.Stream = true
	start := time.Now()
	rec := RequestRecord{Backend: BackendLocal, PromptTokens: estimatePromptTokens(req)}

	ch, err := b.provider.ChatStream(ctx, req)
	if err != nil {
		rec.Err = true
		b.observe(rec)
		return err
	}
	var genStart time.Time
	for chunk := range ch {
		if chunk.Err != nil {
			rec.Err = true
			stream.OnChunk(chunk)
			b.observe(rec)
			return chunk.Err
		}
		if chunk.Delta != "" {
			if genStart.IsZero() {
				genStart = time.Now()
				rec.TimeToFirstToken = genStart.Sub(start)
			}
			rec.GeneratedTokens++
		}
		stream.OnChunk(chunk)
	}
	if rec.GeneratedTokens > 0 && !genStart.IsZero() {
		if elapsed := time.Since(genStart).Seconds(); elapsed > 0 {
			rec.TokensPerSecond = float64(rec.GeneratedTokens) / elapsed
		}
	}
	b.observe(rec)
	return nil
}

func (b *localBackend) observe(rec RequestRecord) {
	if b.metrics != nil {
		b.metrics.Observe(rec)
	}
}
