/*
provider.go adapts a running Runtime to llm.Provider. llama-server exposes an
OpenAI-compatible /v1 endpoint, so chat and streaming are delegated to the
existing openai client pointed at the local port — no wire format is
reimplemented. The provider owns the Runtime's lifecycle: Close stops the
subprocess, and chat calls fail fast once shutdown has begun.
*/
package llamacpp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/* providerName is the registry key used to construct this provider. */
const providerName = "llamacpp"

/*
Config constructs a Provider around an already-started Runtime. Model is the
served model name advertised to callers and echoed to llama-server (which serves
a single model regardless). APIKey, when provided, must match the credential
owned by the supervised Runtime.
*/
type Config struct {
	Runtime *Runtime
	Model   string
	APIKey  string
}

/*
Provider is the llm.Provider implementation for a local llama.cpp server. It
delegates chat to an embedded openai client and guards the Runtime lifecycle.
*/
type Provider struct {
	rt    *Runtime
	inner llm.Provider
	model string

	mu     sync.RWMutex
	closed bool
}

func init() {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("llamacpp: expected llamacpp.Config, got %T", cfg)
		}
		return New(c)
	})
}

/*
New builds a Provider from a started Runtime. It requires the Runtime to have a
BaseURL (i.e. Start has completed) so the embedded client targets the live
endpoint.
*/
func New(cfg Config) (*Provider, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("llamacpp: Config.Runtime is nil")
	}
	base := cfg.Runtime.BaseURL()
	if base == "" {
		return nil, errors.New("llamacpp: runtime not started (empty base URL)")
	}
	key := cfg.Runtime.APIKey()
	if key == "" {
		return nil, errors.New("llamacpp: runtime has no authenticated API key")
	}
	if cfg.APIKey != "" && cfg.APIKey != key {
		return nil, errors.New("llamacpp: provider API key does not match the supervised runtime")
	}
	inner, err := openai.New(openai.Config{APIKey: key, BaseURL: base, Model: cfg.Model})
	if err != nil {
		return nil, fmt.Errorf("llamacpp: build inner client: %w", err)
	}
	return &Provider{rt: cfg.Runtime, inner: inner, model: cfg.Model}, nil
}

/* Name reports the provider identifier used by the registry. */
func (p *Provider) Name() string { return providerName }

// SupportsVision reports whether the supervised runtime was launched with a
// validated multimodal projector.
func (p *Provider) SupportsVision() bool { return p.rt.SupportsVision() }

/* Chat performs a non-streaming completion against the local server. */
func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if err := p.guard(); err != nil {
		return nil, err
	}
	return p.inner.Chat(ctx, req)
}

/* ChatStream performs a streaming completion against the local server. */
func (p *Provider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	if err := p.guard(); err != nil {
		return nil, err
	}
	return p.inner.ChatStream(ctx, req)
}

/* guard rejects calls once Close has begun so requests never hit a dead port. */
func (p *Provider) guard() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return errors.New("llamacpp: provider is closed")
	}
	return nil
}

/*
Close marks the provider closed and stops the underlying llama-server. It is
safe to call multiple times; the Runtime itself guards against double-signal.
*/
func (p *Provider) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return p.rt.Close(ctx)
}

/*
logWriter forwards a child process's stdout/stderr to the structured logger at
debug level, one line per log record, so llama-server output is captured without
flooding higher log levels.
*/
type logWriter struct {
	logger *slog.Logger
	stream string
	buf    []byte
}

func newLogWriter(logger *slog.Logger, stream string) io.Writer {
	return &logWriter{logger: logger, stream: stream}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.logger.Debug("llamacpp: server output", "stream", w.stream, "line", line)
		}
	}
	/* Cap the pending buffer so a stream with no newlines cannot grow unbounded. */
	if len(w.buf) > bufio.MaxScanTokenSize {
		w.logger.Debug("llamacpp: server output", "stream", w.stream, "line", string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
