package agent

import (
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

func TestCompactableMsg(t *testing.T) {
	cases := []struct {
		name string
		msg  llm.Message
		want bool
	}{
		{"user", llm.Message{Role: llm.RoleUser, Content: "x"}, true},
		{"tool", llm.Message{Role: llm.RoleTool, Content: "x"}, true},
		{"assistant answer", llm.Message{Role: llm.RoleAssistant, Content: "a long prior answer"}, true},
		{
			"assistant with tool calls",
			llm.Message{Role: llm.RoleAssistant, Content: "x", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_dir"}}},
			false,
		},
		{"system", llm.Message{Role: llm.RoleSystem, Content: "x"}, false},
	}
	for _, c := range cases {
		if got := compactableMsg(c.msg); got != c.want {
			t.Errorf("%s: compactableMsg = %v, want %v", c.name, got, c.want)
		}
	}
}
