/*
Package agent implements the high-level reasoning loop that drives a
llm.Provider with a tools.Registry. The loop is deliberately small:

	user message -> provider.Chat -> if tool_calls { execute, append, repeat }
	                              -> else return assistant content
*/
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/tools"
	"github.com/ikarolaborda/agent-smith/pkg/prompt"
)

/*
Agent wires a single chat-completion provider to a tool registry and
orchestrates the plan -> call tool -> observe loop. Zero-value fields are
considered invalid; construct an Agent through New.
*/
type Agent struct {
	Provider     llm.Provider
	Tools        *tools.Registry
	SystemPrompt string
	MaxIters     int
	Logger       *slog.Logger
	/*
		RAG, when non-nil, augments the system prompt with retrieval results
		for the latest user message before each Provider call.
	*/
	RAG *rag.Service
	/*
		ProfileID, when non-empty, scopes memory retrieval to one user/
		profile. Set per-request by the HTTP server from the chat payload.
	*/
	ProfileID string
	/*
		Model, when non-empty, overrides the model that the configured
		Provider would otherwise use. Set per-request by the HTTP server
		from the chat payload (the segment after "provider/" in the model
		ID). An empty value preserves the provider's baked-in model.
	*/
	Model string
	/*
		WebSearch enables the web-grounding section in Augment for this
		request. The server sets a provider-aware default (true for
		Ollama, false for cloud providers) which the request body can
		override via web_search.
	*/
	WebSearch bool
	/*
		Verifier, when non-nil, post-processes the final tool-free answer and
		returns a non-destructive advisory note (e.g. a CVE check against the NVD
		primary source) that is appended to the response. A nil Verifier disables
		the gate entirely, so existing callers see no behavior change.
	*/
	Verifier Verifier
}

/*
Verifier checks claims in a finished answer against a primary source and returns
an advisory note to append (empty when there is nothing to flag). It is the
system-enforced half of the anti-fabrication guardrail: independent of whatever
the model asserts. Implementations must be non-destructive (annotate, never
rewrite) and fail-soft (a lookup failure yields an empty or soft note, not an
error that blocks the answer).
*/
type Verifier interface {
	Verify(ctx context.Context, text string) (note string, err error)
}

/*
MultiVerifier runs several Verifiers in order and concatenates their non-empty
notes. It is the seam that composes independent advisory layers — e.g. the NVD
primary-source CVE check and the cross-provider second-opinion validation — into
the single Agent.Verifier slot. A failing verifier is skipped, never fatal.
*/
type MultiVerifier struct {
	verifiers []Verifier
}

/*
NewMultiVerifier composes the given verifiers, dropping nils. It returns nil when
none remain and the sole verifier when exactly one remains, so callers never pay
for an empty or single-element wrapper.
*/
func NewMultiVerifier(verifiers ...Verifier) Verifier {
	live := make([]Verifier, 0, len(verifiers))
	for _, v := range verifiers {
		if v != nil {
			live = append(live, v)
		}
	}
	switch len(live) {
	case 0:
		return nil
	case 1:
		return live[0]
	default:
		return &MultiVerifier{verifiers: live}
	}
}

/* Verify runs each composed verifier and joins their non-empty advisory notes. */
func (m *MultiVerifier) Verify(ctx context.Context, text string) (string, error) {
	var b strings.Builder
	for _, v := range m.verifiers {
		note, err := v.Verify(ctx, text)
		if err != nil || note == "" {
			continue
		}
		b.WriteString(note)
	}
	return b.String(), nil
}

/* ErrMaxIterations is returned when the agent loop exceeds MaxIters. */
var ErrMaxIterations = errors.New("agent: max iterations reached")

/*
New constructs an Agent. The system prompt may be empty; MaxIters must be
positive (defaults to 10 if non-positive). The logger may be nil, in which
case slog.Default() is used.
*/
func New(p llm.Provider, reg *tools.Registry, systemPrompt string, maxIters int, logger *slog.Logger) *Agent {
	if maxIters <= 0 {
		maxIters = 10
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		Provider:     p,
		Tools:        reg,
		SystemPrompt: systemPrompt,
		MaxIters:     maxIters,
		Logger:       logger,
	}
}

