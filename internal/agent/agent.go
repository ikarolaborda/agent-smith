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

	"github.com/ikarolaborda/agent-smith/internal/compact"
	"github.com/ikarolaborda/agent-smith/internal/convomem"
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
		Agentic switches retrieval from one-shot Augment to an agentic-RAG loop:
		the model plans and runs its own retrieval through the rag_search (and any
		graph) tools, self-evaluates sufficiency, and answers with citations. When
		true, composeMessages skips Augment (to avoid double-injecting context) and
		attaches the agentic directive instead. Requires the rag_search tool to be
		registered and a tool-capable provider; defaults false so the classic,
		offline-friendly path is unchanged.
	*/
	Agentic bool
	/*
		Verifier, when non-nil, post-processes the final tool-free answer and
		returns a non-destructive advisory note (e.g. a CVE check against the NVD
		primary source) that is appended to the response. A nil Verifier disables
		the gate entirely, so existing callers see no behavior change.
	*/
	Verifier Verifier
	/*
		MaxTokens caps the model's output tokens per generation (mapped to the
		provider's max_tokens / num_predict). Zero means unlimited — the historical
		default, so ordinary chat is unchanged. It is set on the refine path to bound
		a degenerate/repeating local model so a runaway self-terminates and is judged
		rather than burning the whole round timeout.
	*/
	MaxTokens int
	/*
		Compactor, when non-nil, rewrites a user message that would overflow the
		model's context window into head/tail + a summary, and exposes the original
		text to the loop through a per-turn context_search tool. Nil disables it, so
		callers that never send oversized input are unaffected.
	*/
	Compactor *compact.Compactor
	/*
		ConvoMemory, when non-nil and ProfileID is set, persists each substantive
		turn and recalls the most relevant past turns (across sessions) into the
		prompt, so a conversation's history survives beyond the context window.
		Recall is computed once per turn into convoBlock; nil disables it.
	*/
	ConvoMemory *convomem.Memory
	/* convoBlock is the per-turn recalled-memory section, computed once in Run/RunStream. */
	convoBlock string
	/*
		Workspace, when non-empty, is the opened folder the file_read/read_dir tools
		are rooted at. It drives the workspace-awareness directive so the model reads
		the folder instead of claiming it cannot see local files. Set per-request by
		the server from the active workspace.
	*/
	Workspace string
	/*
		WorkspaceListing, when non-empty, is a compact file tree of the open
		workspace injected into the prompt as ground truth, so the model always knows
		which files exist without first having to call a tool — a hard guarantee
		against disclaiming access or inventing files. Set per-request by the server.
	*/
	WorkspaceListing string
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
	a.recallConvo(ctx, userInput)

	var idxs []*compact.Index
	reg := a.Tools
	usedRetrieval := false
	for i := 0; i < a.MaxIters; i++ {
		reg, idxs = a.compactSession(ctx, session, idxs)
		req := llm.ChatRequest{
			Messages: a.composeMessages(ctx, session),
			Model:    a.Model,
		}
		if reg != nil {
			req.Tools = reg.Definitions()
		}
		if a.MaxTokens > 0 {
			limit := a.MaxTokens
			req.MaxTokens = &limit
		}

		resp, err := a.Provider.Chat(ctx, req)
		if err != nil {
			return "", fmt.Errorf("agent: provider chat: %w", err)
		}
		session.Append(resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			answer := resp.Message.Content
			/*
				No-tool-call fallback: agentic mode skipped the one-shot Augment, so
				a model that answered without ever retrieving is ungrounded. Run a
				single classic grounded pass and return that instead. It never
				recurses — the classic pass has Agentic off, so it cannot re-trigger.
			*/
			if a.Agentic && a.RAG != nil && !usedRetrieval {
				if grounded, ok := a.classicFallback(ctx, session); ok {
					a.rememberTurn(ctx, userInput, grounded)
					return grounded + a.verifyNote(ctx, grounded), nil
				}
			}
			a.rememberTurn(ctx, userInput, answer)
			return answer + a.verifyNote(ctx, answer), nil
		}

		for _, call := range resp.Message.ToolCalls {
			if isRetrievalTool(call.Name) {
				usedRetrieval = true
			}
			result, toolErr := a.dispatch(ctx, reg, call)
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
isRetrievalTool reports whether a dispatched tool actually queried the knowledge
base, so the no-tool-call fallback fires only when the model ignored retrieval
entirely — not merely because it used some other tool.
*/
func isRetrievalTool(name string) bool {
	return name == "rag_search" || name == "graph_expand"
}

/*
classicFallback answers with one classic grounded pass when an agentic turn
produced a tool-free answer without retrieving. It composes over the conversation
MINUS the just-appended ungrounded answer (so that answer cannot bias the grounded
one), with Agentic forced off so it cannot recurse, and does a single Chat. It
returns ok=false on a provider error or empty content, leaving the original
answer in place. It does not mutate the session, so no conversation entry is
duplicated.
*/
func (a *Agent) classicFallback(ctx context.Context, session *Session) (string, bool) {
	prev := a.Agentic
	a.Agentic = false
	defer func() { a.Agentic = prev }()

	msgs := session.Messages()
	if len(msgs) == 0 {
		return "", false
	}
	req := llm.ChatRequest{Messages: a.composeFrom(ctx, msgs[:len(msgs)-1]), Model: a.Model}
	if a.MaxTokens > 0 {
		limit := a.MaxTokens
		req.MaxTokens = &limit
	}
	resp, err := a.Provider.Chat(ctx, req)
	if err != nil || strings.TrimSpace(resp.Message.Content) == "" {
		return "", false
	}
	return resp.Message.Content, true
}

const (
	convoRecallK          = 3
	convoRecallMinScore   = 0.35
	convoMinRememberChars = 40
)

/*
recallConvo computes this turn's recalled-memory section once; composeFrom injects
it on every iteration without re-querying. Best-effort and profile-scoped: no
ConvoMemory, no ProfileID, or an empty query yields no recall.
*/
func (a *Agent) recallConvo(ctx context.Context, query string) {
	if a.ConvoMemory == nil || a.ProfileID == "" {
		return
	}
	a.convoBlock = a.ConvoMemory.RecallBlock(ctx, a.ProfileID, query, convoRecallK, convoRecallMinScore)
}

/*
rememberTurn persists the user message and the assistant answer for this profile
so a later conversation can recall them. Trivial messages are skipped to keep the
store signal-dense; errors are logged rather than failing the turn.
*/
func (a *Agent) rememberTurn(ctx context.Context, userMsg, answer string) {
	if a.ConvoMemory == nil || a.ProfileID == "" {
		return
	}
	if len(strings.TrimSpace(userMsg)) >= convoMinRememberChars {
		if err := a.ConvoMemory.Remember(ctx, a.ProfileID, "user", userMsg); err != nil {
			a.Logger.Warn("agent: convo memory remember (user) failed", "err", err)
		}
	}
	if len(strings.TrimSpace(answer)) >= convoMinRememberChars {
		if err := a.ConvoMemory.Remember(ctx, a.ProfileID, "assistant", answer); err != nil {
			a.Logger.Warn("agent: convo memory remember (assistant) failed", "err", err)
		}
	}
}

/*
compactSession rewrites every oversized user message in the session — the newly
appended one AND any large paste replayed from an earlier turn — into head/tail +
summary, in place, and returns a per-turn tool registry exposing a context_search
index over all of their original text. Compacting the whole session (not just the
latest message) is what keeps a multi-turn conversation whose big input arrived in
an earlier turn from overflowing the context window. With no Compactor, or nothing
oversized, it returns the shared registry unchanged. Compaction errors are
non-fatal: the original message is kept and the provider's own context error (if
any) still surfaces.
*/
func (a *Agent) compactSession(ctx context.Context, session *Session, prior []*compact.Index) (*tools.Registry, []*compact.Index) {
	if a.Compactor == nil {
		return a.Tools, prior
	}
	msgs := session.Messages()
	indexes := prior
	changed := false
	apply := func(i int, res *compact.Result) {
		msgs[i].Content = res.Compacted
		changed = true
		if res.Index != nil {
			indexes = append(indexes, res.Index)
		}
	}

	/*
		Pass 1 — proactively compact any single oversized USER message (a large paste)
		or TOOL result (e.g. a read_dir that loaded a whole folder). Tool results are
		appended mid-loop, so this runs every iteration; an already-compacted message
		is under the trigger and skipped, and the summary cache makes re-scanning cheap.
	*/
	for i := range msgs {
		if msgs[i].Content == "" || !compactableRole(msgs[i].Role) {
			continue
		}
		res, err := a.Compactor.Compact(ctx, msgs[i].Content, "")
		if err != nil {
			a.Logger.Warn("agent: input compaction failed; keeping original", "role", msgs[i].Role, "err", err)
			continue
		}
		if res != nil {
			apply(i, res)
		}
	}

	/*
		Pass 2 — total budget. Several messages each under the per-message trigger can
		still sum past the context window (e.g. a few read_dir calls plus history).
		Compact the largest compactable message repeatedly until the whole prompt fits
		or nothing compactable remains.
	*/
	if budget := a.Compactor.MaxTotalTokens; budget > 0 {
		tried := make([]bool, len(msgs))
		for totalMessageTokens(msgs) > budget {
			bi, bt := -1, 0
			for i := range msgs {
				if tried[i] || !compactableRole(msgs[i].Role) {
					continue
				}
				if tk := compact.EstimateTokens(msgs[i].Content); tk > bt {
					bi, bt = i, tk
				}
			}
			if bi < 0 {
				break // nothing left to compact; the provider's own limit will apply
			}
			tried[bi] = true
			res, err := a.Compactor.CompactForce(ctx, msgs[bi].Content, "")
			if err != nil {
				a.Logger.Warn("agent: budget compaction failed; keeping original", "role", msgs[bi].Role, "err", err)
				continue
			}
			if res != nil {
				apply(bi, res)
			}
		}
	}

	if changed {
		session.Reset()
		for _, m := range msgs {
			session.Append(m)
		}
	}
	reg := a.Tools
	if merged := compact.MergeIndexes(indexes...); merged != nil && merged.Len() > 0 {
		reg = a.Tools.With(compact.NewContextSearchTool(merged))
	}
	return reg, indexes
}

/* compactableRole reports whether a message role may be rewritten by compaction. */
func compactableRole(r llm.Role) bool {
	return r == llm.RoleUser || r == llm.RoleTool
}

/* totalMessageTokens estimates the combined size of all messages (system prompt excluded). */
func totalMessageTokens(msgs []llm.Message) int {
	t := 0
	for i := range msgs {
		t += compact.EstimateTokens(msgs[i].Content)
	}
	return t
}

/*
composeMessages prepends the system message to the session history. The system
content joins the configured system prompt, the always-on coding-paradigm
directive (prefer OOP; see pkg/prompt), and — when RAG is configured and the
latest user message yields a retrieval hit — a "## Relevant documentation"
block. The directive makes the system content non-empty on every request.
*/
func (a *Agent) composeMessages(ctx context.Context, session *Session) []llm.Message {
	return a.composeFrom(ctx, session.Messages())
}

/*
composeFrom builds the provider messages from an explicit conversation slice.
The no-tool-call fallback uses it to re-compose a classic grounded pass over the
turns preceding an ungrounded answer, without that answer polluting context.
*/
func (a *Agent) composeFrom(ctx context.Context, msgs []llm.Message) []llm.Message {
	var aug string
	agenticSection := ""
	if a.Agentic {
		/*
			Agentic mode drives retrieval through the rag_search/graph tools inside
			the loop, so a pre-injected Augment block would only duplicate context
			and pull the two strategies against each other. Attach the directive and
			leave retrieval to the model.
		*/
		agenticSection = prompt.AgenticRAGDirective
	} else if a.RAG != nil {
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
	system := prompt.JoinSections(a.SystemPrompt, prompt.PersonaDirective, prompt.CodingParadigmDirective, prompt.EngineeringDirective, prompt.GroundingGuardrailDirective, prompt.WorkspaceDirective(a.Workspace), a.WorkspaceListing, agenticSection, aug, a.convoBlock)
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
func (a *Agent) dispatch(ctx context.Context, reg *tools.Registry, call llm.ToolCall) (string, error) {
	if reg == nil {
		return "", errors.New("agent: no tool registry configured")
	}
	t, err := reg.Get(call.Name)
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
