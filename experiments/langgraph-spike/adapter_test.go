package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

/*
fakeProvider returns a canned reply and records the last request, so the adapter
and graph can be tested with no network and no model.
*/
type fakeProvider struct{ last ChatRequest }

func (*fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	f.last = req
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	return &ChatResponse{Message: Message{Role: RoleAssistant, Content: "echo:" + last}, FinishReason: "stop"}, nil
}

/*
TestAdapter_MapsRolesAndContent asserts the llms.Model adapter maps langchaingo
roles + text parts onto agent-smith messages and returns a single content choice.
*/
func TestAdapter_MapsRolesAndContent(t *testing.T) {
	fp := &fakeProvider{}
	var m llms.Model = NewProviderModel(fp, "test")

	resp, err := m.GenerateContent(context.Background(), []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "sys"),
		llms.TextParts(llms.ChatMessageTypeHuman, "hi"),
	})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Content != "echo:hi" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(fp.last.Messages) != 2 || fp.last.Messages[0].Role != RoleSystem || fp.last.Messages[1].Role != RoleUser {
		t.Fatalf("role mapping wrong: %+v", fp.last.Messages)
	}
}

/*
TestGraph_RunsEndToEnd runs the two-node graph against the fake model and asserts
both nodes executed (draft + output present, output from the ground_check node).
*/
func TestGraph_RunsEndToEnd(t *testing.T) {
	m := NewProviderModel(&fakeProvider{}, "test")

	final, err := runGraph(context.Background(), m, "q")
	if err != nil {
		t.Fatalf("runGraph: %v", err)
	}
	if _, ok := final["draft"].(string); !ok {
		t.Fatalf("answer node did not run: %+v", final)
	}
	if out, _ := final["output"].(string); !strings.HasPrefix(out, "echo:") {
		t.Fatalf("ground_check node did not run: %+v", final)
	}
}
