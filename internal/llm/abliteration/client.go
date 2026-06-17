/*
Package abliteration registers the "abliteration" provider: abliteration.ai's
OpenAI-compatible chat endpoint. It reuses the OpenAI transport (identical wire
schema) and defaults to a low temperature so generations favour factual,
low-creativity output — the posture this project wants for grounded
cybersecurity work.

abliteration.ai is a REMOTE, hosted endpoint, so this provider only activates
when an API key is configured; it is not part of the offline path.
*/
package abliteration

import (
	"context"
	"fmt"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/* providerName is the registry key for this provider. */
const providerName = "abliteration"

/* DefaultBaseURL is abliteration.ai's OpenAI-compatible API root. */
const DefaultBaseURL = "https://api.abliteration.ai/v1"

/*
DefaultTemperature biases generations toward factual, low-creativity output.
0.2 keeps results grounded and near-deterministic without the degenerate
repetition pure-greedy 0.0 can cause on some models; callers may still override
per request.
*/
const DefaultTemperature = 0.2

/* Config configures the abliteration provider. Temperature overrides DefaultTemperature when non-nil. */
type Config struct {
	APIKey      string
	BaseURL     string
	Model       string
	Temperature *float64
}

/*
Client wraps the OpenAI transport pointed at abliteration.ai and injects the
default temperature on requests that do not set one.
*/
type Client struct {
	inner llm.Provider
	temp  float64
}

/* New builds the provider. A missing BaseURL falls back to DefaultBaseURL; the API key is required. */
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("abliteration: missing api_key")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	inner, err := openai.New(openai.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL, Model: cfg.Model})
	if err != nil {
		return nil, err
	}
	temp := DefaultTemperature
	if cfg.Temperature != nil {
		temp = *cfg.Temperature
	}
	return &Client{inner: inner, temp: temp}, nil
}

/* Name reports the provider identifier used by the registry. */
func (*Client) Name() string { return providerName }

/* Chat performs a non-streaming completion, defaulting the temperature when unset. */
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return c.inner.Chat(ctx, c.withDefaults(req))
}

/* ChatStream performs a streaming completion, defaulting the temperature when unset. */
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	return c.inner.ChatStream(ctx, c.withDefaults(req))
}

/* withDefaults injects the low factual temperature when the caller left it unset. */
func (c *Client) withDefaults(req llm.ChatRequest) llm.ChatRequest {
	if req.Temperature == nil {
		t := c.temp
		req.Temperature = &t
	}
	return req
}

/* init registers the abliteration factory with the llm registry. */
func init() {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("abliteration: expected abliteration.Config, got %T", cfg)
		}
		return New(c)
	})
}
