/*
Package anthropic implements llm.Provider against the Anthropic Messages API
(POST /v1/messages). The system prompt is carried in a top-level "system"
field rather than as a role; tool calls and tool results map onto content
blocks of type "tool_use" and "tool_result" respectively.
*/
package anthropic

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

/* DefaultBaseURL is the public Anthropic API root. */
const DefaultBaseURL = "https://api.anthropic.com"

/* AnthropicVersion is the wire-protocol header value required by /v1/messages. */
const AnthropicVersion = "2023-06-01"

/* providerName is the registry key used to construct this provider. */
const providerName = "anthropic"

/* defaultMaxTokens is used when ChatRequest.MaxTokens is nil; Anthropic requires the field. */
const defaultMaxTokens = 1024

/* Config carries the settings needed to construct a Client. */
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
}

/* Client is the Anthropic implementation of llm.Provider. */
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

/* New constructs a Client. */
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: APIKey is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTP == nil {
		cfg.HTTP = newStreamingClient()
	}
	return &Client{
		apiKey:  cfg.APIKey,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		http:    cfg.HTTP,
	}, nil
}

/*
newStreamingClient returns an HTTP client safe for streaming. A fixed
http.Client.Timeout covers the whole response body, so a 30s timeout aborted any
generation longer than 30s mid-stream. Bound only the connect/handshake/
first-byte phases and leave the streamed body to the per-request context.
*/
func newStreamingClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}

func init() {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("anthropic: expected anthropic.Config, got %T", cfg)
		}
		return New(c)
	})
}

func (c *Client) Name() string { return providerName }

/* wire types */

type wireImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type wireBlock struct {
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   string           `json:"content,omitempty"`
	Source    *wireImageSource `json:"source,omitempty"`
	IsError   bool             `json:"is_error,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireRequest struct {
	Model       string        `json:"model"`
	MaxTokens   int           `json:"max_tokens"`
	System      string        `json:"system,omitempty"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type wireResponse struct {
	Content    []wireBlock `json:"content"`
	StopReason string      `json:"stop_reason"`
	Usage      wireUsage   `json:"usage"`
}

type errorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

/*
buildRequest splits a canonical ChatRequest into Anthropic's system + messages
shape. System-role llm.Messages are concatenated into the top-level "system"
field; tool messages become tool_result blocks attached to the next user
turn; assistant tool calls become tool_use blocks.
*/
func (c *Client) buildRequest(req llm.ChatRequest, stream bool) wireRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := defaultMaxTokens
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	out := wireRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}

	var systemParts []string
	var current *wireMessage
	flush := func() {
		if current != nil {
			out.Messages = append(out.Messages, *current)
			current = nil
		}
	}

	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}

		case llm.RoleTool:
			/*
				Anthropic carries tool results as a user-turn block. Coalesce
				consecutive tool messages into one user message.
			*/
			if current == nil || current.Role != "user" {
				flush()
				current = &wireMessage{Role: "user"}
			}
			current.Content = append(current.Content, wireBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})

		default:
			flush()
			role := "user"
			if m.Role == llm.RoleAssistant {
				role = "assistant"
			}
			current = &wireMessage{Role: role}
			if m.Content != "" {
				current.Content = append(current.Content, wireBlock{Type: "text", Text: m.Content})
			}
			/* Anthropic carries images as base64 source blocks alongside text. */
			for _, img := range m.Images {
				mime := img.MimeType
				if mime == "" {
					mime = "image/png"
				}
				current.Content = append(current.Content, wireBlock{
					Type:   "image",
					Source: &wireImageSource{Type: "base64", MediaType: mime, Data: img.Data},
				})
			}
			for _, tc := range m.ToolCalls {
				input := tc.Arguments
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				current.Content = append(current.Content, wireBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: input,
				})
			}
		}
	}
	flush()
	out.System = strings.Join(systemParts, "\n\n")

	for _, t := range req.Tools {
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out.Tools = append(out.Tools, wireTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

func (c *Client) doRequest(ctx context.Context, body wireRequest, accept string) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	r.Header.Set("x-api-key", c.apiKey)
	r.Header.Set("anthropic-version", AnthropicVersion)
	r.Header.Set("Content-Type", "application/json")
	if accept != "" {
		r.Header.Set("Accept", accept)
	}

	resp, err := c.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var env errorEnvelope
		if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
			return nil, fmt.Errorf("anthropic: %s (%s)", env.Error.Message, env.Error.Type)
		}
		return nil, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp, nil
}

/* Chat performs a non-streaming POST /v1/messages call. */
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := c.doRequest(ctx, c.buildRequest(req, false), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var wr wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}

	out := &llm.ChatResponse{
		FinishReason: wr.StopReason,
		Usage: llm.Usage{
			PromptTokens:     wr.Usage.InputTokens,
			CompletionTokens: wr.Usage.OutputTokens,
			TotalTokens:      wr.Usage.InputTokens + wr.Usage.OutputTokens,
		},
		Message: llm.Message{Role: llm.RoleAssistant},
	}

	var text strings.Builder
	for _, b := range wr.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
				ID:        b.ID,
				Name:      b.Name,
				Arguments: input,
			})
		}
	}
	out.Message.Content = text.String()
	return out, nil
}
