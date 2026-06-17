package validate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* fakeProvider returns a canned chat response and records the request. */
type fakeProvider struct {
	last llm.ChatRequest
	resp string
	err  error
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.last = req
	if p.err != nil {
		return nil, p.err
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: p.resp}}, nil
}
func (p *fakeProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestProviderReviewer_LowTempAndContent(t *testing.T) {
	p := &fakeProvider{resp: "The CVSS score looks invented."}
	r := NewProviderReviewer("OpenAI", p)
	out, err := r.Review(context.Background(), "answer citing CVE-2099-0001")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if out != "The CVSS score looks invented." {
		t.Fatalf("verdict passthrough wrong: %q", out)
	}
	if p.last.Temperature == nil || *p.last.Temperature != lowReviewTemperature {
		t.Fatalf("reviewer must use low temperature, got %v", p.last.Temperature)
	}
	if len(p.last.Messages) != 2 || p.last.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected system+user messages, got %+v", p.last.Messages)
	}
}

func TestProviderReviewer_NilProviderOmitted(t *testing.T) {
	if r := NewProviderReviewer("OpenAI", nil); r != nil {
		t.Fatalf("nil provider must yield nil reviewer")
	}
	if r := NewOpenAIReviewer("", "", ""); r != nil {
		t.Fatalf("empty api key must yield nil OpenAI reviewer")
	}
}

func TestClaudeCLIReviewer_ParsesResultField(t *testing.T) {
	r := &ClaudeCLIReviewer{name: "Claude (Max)", bin: "claude", model: "sonnet",
		run: func(_ context.Context, _, _, prompt string) ([]byte, error) {
			if !strings.Contains(prompt, "Vulnerability-research answer to review") {
				t.Fatalf("CLI prompt missing framing: %q", prompt)
			}
			return []byte(`{"result":"No NVD record; likely fabricated.","session_id":"x"}`), nil
		}}
	out, err := r.Review(context.Background(), "exploit for CVE-2099-0001")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if out != "No NVD record; likely fabricated." {
		t.Fatalf("must parse .result: %q", out)
	}
}

func TestClaudeCLIReviewer_RawFallbackWhenNotJSON(t *testing.T) {
	r := &ClaudeCLIReviewer{name: "Claude (Max)", bin: "claude", model: "sonnet",
		run: func(context.Context, string, string, string) ([]byte, error) {
			return []byte("plain text verdict"), nil
		}}
	out, err := r.Review(context.Background(), "RCE in service")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if out != "plain text verdict" {
		t.Fatalf("must fall back to raw output: %q", out)
	}
}

func TestClaudeCLIReviewer_SubprocessErrorPropagates(t *testing.T) {
	r := &ClaudeCLIReviewer{name: "Claude (Max)", bin: "claude", model: "sonnet",
		run: func(context.Context, string, string, string) ([]byte, error) {
			return nil, errors.New("exit status 1")
		}}
	if _, err := r.Review(context.Background(), "buffer overflow"); err == nil {
		t.Fatalf("subprocess error must propagate so the validator degrades softly")
	}
}

func TestSanitizedEnv_HasMarkerAndNoLeak(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("SECRET_TOKEN", "leak-me")
	env := sanitizedEnv()
	var hasMarker, hasHome, leaked bool
	for _, e := range env {
		switch {
		case e == "AGENT_SMITH_VALIDATION=1":
			hasMarker = true
		case strings.HasPrefix(e, "HOME="):
			hasHome = true
		case strings.HasPrefix(e, "SECRET_TOKEN="):
			leaked = true
		}
	}
	if !hasMarker || !hasHome {
		t.Fatalf("sanitized env must keep HOME and set the recursion marker: %v", env)
	}
	if leaked {
		t.Fatalf("sanitized env must not leak arbitrary parent vars")
	}
}
