package main

import (
	"context"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

/*
ProviderModel adapts an agent-smith Provider to langchaingo's llms.Model, which
is the interface smallnest/langgraphgo consumes. This is the whole point of the
spike: agent-smith keeps its own provider abstraction (local Ollama, cluster,
cloud) and we expose it to the graph engine without rewriting either side.

In the main tree this same type would wrap internal/llm.Provider; the mapping is
identical because the spike's Provider mirrors it.
*/
type ProviderModel struct {
	provider Provider
	model    string
}

func NewProviderModel(p Provider, model string) *ProviderModel {
	return &ProviderModel{provider: p, model: model}
}

/* compile-time assertion that we satisfy the interface the graph engine wants. */
var _ llms.Model = (*ProviderModel)(nil)

/*
GenerateContent maps langchaingo's multi-modal message slice onto agent-smith's
ChatRequest, calls the provider, and maps the response back into a single-choice
ContentResponse. Only text parts are forwarded — the spike's graph is text-only.
*/
func (m *ProviderModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	var opts llms.CallOptions
	for _, o := range options {
		o(&opts)
	}

	req := ChatRequest{Model: m.model}
	for _, mc := range messages {
		req.Messages = append(req.Messages, Message{
			Role:    mapRole(mc.Role),
			Content: textOf(mc.Parts),
		})
	}
	if opts.Temperature != 0 {
		t := opts.Temperature
		req.Temperature = &t
	}
	if opts.MaxTokens != 0 {
		mt := opts.MaxTokens
		req.MaxTokens = &mt
	}

	resp, err := m.provider.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content:    resp.Message.Content,
			StopReason: resp.FinishReason,
		}},
	}, nil
}

/*
Call is the deprecated single-prompt entrypoint langchaingo still exposes; the
graph examples use it, so we implement it by delegating to GenerateContent.
*/
func (m *ProviderModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	resp, err := m.GenerateContent(ctx, []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeHuman, prompt),
	}, options...)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return resp.Choices[0].Content, nil
}

/* mapRole translates langchaingo chat roles to agent-smith roles. */
func mapRole(r llms.ChatMessageType) Role {
	switch r {
	case llms.ChatMessageTypeSystem:
		return RoleSystem
	case llms.ChatMessageTypeAI:
		return RoleAssistant
	case llms.ChatMessageTypeTool, llms.ChatMessageTypeFunction:
		return RoleTool
	default:
		return RoleUser
	}
}

/* textOf concatenates the text parts of a message, ignoring non-text content. */
func textOf(parts []llms.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if tc, ok := p.(llms.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
