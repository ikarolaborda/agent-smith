package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/pkg/prompt"
)

/*
composeMessages must attach the agentic directive when Agentic is set, and must
NOT attach it otherwise — the gate that keeps the classic one-shot RAG path (and
offline models) unchanged by default.
*/
func TestComposeMessagesAgenticGating(t *testing.T) {
	sess := NewSession()
	sess.Append(llm.Message{Role: llm.RoleUser, Content: "what does the manual say about auth?"})

	agentic := &Agent{Agentic: true}
	sys := systemContent(agentic.composeMessages(context.Background(), sess))
	if !strings.Contains(sys, prompt.AgenticRAGDirective) {
		t.Error("agentic mode must attach the agentic-RAG directive")
	}

	classic := &Agent{Agentic: false}
	sys = systemContent(classic.composeMessages(context.Background(), sess))
	if strings.Contains(sys, prompt.AgenticRAGDirective) {
		t.Error("classic mode must NOT attach the agentic-RAG directive")
	}
}

func systemContent(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role == llm.RoleSystem {
			return m.Content
		}
	}
	return ""
}
