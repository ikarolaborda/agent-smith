package refine

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

/* Defaults keep the loop bounded in iterations, latency, and oscillation. */
const (
	DefaultMaxIters      = 3
	DefaultRoundTimeout  = 180 * time.Second
	DefaultMaxNoProgress = 2
)

/* LoopConfig bounds the refinement loop. Zero values fall back to the defaults. */
type LoopConfig struct {
	MaxIters      int
	RoundTimeout  time.Duration
	MaxNoProgress int
	/*
		OnRound, when set, is called synchronously immediately after each round is
		judged and recorded, before the loop decides whether to continue. It exists
		so a caller (e.g. the SSE handler) can stream per-round progress as it
		happens. It must observe the round and return quickly; it must not mutate
		loop state.
	*/
	OnRound func(Round)
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.MaxIters <= 0 {
		c.MaxIters = DefaultMaxIters
	}
	if c.RoundTimeout <= 0 {
		c.RoundTimeout = DefaultRoundTimeout
	}
	if c.MaxNoProgress <= 0 {
		c.MaxNoProgress = DefaultMaxNoProgress
	}
	return c
}

/*
Generator produces a candidate answer for the original task. brief is empty on
the first round and otherwise carries a compact refinement brief synthesised from
the latest verdict. The generator is expected to re-run normal retrieval/grounding
each call rather than editing the previous answer in place, so grounding comes from
fresh evidence every round.
*/
type Generator func(ctx context.Context, task, brief string) (string, error)

/* Round records one generate→judge cycle for the audit ledger. */
type Round struct {
	Iter       int
	Answer     string
	Verdict    Verdict
	DurationMs int64
}

/* StopReason explains why the loop ended. */
type StopReason string

const (
	StopUsable     StopReason = "usable"
	StopMaxIters   StopReason = "max_iters_without_usable"
	StopNoProgress StopReason = "no_progress"
)

/*
Result is the loop outcome. FinalAnswer is the usable answer when Usable is true,
otherwise the least-fabricated attempt. The loop never reports Usable=true unless
the judge actually passed an answer.
*/
type Result struct {
	Usable      bool
	FinalAnswer string
	Reason      StopReason
	Rounds      []Round
}

/*
Run drives the bounded refine loop. Each round generates a candidate, judges it
fail-closed (a judge transport error becomes a NOT_USABLE round, never an abort
into a false pass), records the round, and either returns on a usable verdict or
synthesises a refinement brief for the next round. It stops early when the same
failure modes recur (no progress) and, on exhaustion, returns the least-fabricated
attempt with an honest non-usable reason.
*/
func Run(ctx context.Context, task string, gen Generator, judge Judge, cfg LoopConfig) (Result, error) {
	if gen == nil || judge == nil {
		return Result{}, errors.New("refine: generator and judge are required")
	}
	cfg = cfg.withDefaults()

	res := Result{Reason: StopMaxIters}
	brief := ""
	var prevSignature string
	noProgress := 0
	bestSet := false
	bestModes := 0

	for i := 1; i <= cfg.MaxIters; i++ {
		rctx, cancel := context.WithTimeout(ctx, cfg.RoundTimeout)
		start := time.Now()
		answer, genErr := gen(rctx, task, brief)
		if genErr != nil {
			cancel()
			/* A generation failure ends the loop; surface it only if we have nothing yet. */
			if !bestSet {
				return res, genErr
			}
			return res, nil
		}

		verdict, jErr := judge.Judge(rctx, task, answer)
		cancel()
		if jErr != nil {
			/* Fail closed: an unjudgeable round is NOT_USABLE, never a pass. */
			verdict = Verdict{
				Usable:       false,
				Reasons:      "judge unavailable: " + jErr.Error(),
				FailureModes: []string{"judge-error"},
			}
		}

		round := Round{
			Iter:       i,
			Answer:     answer,
			Verdict:    verdict,
			DurationMs: time.Since(start).Milliseconds(),
		}
		res.Rounds = append(res.Rounds, round)
		if cfg.OnRound != nil {
			cfg.OnRound(round)
		}

		if verdict.Usable {
			res.Usable = true
			res.FinalAnswer = answer
			res.Reason = StopUsable
			return res, nil
		}

		/* Track the least-fabricated attempt so exhaustion returns the most honest one. */
		if !bestSet || len(verdict.FailureModes) < bestModes {
			res.FinalAnswer = answer
			bestModes = len(verdict.FailureModes)
			bestSet = true
		}

		sig := signature(verdict.FailureModes)
		if sig != "" && sig == prevSignature {
			noProgress++
			if noProgress >= cfg.MaxNoProgress {
				res.Reason = StopNoProgress
				return res, nil
			}
		} else {
			noProgress = 0
		}
		prevSignature = sig

		brief = buildBrief(verdict)
	}

	return res, nil
}

/*
signature canonicalises a failure-mode SET so repeated stagnation is detectable
regardless of ordering, casing, whitespace, or duplicate tags. It is a
conservative stagnation signal, not a proof of semantic equivalence.
*/
func signature(modes []string) string {
	if len(modes) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(modes))
	norm := make([]string, 0, len(modes))
	for _, m := range modes {
		t := strings.ToLower(strings.TrimSpace(m))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		norm = append(norm, t)
	}
	sort.Strings(norm)
	return strings.Join(norm, "|")
}

/*
buildBrief turns a NOT_USABLE verdict into a compact instruction for the next
round. It is bounded (top reasons + at most two fixes) and is constrained to ask
for better grounding, scoping, and labelling ONLY — it must never instruct the
model to find a vulnerability or to sound more confident.
*/
func buildBrief(v Verdict) string {
	var b strings.Builder
	b.WriteString("Revise the SAME analysis to be more grounded, honestly scoped, and correctly labelled. ")
	b.WriteString("Do NOT invent findings, add confidence, or claim a vulnerability the evidence does not support; ")
	b.WriteString("an honest negative is an acceptable answer. ")
	if v.Reasons != "" {
		b.WriteString("Reviewer concerns: ")
		b.WriteString(v.Reasons)
		b.WriteString(". ")
	}
	if len(v.Fixes) > 0 {
		top := v.Fixes
		if len(top) > 2 {
			top = top[:2]
		}
		b.WriteString("Address: ")
		b.WriteString(strings.Join(top, "; "))
		b.WriteString(".")
	}
	return strings.TrimSpace(b.String())
}