/*
Run drives the agent loop for a single user input. It appends the user
message to the session, then repeatedly:

  - calls Provider.Chat with the current session
  - if the response has no tool calls, returns its content
  - otherwise, dispatches each tool call through the registry, appends the
    tool result messages, and continues

Run terminates with ErrMaxIterations if MaxIters is exceeded without a
tool-free assistant response. The session is mutated in place so callers can
inspect or persist the full trace.

TODO: implement once provider stubs return real ChatResponses.
*/
func (a *Agent) Run(ctx context.Context, session *Session, userInput string) (string, error) {
	if a == nil || a.Provider == nil {
		return "", errors.New("agent: not configured")
	}
	if session == nil {
		return "", errors.New("agent: nil session")
	}

	session.Append(llm.Message{Role: llm.RoleUser, Content: userInput})

	for i := 0; i < a.MaxIters; i++ {
		req := llm.ChatRequest{
			Messages: a.composeMessages(ctx, session),
			Model:    a.Model,
		}
		if a.Tools != nil {
			req.Tools = a.Tools.Definitions()
		}

		resp, err := a.Provider.Chat(ctx, req)
		if err != nil {
			return "", fmt.Errorf("agent: provider chat: %w", err)
		}
		session.Append(resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			return resp.Message.Content + a.verifyNote(ctx, resp.Message.Content), nil
		}

		for _, call := range resp.Message.ToolCalls {
			result, toolErr := a.dispatch(ctx, call)
			session.Append(llm.Message{
				Role:       llm.RoleTool,
				Name:       call.Name,
				ToolCallID: call.ID,
				Content:    formatToolResult(result, toolErr),
			})
		}
	}
	return "", ErrMaxIterations
}

/*
composeMessages prepends the system message to the session history. The system
content joins the configured system prompt, the always-on coding-paradigm
directive (prefer OOP; see pkg/prompt), and — when RAG is configured and the
latest user message yields a retrieval hit — a "## Relevant documentation"
block. The directive makes the system content non-empty on every request.
*/
func (a *Agent) composeMessages(ctx context.Context, session *Session) []llm.Message {
	msgs := session.Messages()
	var aug string
	if a.RAG != nil {
		if q := latestUserMessage(msgs); q != "" {
			aug = a.RAG.Augment(ctx, q, a.ProfileID, a.WebSearch)
		}
	}
	/*
		The coding-paradigm, engineering, and grounding-guardrail directives are
		appended on every request so the house standard (PHP clean architecture,
		idiomatic Go, mandatory Context7 for third-party code), the authorized-
		defensive security posture, and the anti-fabrication guardrail (assert
		CVE/CVSS/version specifics only from retrieved context; honor known
		corrections) reach all models and providers — including the clustered Qwen
		and the remote abliteration model — regardless of the configured system
		prompt.
	*/
	system := prompt.JoinSections(a.SystemPrompt, prompt.PersonaDirective, prompt.CodingParadigmDirective, prompt.EngineeringDirective, prompt.GroundingGuardrailDirective, aug)
	if system == "" {
		return msgs
	}
	out := make([]llm.Message, 0, len(msgs)+1)
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: system})
	out = append(out, msgs...)
	return out
}

/* latestUserMessage returns the content of the most recent user-authored message. */
func latestUserMessage(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	return ""
}

/*
verifyNote runs the configured Verifier over the final answer and returns the
advisory note to append. It is fail-soft: a nil Verifier, empty content, or a
verifier error all yield an empty string so the answer is never blocked. The
verifier is given the RAW model answer only (callers append the note to the
returned value), so it never re-extracts claims from its own advisory.
*/
func (a *Agent) verifyNote(ctx context.Context, content string) string {
	if a.Verifier == nil || content == "" {
		return ""
	}
	note, err := a.Verifier.Verify(ctx, content)
	if err != nil {
		a.Logger.Warn("agent: answer verification failed", "err", err)
		return ""
	}
	return note
}

/*
dispatch resolves a tool by name and executes it. Unknown tools surface as
errors that the loop converts into tool messages so the model can recover.
*/
func (a *Agent) dispatch(ctx context.Context, call llm.ToolCall) (string, error) {
	if a.Tools == nil {
		return "", errors.New("agent: no tool registry configured")
	}
	t, err := a.Tools.Get(call.Name)
	if err != nil {
		return "", err
	}
	return t.Execute(ctx, call.Arguments)
}

/*
formatToolResult renders a tool's (string, error) return as the textual body
of a tool message. Errors are surfaced as plain prefixed strings so the model
can react to them.
*/
func formatToolResult(result string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return result
}
