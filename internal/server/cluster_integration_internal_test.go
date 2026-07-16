package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/web"
)

/* clusterStub is a minimal llm.Provider standing in for the cluster provider. */
type clusterStub struct{}

func (clusterStub) Name() string { return "cluster" }

func (clusterStub) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}

func (clusterStub) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

/*
The cluster provider serves local models, so it must default to web grounding
exactly like Ollama — otherwise augmented context would silently switch off in
cluster mode. The operator kill switch and per-request override must still win.
*/
func TestClusterProviderGetsWebGroundingDefault(t *testing.T) {
	s := &Server{webSearchEnabled: true, rag: &rag.Service{WebSearch: web.NewDDGSearcher()}}

	if !s.shouldWebSearch("cluster", nil) {
		t.Error("cluster should default web grounding ON")
	}
	if !s.shouldWebSearch("ollama", nil) {
		t.Error("ollama should default web grounding ON")
	}
	if !s.shouldWebSearch("llamacpp", nil) {
		t.Error("self-managed llamacpp should default web grounding ON")
	}
	if s.shouldWebSearch("openai", nil) {
		t.Error("cloud provider should default web grounding OFF")
	}

	off := false
	if s.shouldWebSearch("cluster", &off) {
		t.Error("per-request override (false) must win over the cluster default")
	}

	s.webSearchEnabled = false
	if s.shouldWebSearch("cluster", nil) {
		t.Error("operator kill switch must win over the cluster default")
	}
}

/*
RAG, long-term memory, and augmentation live in the agent layer (agent.RAG),
wrapping any provider. This proves the cluster provider inherits them: a request
routed to "cluster" gets an agent with RAG attached, identical to any other
provider.
*/
func TestClusterProviderInheritsRAGAugmentation(t *testing.T) {
	s := &Server{
		providers: map[string]llm.Provider{"cluster": clusterStub{}},
		rag:       &rag.Service{},
		cfg:       &config.Config{},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	a, err := s.newAgent("cluster")
	if err != nil {
		t.Fatalf("newAgent(cluster): %v", err)
	}
	if a.RAG == nil {
		t.Fatal("cluster agent must inherit the RAG/memory/augmentation service")
	}

	/* DisableRAG must suppress augmentation for cluster too (no special-casing). */
	s.disableRAG = true
	a2, err := s.newAgent("cluster")
	if err != nil {
		t.Fatalf("newAgent(cluster) with disableRAG: %v", err)
	}
	if a2.RAG != nil {
		t.Error("disableRAG must suppress RAG for the cluster provider as well")
	}
}
