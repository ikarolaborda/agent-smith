package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/*
reviewSystemPrompt steers a reviewer to critique only the verifiable security
specifics and to stay terse. It is deliberately adversarial: the reviewer's job
is to find fabrication, not to be agreeable.
*/
const reviewSystemPrompt = "You are a blunt, skeptical security reviewer. You are given another AI's vulnerability-research answer. " +
	"In 3-4 sentences, judge whether its SPECIFIC security claims — CVE identifiers, CVSS scores, affected version ranges, " +
	"exploit primitives, and the vulnerability class — look accurate, or whether any appear fabricated, misattributed, or " +
	"unverifiable. Name the single most doubtful claim. Do not restate the answer. Reply in plain text only; do not use any tools."

/* reviewUserPrompt frames the answer under review. */
func reviewUserPrompt(answer string) string {
	return "Vulnerability-research answer to review:\n\n" + answer
}

/* reviewCLIPrompt is the single-string prompt for the headless Claude CLI (no system role). */
func reviewCLIPrompt(answer string) string {
	return reviewSystemPrompt + "\n\n" + reviewUserPrompt(answer)
}

/* lowReviewTemperature keeps reviewer judgements factual rather than creative. */
const lowReviewTemperature = 0.1

/*
ProviderReviewer is a Reviewer backed by any llm.Provider (used for the OpenAI
reviewer, but provider-agnostic and fakeable in tests).
*/
type ProviderReviewer struct {
	name     string
	provider llm.Provider
}

/* NewProviderReviewer builds a reviewer over an llm.Provider; nil provider yields nil. */
func NewProviderReviewer(name string, p llm.Provider) *ProviderReviewer {
	if p == nil {
		return nil
	}
	return &ProviderReviewer{name: name, provider: p}
}

/*
NewOpenAIReviewer builds an OpenAI-backed reviewer from credentials, returning
nil when no API key is configured so the caller can omit it. baseURL and model
fall back to the openai client defaults when empty.
*/
func NewOpenAIReviewer(apiKey, baseURL, model string) *ProviderReviewer {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	client, err := openai.New(openai.Config{APIKey: apiKey, BaseURL: baseURL, Model: model})
	if err != nil {
		return nil
	}
	return &ProviderReviewer{name: "OpenAI", provider: client}
}

/* Name satisfies Reviewer. */
func (r *ProviderReviewer) Name() string { return r.name }

/* Review asks the backing provider to critique the answer at low temperature. */
func (r *ProviderReviewer) Review(ctx context.Context, answer string) (string, error) {
	temp := lowReviewTemperature
	resp, err := r.provider.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: reviewSystemPrompt},
			{Role: llm.RoleUser, Content: reviewUserPrompt(answer)},
		},
		Temperature: &temp,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("validate: nil review response")
	}
	return resp.Message.Content, nil
}

/* DefaultClaudeModel is the Claude alias used for CLI validation (Sonnet balances cost/quality). */
const DefaultClaudeModel = "sonnet"

/* maxCLIOutputBytes caps how much subprocess stdout we read, bounding memory. */
const maxCLIOutputBytes = 1 << 20

/*
cliRunner executes the Claude CLI and returns its stdout. It is injected so tests
can exercise ClaudeCLIReviewer without the real binary.
*/
type cliRunner func(ctx context.Context, bin, model, prompt string) ([]byte, error)

/*
ClaudeCLIReviewer is a Reviewer that drives the operator's Claude Max subscription
through the Claude Code CLI in headless mode (`claude -p ... --output-format json`),
which reuses the OAuth login with no API key. The Anthropic API SDK cannot use a
Max subscription, so the CLI subprocess is the only path.

The subprocess is hardened: direct argv (no shell), a neutral temp working
directory and a sanitised minimal environment (so it does not inherit the host
project's CLAUDE.md / MCP servers / hooks), stdin disabled, output size-capped, and
a recursion marker so any re-entrant tooling can detect and refuse to re-validate.
*/
type ClaudeCLIReviewer struct {
	name  string
	bin   string
	model string
	run   cliRunner
}

/*
NewClaudeCLIReviewer returns a reviewer if the claude binary is resolvable on
PATH, else nil so the caller can omit it cleanly. model defaults to Sonnet.
*/
func NewClaudeCLIReviewer(model string) *ClaudeCLIReviewer {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil
	}
	if model == "" {
		model = DefaultClaudeModel
	}
	return &ClaudeCLIReviewer{name: "Claude (Max)", bin: bin, model: model, run: runClaudeCLI}
}

/* Name satisfies Reviewer. */
func (r *ClaudeCLIReviewer) Name() string { return r.name }

/* Review invokes the CLI and parses the JSON .result field. */
func (r *ClaudeCLIReviewer) Review(ctx context.Context, answer string) (string, error) {
	out, err := r.run(ctx, r.bin, r.model, reviewCLIPrompt(answer))
	if err != nil {
		return "", err
	}
	var parsed struct {
		Result string `json:"result"`
	}
	if jErr := json.Unmarshal(out, &parsed); jErr == nil && strings.TrimSpace(parsed.Result) != "" {
		return parsed.Result, nil
	}
	/* If the output format ever changes, fall back to the raw text rather than erroring. */
	if s := strings.TrimSpace(string(out)); s != "" {
		return s, nil
	}
	return "", errors.New("validate: empty claude CLI output")
}

/*
runClaudeCLI is the production cliRunner. It runs `claude -p <prompt>
--output-format json --model <model>` with a hardened process environment.
*/
func runClaudeCLI(ctx context.Context, bin, model, prompt string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, "-p", prompt, "--output-format", "json", "--model", model)
	cmd.Dir = os.TempDir()
	cmd.Env = sanitizedEnv()
	cmd.Stdin = nil

	var stdout bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, remaining: maxCLIOutputBytes}
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

/*
sanitizedEnv returns the minimal environment the Claude CLI needs to find its
OAuth session, plus a recursion marker. It deliberately omits the bulk of the
parent environment so the subprocess cannot inherit project-scoped tooling.
*/
func sanitizedEnv() []string {
	keep := []string{"HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "TERM", "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}
	env := make([]string, 0, len(keep)+1)
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	/* A re-entrant code path can check this to refuse nested validation. */
	env = append(env, "AGENT_SMITH_VALIDATION=1")
	return env
}

/* limitedWriter caps the number of bytes written, silently discarding the rest. */
type limitedWriter struct {
	w         *bytes.Buffer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil
	}
	if len(p) > l.remaining {
		_, _ = l.w.Write(p[:l.remaining])
		l.remaining = 0
		return len(p), nil
	}
	l.remaining -= len(p)
	return l.w.Write(p)
}
