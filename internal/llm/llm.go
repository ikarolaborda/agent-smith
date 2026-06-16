/*
Package llm defines the provider-agnostic chat completion interface and the
shared message, tool, and streaming types that every provider implementation
(OpenAI, Anthropic, Ollama, ...) maps onto.

The shapes here are intentionally minimal and lossless: each provider adapter
is responsible for translating between this canonical model and its own wire
format (OpenAI tool_calls array, Anthropic content blocks of type tool_use,
Ollama tool_calls, etc).
*/
package llm

import (
	"context"
	"encoding/json"
)

/* Role identifies who produced a message in a conversation. */
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

/*
Message is a single turn in a conversation. ToolCalls is populated on
assistant messages that requested tool execution. ToolCallID and Name are
populated on tool messages that carry tool-execution results back to the
model.
*/
type Message struct {
	Role       Role        `json:"role"`
	Content    string      `json:"content,omitempty"`
	Images     []ImagePart `json:"images,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
}

/*
ImagePart is an inline image attached to a (typically user) message. Data is
the raw image bytes encoded as standard base64 with no data: URL prefix, so
each provider adapter can re-wrap it in its own format (OpenAI data URL,
Anthropic base64 source block, Ollama images array). MimeType is the image
media type, e.g. "image/png". Only models advertising vision support will do
anything useful with these.
*/
type ImagePart struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

/*
ToolCall represents a single function/tool invocation requested by the model.
Arguments is the raw JSON-encoded argument object as emitted by the provider;
adapters must not pre-parse it so that downstream tools can validate against
their own schemas.
*/
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

/*
ToolDefinition is the canonical description of a tool advertised to the
provider. Parameters is a JSON Schema object describing the accepted
arguments; the provider adapter is responsible for mapping it into the
provider's native tool/function-call schema.
*/
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

/*
ChatRequest is the canonical request payload sent to a Provider. Temperature,
MaxTokens, and Think are pointers so callers can distinguish "leave default"
from an explicit value. Think toggles a reasoning model's chain-of-thought:
some providers (Ollama) expose this directly, and a nil value leaves the
provider/model default untouched. Providers that have no notion of reasoning
control simply ignore it.
*/
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Think       *bool            `json:"think,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	/*
		NumCtx requests a context-window size for backends that accept one per
		request (Ollama's num_ctx). A nil value leaves the provider/model default.
		The cluster sets it from a model's context_tokens so a single-node model
		actually serves the large window it was configured for, instead of
		silently falling back to the runtime's small default.
	*/
	NumCtx *int `json:"num_ctx,omitempty"`
}

/* Usage reports token accounting for a completed (non-streaming) request. */
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

/*
ChatResponse is the canonical non-streaming response from a Provider.
FinishReason is provider-specific but normalized where possible
(e.g. "stop", "tool_calls", "length").
*/
type ChatResponse struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
}

/*
StreamChunk is one event from a streaming chat completion. Exactly one of
Delta, ToolCallDelta, Done=true, or Err is meaningfully populated per chunk.
The channel returned by ChatStream MUST be closed by the producer when the
stream ends; consumers should range over it.
*/
type StreamChunk struct {
	Delta         string    `json:"delta,omitempty"`
	ToolCallDelta *ToolCall `json:"tool_call_delta,omitempty"`
	Done          bool      `json:"done,omitempty"`
	Err           error     `json:"-"`
}

/*
Provider is the pluggable chat-completion backend. Implementations live under
internal/llm/<provider>/. Both methods MUST respect ctx for cancellation and
timeouts.
*/
type Provider interface {
	/*
		Name returns a stable identifier (e.g. "openai", "anthropic", "ollama")
		used in logs and registry lookups.
	*/
	Name() string

	/* Chat performs a non-streaming chat completion. */
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)

	/*
		ChatStream performs a streaming chat completion. The returned channel
		is closed by the implementation when the stream terminates.
	*/
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
}
