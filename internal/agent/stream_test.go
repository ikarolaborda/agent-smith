package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/* stubStreamProvider returns a canned set of chunks on every ChatStream call. */
type stubStreamProvider struct {
	chunks []llm.StreamChunk
}

func (s *stubStreamProvider) Name() string { return "stub" }
func (s *stubStreamProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (s *stubStreamProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk, len(s.chunks)+1)
	for _, c := range s.chunks {
		out <- c
	}
	close(out)
	return out, nil
}

func TestRunStream_EmitsTextDeltasAndDone(t *testing.T) {
	p := &stubStreamProvider{chunks: []llm.StreamChunk{
		{Delta: "abc"},
		{Delta: "def"},
		{Done: true},
	}}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)

	var kinds []agent.StreamEventKind
	var content string
	sink := agent.SinkFunc(func(ev agent.StreamEvent) error {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == agent.StreamEventDone {
			content = ev.FinalContent
		}
		return nil
	})

	got, err := a.RunStream(context.Background(), agent.NewSession(), "hi", sink)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if got != "abcdef" {
		t.Fatalf("content: %q", got)
	}
	if content != "abcdef" {
		t.Fatalf("done.FinalContent: %q", content)
	}
	if len(kinds) < 3 || kinds[len(kinds)-1] != agent.StreamEventDone {
		t.Fatalf("ordering: %v", kinds)
	}
}

/* noteVerifier appends a fixed advisory whenever the answer mentions "CVE". */
type noteVerifier struct{ calls int }

func (n *noteVerifier) Verify(_ context.Context, text string) (string, error) {
	n.calls++
	if !containsCVE(text) {
		return "", nil
	}
	return "\n\n[verify] checked", nil
}

func containsCVE(s string) bool {
	for i := 0; i+3 <= len(s); i++ {
		if s[i] == 'C' && s[i+1] == 'V' && s[i+2] == 'E' {
			return true
		}
	}
	return false
}

func TestRunStream_AppendsVerificationAdvisoryBeforeDone(t *testing.T) {
	p := &stubStreamProvider{chunks: []llm.StreamChunk{
		{Delta: "patch CVE-2021-44228"},
		{Done: true},
	}}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	a.Verifier = &noteVerifier{}

	var deltas []string
	var doneContent string
	var lastKind agent.StreamEventKind
	sink := agent.SinkFunc(func(ev agent.StreamEvent) error {
		switch ev.Kind {
		case agent.StreamEventTextDelta:
			deltas = append(deltas, ev.Delta)
		case agent.StreamEventDone:
			doneContent = ev.FinalContent
		}
		lastKind = ev.Kind
		return nil
	})

	got, err := a.RunStream(context.Background(), agent.NewSession(), "hi", sink)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if lastKind != agent.StreamEventDone {
		t.Fatalf("advisory delta must come before Done; last kind was %v", lastKind)
	}
	if len(deltas) == 0 || deltas[len(deltas)-1] != "\n\n[verify] checked" {
		t.Fatalf("advisory must be emitted as the final text delta: %v", deltas)
	}
	want := "patch CVE-2021-44228\n\n[verify] checked"
	if got != want || doneContent != want {
		t.Fatalf("final content must include advisory once: return=%q done=%q", got, doneContent)
	}
}

func TestRunStream_NilVerifierIsUnchanged(t *testing.T) {
	p := &stubStreamProvider{chunks: []llm.StreamChunk{{Delta: "CVE-2021-44228 here"}, {Done: true}}}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	got, err := a.RunStream(context.Background(), agent.NewSession(), "hi", agent.SinkFunc(func(agent.StreamEvent) error { return nil }))
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if got != "CVE-2021-44228 here" {
		t.Fatalf("nil verifier must not alter output: %q", got)
	}
}

/* blockingProvider produces a channel that only respects ctx for cancellation. */
type blockingProvider struct{}

func (blockingProvider) Name() string { return "block" }
func (blockingProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (blockingProvider) ChatStream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		<-ctx.Done()
	}()
	return out, nil
}

func TestRunStream_ContextCancellationStopsLoop(t *testing.T) {
	a := agent.New(blockingProvider{}, tools.NewRegistry(), "", 3, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.RunStream(ctx, agent.NewSession(), "go", agent.SinkFunc(func(agent.StreamEvent) error { return nil }))
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

/*
leakProbeProvider streams via llm.SendChunk (like the real producers) on an
unbuffered channel and closes exited only when its goroutine returns. If the
agent fails to cancel the round on a sink error, the goroutine parks forever.
*/
type leakProbeProvider struct{ exited chan struct{} }

func (leakProbeProvider) Name() string { return "leakprobe" }
func (leakProbeProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (p leakProbeProvider) ChatStream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		defer close(p.exited)
		for i := 0; i < 100000; i++ {
			if !llm.SendChunk(ctx, out, llm.StreamChunk{Delta: "x"}) {
				return
			}
		}
	}()
	return out, nil
}

func TestRunStream_SinkErrorCancelsProducer(t *testing.T) {
	p := leakProbeProvider{exited: make(chan struct{})}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)

	sinkErr := errors.New("client disconnected")
	_, err := a.RunStream(context.Background(), agent.NewSession(), "go",
		agent.SinkFunc(func(agent.StreamEvent) error { return sinkErr }))
	if err == nil {
		t.Fatalf("expected the sink error to propagate")
	}

	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("producer goroutine leaked: not cancelled after the consumer stopped reading")
	}
}
