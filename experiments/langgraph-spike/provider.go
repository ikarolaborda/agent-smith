/*
Package main is an isolated spike proving that agent-smith's own llm.Provider
can drive a smallnest/langgraphgo graph through a langchaingo llms.Model adapter.

The types below MIRROR internal/llm (Provider, Message, ChatRequest,
ChatResponse, Role) field-for-field on the parts the adapter touches. They are
copied rather than imported because internal/llm is import-restricted and this
is a separate module by design; the adapter logic is therefore copy-paste
portable into the main tree, where it would wrap the real llm.Provider 1:1.
*/
package main

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

/* Message mirrors internal/llm.Message (subset the adapter needs). */
type Message struct {
	Role    Role
	Content string
}

/* ChatRequest mirrors internal/llm.ChatRequest (subset). */
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
}

/* ChatResponse mirrors internal/llm.ChatResponse (subset). */
type ChatResponse struct {
	Message      Message
	FinishReason string
}

/*
Provider is agent-smith's pluggable chat backend, reproduced here so the adapter
exercises the exact same contract the product exposes.
*/
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
