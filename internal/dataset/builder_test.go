package dataset_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/dataset"
	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* echoProvider returns the source content back and records the temperature it was called with. */
type echoProvider struct{ lastTemp *float64 }

func (*echoProvider) Name() string { return "echo" }

func (p *echoProvider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.lastTemp = req.Temperature
	last := req.Messages[len(req.Messages)-1].Content
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "answer-from:" + last}}, nil
}

func (*echoProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestBuild_ProducesGroundedChatSFTRecords(t *testing.T) {
	p := &echoProvider{}
	items := []dataset.SourceItem{
		{ID: "a", Content: "CVE-2024-0001 affects nginx."},
		{ID: "b", Content: "TLS 1.3 removes renegotiation."},
	}

	recs, skipped, err := dataset.Build(context.Background(), p, items, dataset.Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if skipped != 0 || len(recs) != 2 {
		t.Fatalf("expected 2 records 0 skipped, got %d/%d", len(recs), skipped)
	}
	if p.lastTemp == nil || *p.lastTemp != dataset.DefaultTemperature {
		t.Fatalf("expected low default temperature %v, got %v", dataset.DefaultTemperature, p.lastTemp)
	}

	r := recs[0]
	if len(r.Messages) != 3 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" || r.Messages[2].Role != "assistant" {
		t.Fatalf("record must be system/user/assistant, got %+v", r.Messages)
	}
	if !strings.HasPrefix(r.Messages[2].Content, "answer-from:") || !strings.Contains(r.Messages[2].Content, "CVE-2024-0001") {
		t.Fatalf("assistant content must be grounded in the source item: %q", r.Messages[2].Content)
	}
}

func TestWriteJSONL_IsValidOpenAIChatFormat(t *testing.T) {
	recs := []dataset.Record{{Messages: []dataset.Msg{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "a"},
	}}}

	var buf bytes.Buffer
	if err := dataset.WriteJSONL(&buf, recs); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}
	line := strings.TrimSpace(buf.String())
	var got struct {
		Messages []dataset.Msg `json:"messages"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("each line must be a valid {messages:[...]} object: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
}
