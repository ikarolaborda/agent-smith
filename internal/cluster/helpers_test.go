package cluster

import (
	"context"
	"io"
	"log/slog"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* discardLogger is a no-op logger for tests. */
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

/*
fakeProvider is a deterministic llm.Provider used to stand in for the local
single-node runner in fallback tests. It emits one chunk per word of tokens.
*/
type fakeProvider struct {
	name   string
	tokens []string
	/* chunks, when set, are streamed verbatim instead of tokens. */
	chunks []llm.StreamChunk
	err    error
	/* streamCalls counts ChatStream invocations to assert no fallback replay. */
	streamCalls int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	content := ""
	for _, t := range f.tokens {
		content += t
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: content}, FinishReason: "stop"}, nil
}

func (f *fakeProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	f.streamCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan llm.StreamChunk, len(f.tokens)+len(f.chunks)+1)
	go func() {
		defer close(out)
		if len(f.chunks) > 0 {
			for _, c := range f.chunks {
				out <- c
			}
			out <- llm.StreamChunk{Done: true}
			return
		}
		for _, t := range f.tokens {
			out <- llm.StreamChunk{Delta: t}
		}
		out <- llm.StreamChunk{Done: true}
	}()
	return out, nil
}

/* testConfig builds a normalized two-node cluster config for scheduler tests. */
func testConfig() *ClusterConfig {
	cfg := &ClusterConfig{
		Cluster: ClusterTopology{
			Mode:        "auto",
			Coordinator: "m5max",
			Nodes: []Node{
				{ID: "m5max", Host: "127.0.0.1", Role: RoleCoordinator, MemoryGB: 64, GPUCores: 40, Priority: 100,
					Backends: []string{BackendExo, BackendMLXJACCL, BackendLlamaRPC, BackendLocal}},
				/* 127.0.0.2 refuses connections instantly (vs a DNS+dial timeout for a
				   real .local name), keeping discovery-driven tests fast while still
				   exercising the non-coordinator code path. */
				{ID: "m5pro", Host: "127.0.0.2", Role: RoleWorker, MemoryGB: 24, GPUCores: 20, Priority: 50,
					Backends: []string{BackendExo, BackendMLXJACCL, BackendLlamaRPC}},
			},
		},
		Models: []ModelConfig{{
			ID:                "70b-q4-default",
			Path:              "/models/70b-q4",
			ContextTokens:     8192,
			PreferredBackends: []string{BackendExo, BackendMLXJACCL, BackendLlamaRPC, BackendLocal},
			MinMemoryGB:       48,
		}},
	}
	cfg.Normalize()
	return cfg
}
