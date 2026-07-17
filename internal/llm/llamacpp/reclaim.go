/*
reclaim.go implements the first two stages of the memory-readiness pipeline
(docs/plans/memory-readiness.md):

  Stage 0 — instrumentation. Structured admission/teardown telemetry so an
  operator can see the pre-load memory picture and what a load freed. Everything
  downstream depends on this visibility.

  Stage 1 — self-reclamation. The single-model-at-a-time invariant: before a new
  model is admitted, release the memory the agent already owns (a superseded
  llama-server and its resident set) and BLOCK until the OS has reaped it, so the
  admission gate measures a host that no longer holds the old model. This never
  touches a process the agent does not own — it is the deliberate opposite of an
  OOM-killer.
*/
package llamacpp

import (
	"context"
	"fmt"
	"log/slog"
)

/*
ReclaimOutcome reports what a pre-admission reclamation released. FreedEstimateBytes
is best-effort: it is the runtime estimate the superseded model was admitted
against, which is the amount the fit gate can expect to see returned to the host
once the process is reaped. It is telemetry and a future Stage-2 self-accounting
input, never a promise of exact bytes.
*/
type ReclaimOutcome struct {
	ClosedRuntime      bool   `json:"closed_runtime"`
	FreedEstimateBytes uint64 `json:"freed_estimate_bytes"`
	Note               string `json:"note"`
}

/*
ReclaimFunc is the pre-admission hook run at the top of Runtime.Start, before the
host is measured. It is where a caller releases agent-owned memory — a prior
llama-server via Reclaim, plus any in-memory caches (RAG index, warm buffers) —
so the subsequent fit decision reflects the freed state. A nil hook is a no-op,
which is the current single-launch path: there is nothing prior to reclaim.
*/
type ReclaimFunc func(context.Context) (ReclaimOutcome, error)

/*
Reclaim enforces the single-model-at-a-time invariant for one superseded runtime.
It Closes the process and, because Close blocks until the reaper observes exit,
returns only once the resident set has actually been released. The freed estimate
is the prior runtime's admitted runtime size. A nil prior is a no-op so callers
can wire the hook unconditionally.
*/
func Reclaim(ctx context.Context, prior *Runtime) (ReclaimOutcome, error) {
	if prior == nil {
		return ReclaimOutcome{Note: "no prior runtime to reclaim"}, nil
	}
	freed := prior.AdmittedRuntimeBytes()
	if err := prior.Close(ctx); err != nil {
		return ReclaimOutcome{}, fmt.Errorf("llamacpp: reclaim superseded runtime: %w", err)
	}
	return ReclaimOutcome{
		ClosedRuntime:      true,
		FreedEstimateBytes: freed,
		Note:               "superseded llama-server stopped and reaped before admission",
	}, nil
}

/*
runReclaimHook executes the configured pre-admission hook and logs its outcome. A
missing hook is silent; a hook error is returned so Start fails closed rather than
admitting a model against a host it never actually reclaimed.
*/
func (r *Runtime) runReclaimHook(ctx context.Context) error {
	if r.cfg.ReclaimBeforeStart == nil {
		return nil
	}
	outcome, err := r.cfg.ReclaimBeforeStart(ctx)
	if err != nil {
		return fmt.Errorf("llamacpp: pre-admission self-reclamation failed: %w", err)
	}
	if outcome.ClosedRuntime || outcome.FreedEstimateBytes > 0 {
		r.logger.Info("llamacpp: self-reclamation",
			"closed_runtime", outcome.ClosedRuntime,
			"freed_estimate_bytes", outcome.FreedEstimateBytes,
			"note", outcome.Note)
	}
	return nil
}

/*
admissionLogFields renders the memory picture behind a fit decision as structured
slog attributes, so the operator can see how much headroom the load was admitted
(or refused) against without re-deriving it from the raw report.
*/
func admissionLogFields(report FitReport) []any {
	return []any{
		"decision", string(report.Decision),
		"os", report.Host.OS,
		"total_bytes", report.Host.TotalMemoryBytes,
		"available_bytes", report.Host.AvailableMemoryBytes,
		"available_budget_bytes", report.AvailableMemoryBudget,
		"estimated_runtime_bytes", report.EstimatedRuntimeBytes,
		"estimated_kv_bytes", report.EstimatedKVBytes,
		"os_reserve_bytes", report.OSReserveBytes,
		"ctx", report.ContextTokens,
		"gpu_offload", report.GPUOffload,
	}
}

/* logAdmission emits the Stage-0 admission telemetry at the appropriate level. */
func (r *Runtime) logAdmission(report FitReport) {
	level := slog.LevelInfo
	msg := "llamacpp: admission passed"
	if !report.Fits {
		level = slog.LevelWarn
		msg = "llamacpp: admission refused"
	}
	r.logger.Log(context.Background(), level, msg, admissionLogFields(report)...)
}
