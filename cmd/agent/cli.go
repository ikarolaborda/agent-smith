package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/refine"
)

/*
logExecBanner emits the high-visibility startup banner ADR 0003 requires when
contained execution is enabled, and warns about the misconfigurations that make
the gate a no-op (no workspace, or Docker absent).
*/
func logExecBanner(f flags, logger *slog.Logger) {
	if !f.allowExec {
		return
	}
	if f.workspace == "" {
		logger.Warn("exec: --allow-exec set but no --workspace; the contained run tool is NOT registered")
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		logger.Warn("exec: --allow-exec set but docker not found on PATH; contained runs will fail", "err", err)
	}
	logger.Warn("exec: CONTAINED EXECUTION ENABLED — agent may run fuzz/reproduce/triage in an ephemeral, network-isolated, read-only Docker container", "workspace", f.workspace)
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

/* runOnce answers a single prompt and writes the result to stdout. */
func runOnce(ctx context.Context, a *agent.Agent, prompt string) error {
	out, err := a.Run(ctx, agent.NewSession(), prompt)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

/*
runRefine drives the opt-in refinement loop: it grounds the agent (RAG/web/
Context7, same posture as the server), builds the strict OpenAI judge from the
configured openai provider, and regenerates the answer with the judge's critique
until the judge passes it as USABLE or the iteration budget is exhausted. The
agent's advisory verifier is disabled on this path because the judge IS the
validation, and feeding the appended advisory back into the next round would
pollute both the answer and the grounding. The loop never fabricates a pass; on
exhaustion it prints the least-fabricated attempt and an honest non-usable note.
*/
func runRefine(ctx context.Context, cfg *config.Config, f flags, a *agent.Agent, logger *slog.Logger) error {
	oa := cfg.Providers["openai"]
	judge := refine.NewOpenAIJudge(oa.APIKey, oa.BaseURL, oa.Model)
	if judge == nil {
		return errors.New("--refine-loop requires the OpenAI judge: set OPENAI_API_KEY and a real OPENAI_MODEL (e.g. gpt-5.5)")
	}

	/* Ground each round like the server, and take the raw answer (no appended advisory). */
	a.WebSearch = !f.disableWeb
	a.Verifier = nil

	gen := func(gctx context.Context, task, brief string) (string, error) {
		prompt := task
		if brief != "" {
			prompt = task + "\n\n[Refinement brief — improve grounding, scoping, and labelling ONLY; do NOT fabricate]\n" + brief
		}
		return a.Run(gctx, agent.NewSession(), prompt)
	}

	logger.Info("refine loop: enabled", "judge", judge.Name(), "max_iters", f.refineIters, "round_timeout", f.refineTO.String())
	res, err := refine.Run(ctx, f.prompt, gen, judge, refine.LoopConfig{MaxIters: f.refineIters, RoundTimeout: f.refineTO})
	if err != nil {
		return err
	}

	printRefineResult(os.Stdout, res)
	return nil
}

/* printRefineResult writes the final answer followed by the per-round audit ledger. */
func printRefineResult(w io.Writer, res refine.Result) {
	fmt.Fprintln(w, res.FinalAnswer)
	fmt.Fprintf(w, "\n--- refinement ledger (%d round(s), outcome: %s) ---\n", len(res.Rounds), res.Reason)
	if !res.Usable {
		fmt.Fprintln(w, "NOTE: the loop did NOT reach a usable answer; the least-fabricated attempt is shown above. Not a confirmed result.")
	}
	for _, r := range res.Rounds {
		status := "NOT_USABLE"
		if r.Verdict.Usable {
			status = "USABLE"
		}
		fmt.Fprintf(w, "round %d [%s, %dms]: %s\n", r.Iter, status, r.DurationMs, r.Verdict.Reasons)
		if len(r.Verdict.FailureModes) > 0 {
			fmt.Fprintf(w, "  failure modes: %s\n", strings.Join(r.Verdict.FailureModes, ", "))
		}
	}
}

/*
runInteractive reads one prompt per line from r and writes the assistant
response to w. The session is reused so the conversation builds up across
turns.
*/
func runInteractive(ctx context.Context, a *agent.Agent, r io.Reader, w io.Writer) error {
	session := agent.NewSession()
	scanner := bufio.NewScanner(r)
	for {
		fmt.Fprint(w, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		reply, err := a.Run(ctx, session, line)
		if err != nil {
			fmt.Fprintln(w, "error:", err)
			continue
		}
		fmt.Fprintln(w, reply)
	}
	return scanner.Err()
}
