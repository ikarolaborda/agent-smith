package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/*
fakeProvider is a minimal llm.Provider used to exercise the agent loop. It
returns a queue of pre-recorded responses, one per Chat call.
*/
type fakeProvider struct {
	responses []llm.ChatResponse
	idx       int
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	if f.idx >= len(f.responses) {
		return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
	}
	r := f.responses[f.idx]
	f.idx++
	return &r, nil
}

func (f *fakeProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestAgent_RunReturnsAssistantContentWhenNoToolCalls(t *testing.T) {
	p := &fakeProvider{
		responses: []llm.ChatResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, Content: "hello back"}},
		},
	}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	got, err := a.Run(context.Background(), agent.NewSession(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "hello back" {
		t.Fatalf("got %q want %q", got, "hello back")
	}
}

/* capturingProvider records the last ChatRequest so tests can inspect the composed messages. */
type capturingProvider struct {
	last llm.ChatRequest
}

func (c *capturingProvider) Name() string { return "cap" }

func (c *capturingProvider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.last = req
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (c *capturingProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestAgent_AlwaysAppliesCodingParadigmDirective(t *testing.T) {
	/* Even with no configured system prompt, every request must carry the OOP directive. */
	p := &capturingProvider{}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	if _, err := a.Run(context.Background(), agent.NewSession(), "write me a parser"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.last.Messages) == 0 || p.last.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected a leading system message, got %+v", p.last.Messages)
	}
	sys := p.last.Messages[0].Content
	if !strings.Contains(sys, "object-oriented") || !strings.Contains(strings.ToLower(sys), "procedural") {
		t.Fatalf("system message missing OOP directive: %q", sys)
	}
}

/*
The enforced engineering + defensive-security directive must reach every request
regardless of the configured system prompt. This guards the only behavioral
safety boundary for the (abliterated) clustered model against accidental removal
in a future refactor of message composition.
*/
func TestAgent_AlwaysAppliesEngineeringDirective(t *testing.T) {
	p := &capturingProvider{}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	if _, err := a.Run(context.Background(), agent.NewSession(), "review this code"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.last.Messages) == 0 || p.last.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected a leading system message, got %+v", p.last.Messages)
	}
	sys := strings.ToLower(p.last.Messages[0].Content)
	for _, needle := range []string{"clean architecture", "context7 is mandatory", "grounding is the hard rule", "never fabricate a cve"} {
		if !strings.Contains(sys, strings.ToLower(needle)) {
			t.Fatalf("system message missing enforced directive clause %q: %q", needle, p.last.Messages[0].Content)
		}
	}
}

/*
The default persona (informal cybersecurity software architect) must reach every
request so the voice is consistent across all models and languages. Guards the
PersonaDirective against accidental drop in a message-composition refactor.
*/
func TestAgent_AlwaysAppliesPersona(t *testing.T) {
	p := &capturingProvider{}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	if _, err := a.Run(context.Background(), agent.NewSession(), "hey"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.last.Messages) == 0 || p.last.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected a leading system message, got %+v", p.last.Messages)
	}
	sys := strings.ToLower(p.last.Messages[0].Content)
	for _, needle := range []string{"software architect", "cybersecurity", "swearing is allowed", "reply in the user's language"} {
		if !strings.Contains(sys, strings.ToLower(needle)) {
			t.Fatalf("system message missing persona clause %q: %q", needle, p.last.Messages[0].Content)
		}
	}
}

/*
The anti-fabrication grounding guardrail must reach every request so the model is
always told to assert security specifics (CVE ids, CVSS, version ranges) only from
retrieved context, to treat its own recall as unverified, and to honor known
corrections. This guards the directive that suppresses the observed CVE
fabrication against accidental removal in a message-composition refactor.
*/
func TestAgent_AlwaysAppliesGroundingGuardrail(t *testing.T) {
	p := &capturingProvider{}
	a := agent.New(p, tools.NewRegistry(), "", 3, nil)
	if _, err := a.Run(context.Background(), agent.NewSession(), "what CVE affects PHP 7.4?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.last.Messages) == 0 || p.last.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("expected a leading system message, got %+v", p.last.Messages)
	}
	sys := strings.ToLower(p.last.Messages[0].Content)
	for _, needle := range []string{"anti-fabrication guardrail", "treat your own", "unverified", "known corrections"} {
		if !strings.Contains(sys, strings.ToLower(needle)) {
			t.Fatalf("system message missing grounding-guardrail clause %q: %q", needle, p.last.Messages[0].Content)
		}
	}
}

/* fixedVerifier appends a fixed note (ignores input) for MultiVerifier tests. */
type fixedVerifier struct{ note string }

func (f fixedVerifier) Verify(context.Context, string) (string, error) { return f.note, nil }

func TestMultiVerifier_JoinsNotesAndCollapses(t *testing.T) {
	/* Zero -> nil, one -> identity, many -> joined. */
	if agent.NewMultiVerifier() != nil {
		t.Fatalf("no verifiers must yield nil")
	}
	if agent.NewMultiVerifier(nil, nil) != nil {
		t.Fatalf("all-nil must yield nil")
	}
	one := fixedVerifier{note: "\n\n[a]"}
	if got := agent.NewMultiVerifier(one); got != one {
		t.Fatalf("single verifier must be returned as-is")
	}
	multi := agent.NewMultiVerifier(fixedVerifier{note: "\n\n[a]"}, fixedVerifier{note: ""}, fixedVerifier{note: "\n\n[b]"})
	got, err := multi.Verify(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != "\n\n[a]\n\n[b]" {
		t.Fatalf("must join non-empty notes in order: %q", got)
	}
}

/* cveReplyProvider returns a fixed final answer that cites a CVE. */
type cveReplyProvider struct{ reply string }

func (cveReplyProvider) Name() string { return "cvereply" }
func (p cveReplyProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: p.reply}}, nil
}
func (cveReplyProvider) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func TestAgent_RunAppendsVerificationNote(t *testing.T) {
	a := agent.New(cveReplyProvider{reply: "patch CVE-2021-44228 now"}, tools.NewRegistry(), "", 3, nil)
	a.Verifier = &noteVerifier{}
	out, err := a.Run(context.Background(), agent.NewSession(), "any cves?")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "patch CVE-2021-44228 now\n\n[verify] checked" {
		t.Fatalf("Run must append the verification note once: %q", out)
	}
}

func TestAgent_RunNilVerifierUnchanged(t *testing.T) {
	a := agent.New(cveReplyProvider{reply: "CVE-2021-44228"}, tools.NewRegistry(), "", 3, nil)
	out, err := a.Run(context.Background(), agent.NewSession(), "x")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "CVE-2021-44228" {
		t.Fatalf("nil verifier must not alter output: %q", out)
	}
}

func TestAgent_RunHitsMaxIterationsOnToolLoop(t *testing.T) {
	/*
		The provider always returns a tool call; the loop should terminate
		with ErrMaxIterations rather than spinning forever.
	*/
	p := &fakeProvider{
		responses: []llm.ChatResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "missing"}}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "2", Name: "missing"}}}},
		},
	}
	a := agent.New(p, tools.NewRegistry(), "", 2, nil)
	_, err := a.Run(context.Background(), agent.NewSession(), "loop")
	if err == nil {
		t.Fatalf("expected ErrMaxIterations, got nil")
	}
}
