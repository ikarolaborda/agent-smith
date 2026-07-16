package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/refine"
)

/* refineKeepaliveInterval bounds the gap between SSE writes during a long round. */
const refineKeepaliveInterval = 15 * time.Second

/*
Refine round-timeout bounds. A large local model (e.g. a 60B served via Ollama)
can take several minutes per grounded round — the loop's 180s default was shorter
than a measured healthy round and cancelled the in-flight stream. The server uses
a generous default and clamps any client-supplied value to a safe range so a
pathological value cannot wedge the connection.
*/
const (
	defaultRefineRoundTimeout = 480 * time.Second
	minRefineRoundTimeout     = 60 * time.Second
	maxRefineRoundTimeout     = 900 * time.Second
)

/*
refineGenMaxTokens caps refine-generation output so a degenerate/repeating local
model self-terminates (and is then judged NOT_USABLE) instead of looping until the
round timeout. Generous enough for a thorough grounded assessment.
*/
const refineGenMaxTokens = 4096

/* clampRefineTimeout maps a client-supplied seconds value to a safe round timeout (0 = server default). */
func clampRefineTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultRefineRoundTimeout
	}
	d := time.Duration(seconds) * time.Second
	if d < minRefineRoundTimeout {
		return minRefineRoundTimeout
	}
	if d > maxRefineRoundTimeout {
		return maxRefineRoundTimeout
	}
	return d
}

/*
maxRefineIters caps the client-supplied refine_max_iters. Each round is a full
model generation plus a judge call (minutes on a large local model), so an
unbounded value would let a single request run away; the loop's own default
applies when the request asks for 0.
*/
const maxRefineIters = 8

/* clampRefineIters bounds a requested iteration budget to [0, maxRefineIters] (0 = use loop default). */
func clampRefineIters(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxRefineIters {
		return maxRefineIters
	}
	return n
}

/*
buildRefineJudge constructs the strict OpenAI judge from the configured openai
provider, or returns a true nil interface when no judge is available (so the
handler can hard-fail refine mode rather than silently return an unevaluated
answer). The explicit nil return avoids the typed-nil-in-interface trap.
*/
func buildRefineJudge(cfg *config.Config) refine.Judge {
	if cfg == nil {
		return nil
	}
	oa := cfg.Providers["openai"]
	j := refine.NewOpenAIJudge(oa.APIKey, oa.BaseURL, oa.Model)
	if j == nil {
		return nil
	}
	return j
}

/*
streamRefine runs the refinement loop and streams its progress over SSE so the
web user can watch the evaluation. It emits a refine_round event per judged round
(with a periodic keepalive comment so a minutes-long round does not idle the
connection), then a refine_summary carrying an explicit status, then the final
answer as a single assistant delta.

Two invariants matter:
  - refine_summary is emitted BEFORE the final content, so the client always has
    the safety label (status) before it renders the answer.
  - a nil judge is a hard error: refine must never return an unevaluated answer.

Only this goroutine writes to enc; refine.Run runs in a child goroutine and
communicates rounds over a buffered channel, so there is no concurrent writer.
*/
func streamRefine(ctx context.Context, enc *sseEncoder, id, model string, created int64, task string, gen refine.Generator, judge refine.Judge, cfg refine.LoopConfig) {
	if judge == nil {
		_ = enc.writeNamedEvent("error", map[string]any{
			"message": "refine mode requires the OpenAI judge: set OPENAI_API_KEY and a real OPENAI_MODEL (e.g. gpt-5.5)",
		})
		enc.writeDone()
		return
	}

	bufN := cfg.MaxIters
	if bufN <= 0 {
		bufN = refine.DefaultMaxIters
	}
	rounds := make(chan refine.Round, bufN+1)
	cfg.OnRound = func(rd refine.Round) {
		select {
		case rounds <- rd:
		case <-ctx.Done():
		}
	}

	type outcome struct {
		res refine.Result
		err error
	}
	doneCh := make(chan outcome, 1)
	go func() {
		/*
			net/http only recovers panics raised in the handler goroutine, not in
			ones it spawns. Without this a panic anywhere under refine.Run (the
			judge call, the provider stack) would crash the whole server process
			instead of failing this one request.
		*/
		defer func() {
			if p := recover(); p != nil {
				doneCh <- outcome{err: fmt.Errorf("refine: panic: %v", p)}
			}
		}()
		res, err := refine.Run(ctx, task, gen, judge, cfg)
		doneCh <- outcome{res: res, err: err}
	}()

	ticker := time.NewTicker(refineKeepaliveInterval)
	defer ticker.Stop()

	var out outcome
	streaming := true
	for streaming {
		select {
		case rd := <-rounds:
			emitRefineRound(enc, rd)
		case <-ticker.C:
			_ = enc.writeComment("refine in progress")
		case out = <-doneCh:
			streaming = false
		case <-ctx.Done():
			return
		}
	}

	/* Drain rounds buffered before Run returned (OnRound pushes synchronously). */
	draining := true
	for draining {
		select {
		case rd := <-rounds:
			emitRefineRound(enc, rd)
		default:
			draining = false
		}
	}

	if out.err != nil {
		/*
			Surface the failure to the client AND server-side: a refine run that
			dies (e.g. a round timeout or a hung local model) must be diagnosable
			after the fact, not only visible as a one-off SSE error in a browser.
		*/
		slog.Warn("refine loop failed", "error", out.err, "rounds", len(out.res.Rounds))
		_ = enc.writeNamedEvent("error", map[string]any{"message": out.err.Error()})
		enc.writeDone()
		return
	}

	status := "least_fabricated"
	if out.res.Usable {
		status = "usable"
	}
	_ = enc.writeNamedEvent("refine_summary", map[string]any{
		"status": status,
		"usable": out.res.Usable,
		"reason": string(out.res.Reason),
		"rounds": len(out.res.Rounds),
	})

	_ = enc.writeOpenAIDelta(id, model, created, map[string]any{
		"role":    "assistant",
		"content": out.res.FinalAnswer,
	}, "")
	_ = enc.writeOpenAIDelta(id, model, created, map[string]any{}, "stop")
	enc.writeDone()
}

/* emitRefineRound serialises one judged round for the UI timeline. */
func emitRefineRound(enc *sseEncoder, rd refine.Round) {
	status := "not_usable"
	if rd.Verdict.Usable {
		status = "usable"
	}
	_ = enc.writeNamedEvent("refine_round", map[string]any{
		"iter":          rd.Iter,
		"status":        status,
		"usable":        rd.Verdict.Usable,
		"reasons":       rd.Verdict.Reasons,
		"fixes":         rd.Verdict.Fixes,
		"failure_modes": rd.Verdict.FailureModes,
		"duration_ms":   rd.DurationMs,
	})
}
