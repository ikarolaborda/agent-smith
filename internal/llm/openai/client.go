/*
Package openai implements llm.Provider against the OpenAI Chat Completions
API. Streaming uses Server-Sent Events; tool calls map onto the
ChatCompletionMessageToolCall schema.
*/
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* DefaultBaseURL is the public OpenAI Chat Completions endpoint root. */
const DefaultBaseURL = "https://api.openai.com/v1"

/* providerName is the registry key used to construct this provider. */
const providerName = "openai"

/* Config carries the settings needed to construct a Client. */
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

/*
Client is the OpenAI implementation of llm.Provider. All exported fields are
read-only after construction; the HTTP client is reused across requests.
*/
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

/*
New constructs a Client. Missing BaseURL falls back to DefaultBaseURL; a nil
HTTP client is replaced with a 30-second-timeout default.
*/
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("openai: APIKey is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		http:    cfg.HTTP,
	}, nil
}

/* init registers the OpenAI factory with the llm registry. */
func init() {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("openai: expected openai.Config, got %T", cfg)
		}
		return New(c)
	})
}

/* Name reports the provider identifier used by the registry. */
func (c *Client) Name() string { return providerName }

/* wire types: kept close to the OpenAI schema, exposed only as request/response shapes. */

type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCall struct {
	ID       string               `json:"id,omitempty"`
	Index    *int                 `json:"index,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function wireToolCallFunction `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

/*
wireOutMessage is the request-side message. Content is an interface because the
OpenAI chat API accepts either a plain string or an array of typed parts (text
+ image_url) for multimodal input; the response-side wireMessage keeps Content
as a plain string.
*/
type wireOutMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireRequest struct {
	Model       string           `json:"model"`
	Messages    []wireOutMessage `json:"messages"`
	Tools       []wireTool       `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type wireChoice struct {
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type wireResponse struct {
	Choices []wireChoice `json:"choices"`
	Usage   wireUsage    `json:"usage"`
}

/* errorEnvelope is the OpenAI error response shape. */
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

/* buildRequest produces the wire-level request body from the canonical ChatRequest. */
func (c *Client) buildRequest(req llm.ChatRequest, stream bool) wireRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}

	out := wireRequest{
		Model:       model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}

	out.Messages = make([]wireOutMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		wm := wireOutMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		/*
			With images present, OpenAI requires content as an array of parts:
			a text part (when there is text) followed by image_url parts whose
			URL is a base64 data URL. Without images we keep the plain string.
		*/
		if len(m.Images) > 0 {
			parts := make([]any, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURL(img)},
				})
			}
			wm.Content = parts
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunction{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		out.Messages = append(out.Messages, wm)
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, wireTool{
			Type: "function",
			Function: wireToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

/* dataURL re-wraps a canonical ImagePart as the base64 data URL OpenAI expects. */
func dataURL(img llm.ImagePart) string {
	mime := img.MimeType
	if mime == "" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + img.Data
}

/* doRequest issues a POST and returns the raw response body or an error envelope. */
func (c *Client) doRequest(ctx context.Context, body wireRequest, accept string) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
	r.Header.Set("Content-Type", "application/json")
	if accept != "" {
		r.Header.Set("Accept", accept)
	}

	resp, err := c.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var env errorEnvelope
		if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
			return nil, fmt.Errorf("openai: %s (%s)", env.Error.Message, env.Error.Type)
		}
		return nil, fmt.Errorf("openai: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp, nil
}

/* Chat performs a non-streaming POST to /chat/completions. */
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := c.doRequest(ctx, c.buildRequest(req, false), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var wr wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(wr.Choices) == 0 {
		return nil, errors.New("openai: empty choices")
	}

	first := wr.Choices[0]
	out := &llm.ChatResponse{
		FinishReason: first.FinishReason,
		Usage: llm.Usage{
			PromptTokens:     wr.Usage.PromptTokens,
			CompletionTokens: wr.Usage.CompletionTokens,
			TotalTokens:      wr.Usage.TotalTokens,
		},
		Message: llm.Message{
			Role:    llm.Role(first.Message.Role),
			Content: first.Message.Content,
		},
	}
	for _, tc := range first.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return out, nil
}
