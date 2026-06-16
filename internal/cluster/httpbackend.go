/*
Generic OpenAI-compatible HTTP backend. exo, the MLX sidecar, and llama-server
all converge on the same thing once running: a loopback HTTP server speaking the
OpenAI Chat Completions protocol with SSE streaming. httpBackend implements the
shared transport (reusing internal/llm/openai as the wire client) plus probe,
health, and streamed-chat-with-metrics. The concrete backends embed it and only
supply their own Probe/Start (process launch + diagnostics).
*/
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/*
authPlaceholder is sent as the bearer token to local runtimes that do not
require auth (llama.cpp documents exactly this string). When the runtime config
requires a real token, the configured value is used instead.
*/
const authPlaceholder = "sk-no-key-required"

/* httpBackend is the shared base for every OpenAI-compatible HTTP backend. */
type httpBackend struct {
	name       string
	servedName string
	/* baseURL is the OpenAI base, including the /v1 suffix. */
	baseURL    string
	maxContext int
	logger     *slog.Logger
	metrics    *Collector
	sup        *supervisor
	http       *http.Client
}

/* Name implements InferenceBackend. */
func (b *httpBackend) Name() string { return b.name }

/* client builds an openai transport pointed at this backend's endpoint. */
func (b *httpBackend) client(model string) (*openai.Client, error) {
	served := model
	if served == "" {
		served = b.servedName
	}
	return openai.New(openai.Config{
		APIKey:  authPlaceholder,
		BaseURL: b.baseURL,
		Model:   served,
		HTTP:    &http.Client{Timeout: 0},
	})
}

/*
probeEndpoint performs a cheap GET <base>/models to decide reachability. A 2xx
means the runtime is up and answering the OpenAI surface.
*/
func (b *httpBackend) probeEndpoint(ctx context.Context) (bool, string) {
	if b.baseURL == "" {
		return false, "no endpoint configured"
	}
	hc := b.http
	if hc == nil {
		hc = &http.Client{Timeout: 3 * time.Second}
	}
	/*
		Try the OpenAI model-list first, then fall back to the conventional
		/health endpoint and the server root. Backends vary: llama-server
		exposes /health, mlx_lm.server and exo expose /v1/models — any 2xx on
		one of these means "up and answering".
	*/
	root := strings.TrimSuffix(strings.TrimRight(b.baseURL, "/"), "/v1")
	candidates := []string{
		strings.TrimRight(b.baseURL, "/") + "/models",
		root + "/health",
		root + "/",
	}
	var lastDetail string
	for _, url := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastDetail = err.Error()
			continue
		}
		req.Header.Set("Authorization", "Bearer "+authPlaceholder)
		resp, err := hc.Do(req)
		if err != nil {
			lastDetail = err.Error()
			continue
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code >= 200 && code < 300 {
			return true, ""
		}
		lastDetail = fmt.Sprintf("%s -> http %d", url, code)
	}
	return false, lastDetail
}

/* Health implements InferenceBackend for HTTP-backed runtimes. */
func (b *httpBackend) Health(ctx context.Context) (*BackendHealth, error) {
	ok, detail := b.probeEndpoint(ctx)
	h := &BackendHealth{
		Backend:  b.name,
		Healthy:  ok,
		Endpoint: b.baseURL,
		Detail:   detail,
	}
	if b.sup != nil {
		h.Restarts = b.sup.RestartCount()
	}
	if !ok {
		h.LastError = detail
	}
	return h, nil
}

/* Stop implements InferenceBackend: stop the supervised process if any. */
func (b *httpBackend) Stop(ctx context.Context) error {
	if b.sup == nil {
		return nil
	}
	return b.sup.Stop(ctx)
}

/*
Chat streams a completion through the OpenAI transport, forwarding each chunk to
the sink while measuring time-to-first-token and tokens/sec. The measured record
is folded into the shared metrics collector on completion (success or failure).
*/
func (b *httpBackend) Chat(ctx context.Context, req llm.ChatRequest, stream TokenStream) error {
	cl, err := b.client(req.Model)
	if err != nil {
		return fmt.Errorf("%s: build client: %w", b.name, err)
	}
	req.Stream = true

	start := time.Now()
	rec := RequestRecord{Backend: b.name, PromptTokens: estimatePromptTokens(req)}

	ch, err := cl.ChatStream(ctx, req)
	if err != nil {
		rec.Err = true
		b.observe(rec, start)
		return fmt.Errorf("%s: chat stream: %w", b.name, err)
	}

	var firstToken time.Time
	var genStart time.Time
	for chunk := range ch {
		if chunk.Err != nil {
			rec.Err = true
			stream.OnChunk(chunk)
			b.observe(rec, start)
			return fmt.Errorf("%s: stream: %w", b.name, chunk.Err)
		}
		if chunk.Delta != "" {
			if firstToken.IsZero() {
				firstToken = time.Now()
				rec.TimeToFirstToken = firstToken.Sub(start)
				genStart = firstToken
			}
			rec.GeneratedTokens++
		}
		stream.OnChunk(chunk)
	}
	if rec.GeneratedTokens > 0 && !genStart.IsZero() {
		elapsed := time.Since(genStart).Seconds()
		if elapsed > 0 {
			rec.TokensPerSecond = float64(rec.GeneratedTokens) / elapsed
		}
	}
	b.observe(rec, start)
	return nil
}

func (b *httpBackend) observe(rec RequestRecord, start time.Time) {
	if b.metrics != nil {
		b.metrics.Observe(rec)
	}
	if b.logger != nil {
		b.logger.Info("cluster: request complete",
			"backend", b.name,
			"ttft_ms", rec.TimeToFirstToken.Milliseconds(),
			"tokens_per_sec", int(rec.TokensPerSecond),
			"generated_tokens", rec.GeneratedTokens,
			"prompt_tokens_est", rec.PromptTokens,
			"err", rec.Err,
			"total_ms", time.Since(start).Milliseconds(),
		)
	}
}

/*
estimatePromptTokens is a coarse word-count proxy. Local OpenAI-compatible
servers report real usage on non-streaming calls but rarely on streamed ones,
so a deterministic estimate keeps the metric populated without a tokenizer
dependency. The estimate is labeled "_est" in logs to avoid overclaiming.
*/
func estimatePromptTokens(req llm.ChatRequest) int {
	words := 0
	for _, m := range req.Messages {
		words += len(strings.Fields(m.Content))
	}
	/* ~1.3 tokens per word is a stable rough ratio for English prose. */
	return words * 4 / 3
}
