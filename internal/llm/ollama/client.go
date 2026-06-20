/*
Package ollama implements llm.Provider against a local Ollama server. The
wire format is JSON; streaming responses are newline-delimited JSON, not
SSE, and no API key is required because Ollama is typically reached over
localhost.
*/
package ollama

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

/* DefaultBaseURL points at a local Ollama daemon. */
const DefaultBaseURL = "http://localhost:11434"

/* providerName is the registry key used to construct this provider. */
const providerName = "ollama"

/* Config carries the settings needed to construct a Client. */
type Config struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

/* Client is the Ollama implementation of llm.Provider. */
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

/*
New constructs a Client. Missing BaseURL falls back to DefaultBaseURL; a nil
HTTP client gets a 5-minute timeout because local generations can run long.
*/
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTP == nil {
		/*
			Transport backstop only — the per-request context deadline is the real
			bound. Large local models (e.g. a 60B served via Ollama) can take several
			minutes for a single grounded, long-context generation; 5m cut healthy
			refine rounds before their context deadline, so this is a finite-but-
			generous backstop against truly hung sockets, not a generation budget.
		*/
		cfg.HTTP = &http.Client{Timeout: 15 * time.Minute}
	}
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		http:    cfg.HTTP,
	}, nil
}

func init() {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		c, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("ollama: expected ollama.Config, got %T", cfg)
		}
		return New(c)
	})
}

func (c *Client) Name() string { return providerName }

/* wire types */

type wireToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type wireToolCall struct {
	Function wireToolCallFunction `json:"function"`
}

type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content,omitempty"`
	Name      string         `json:"name,omitempty"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type wireTool struct {
	Type     string           `json:"type"`
	Function wireToolFunction `json:"function"`
}

type wireOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
	NumCtx      *int     `json:"num_ctx,omitempty"`
}

type wireRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
	Options  *wireOptions  `json:"options,omitempty"`
	/*
		Think is Ollama's top-level reasoning toggle. It is a pointer so the
		field is omitted entirely (leaving the model default) unless a caller
		sets ChatRequest.Think; setting it false makes reasoning models answer
		directly instead of spending the token budget on a thinking span.
	*/
	Think *bool `json:"think,omitempty"`
}

type wireResponse struct {
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

func (c *Client) buildRequest(req llm.ChatRequest, stream bool) wireRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}

	out := wireRequest{
		Model:  model,
		Stream: stream,
		Think:  req.Think,
	}

	if req.Temperature != nil || req.MaxTokens != nil || req.NumCtx != nil {
		out.Options = &wireOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
			NumCtx:      req.NumCtx,
		}
	}

	for _, m := range req.Messages {
		wm := wireMessage{
			Role:    string(m.Role),
			Content: m.Content,
			Name:    m.Name,
		}
		/* Ollama's /api/chat carries images as a per-message base64 array. */
		for _, img := range m.Images {
			wm.Images = append(wm.Images, img.Data)
		}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				Function: wireToolCallFunction{Name: tc.Name, Arguments: args},
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

func (c *Client) doRequest(ctx context.Context, body wireRequest) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal: %w", err)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("ollama: new request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("ollama: http: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp, nil
}

/* Chat performs a non-streaming POST /api/chat call. */
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	resp, err := c.doRequest(ctx, c.buildRequest(req, false))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var wr wireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if !wr.Done {
		return nil, errors.New("ollama: response not marked done")
	}

	out := &llm.ChatResponse{
		FinishReason: "stop",
		Usage: llm.Usage{
			PromptTokens:     wr.PromptEvalCount,
			CompletionTokens: wr.EvalCount,
			TotalTokens:      wr.PromptEvalCount + wr.EvalCount,
		},
		Message: llm.Message{
			Role:    llm.Role(wr.Message.Role),
			Content: wr.Message.Content,
		},
	}
	for _, tc := range wr.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	if len(out.Message.ToolCalls) > 0 {
		out.FinishReason = "tool_calls"
	}
	return out, nil
}
