package llamacpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

/*
it_reclaims_a_prior_runtime_and_reports_freed proves the Stage-1 single-model
invariant: Reclaim stops the superseded llama-server, returns only after it is
reaped, and reports the estimate the prior model was admitted against.
*/
func it_reclaims_a_prior_runtime_and_reports_freed(t *testing.T) {
	prior := startFake(t, "ok", 10*time.Second)
	if err := prior.Start(context.Background()); err != nil {
		t.Fatalf("prior Start: %v", err)
	}
	if prior.AdmittedRuntimeBytes() == 0 {
		t.Fatal("admitted runtime bytes should be non-zero after a successful start")
	}

	outcome, err := Reclaim(context.Background(), prior)
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if !outcome.ClosedRuntime {
		t.Fatal("Reclaim should report the prior runtime was closed")
	}
	if outcome.FreedEstimateBytes == 0 {
		t.Fatal("Reclaim should report a non-zero freed estimate")
	}
	if prior.State() != RuntimeStopped {
		t.Fatalf("prior state = %s, want stopped", prior.State())
	}
	if prior.AdmittedRuntimeBytes() != 0 {
		t.Fatal("admitted estimate should be cleared once the process is reaped")
	}
}

func TestReclaimClosesPriorAndReportsFreed(t *testing.T) {
	it_reclaims_a_prior_runtime_and_reports_freed(t)
}

func TestReclaimNilPriorIsNoop(t *testing.T) {
	outcome, err := Reclaim(context.Background(), nil)
	if err != nil {
		t.Fatalf("Reclaim(nil): %v", err)
	}
	if outcome.ClosedRuntime || outcome.FreedEstimateBytes != 0 {
		t.Fatalf("nil prior should be a no-op, got %+v", outcome)
	}
}

/*
it_runs_the_reclaim_hook_before_measuring is the ordering guarantee: the
pre-admission hook must run before the host is profiled, so a supersede's freed
memory is visible to the fit gate. A profiler that fails unless the hook already
ran makes the ordering observable.
*/
func it_runs_the_reclaim_hook_before_measuring(t *testing.T) {
	t.Setenv("LLAMACPP_FAKE", "ok")
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("GGUF-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}

	var hookRan atomic.Bool
	orderingProfiler := ProfilerFunc(func(context.Context, string) (HostProfile, error) {
		if !hookRan.Load() {
			return HostProfile{}, errors.New("host measured before self-reclamation hook ran")
		}
		return HostProfile{
			OS: "test", Arch: "test", TotalMemoryBytes: 64 * byteGiB,
			AvailableMemoryBytes: 48 * byteGiB, FreeDiskBytes: 100 * byteGiB,
		}, nil
	})

	rt := NewRuntime(RuntimeConfig{
		Binary:           os.Args[0],
		ModelPath:        model,
		Profiler:         orderingProfiler,
		StartupTimeout:   10 * time.Second,
		AdmissionLockDir: t.TempDir(),
		ReclaimBeforeStart: func(context.Context) (ReclaimOutcome, error) {
			hookRan.Store(true)
			return ReclaimOutcome{ClosedRuntime: true, FreedEstimateBytes: 3 * byteGiB, Note: "test"}, nil
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Close(context.Background()) }()
	if !hookRan.Load() {
		t.Fatal("reclaim hook was never invoked")
	}
}

func TestReclaimHookRunsBeforeMeasurement(t *testing.T) {
	it_runs_the_reclaim_hook_before_measuring(t)
}

/*
it_fails_start_closed_when_the_hook_errors proves the fail-closed contract: if
pre-admission reclamation errors, Start must refuse rather than admit a model
against a host it never reclaimed.
*/
func it_fails_start_closed_when_the_hook_errors(t *testing.T) {
	t.Setenv("LLAMACPP_FAKE", "ok")
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("GGUF-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(RuntimeConfig{
		Binary:           os.Args[0],
		ModelPath:        model,
		Profiler:         ampleProfiler(),
		StartupTimeout:   10 * time.Second,
		AdmissionLockDir: t.TempDir(),
		ReclaimBeforeStart: func(context.Context) (ReclaimOutcome, error) {
			return ReclaimOutcome{}, errors.New("reclaim failed")
		},
	})
	err := rt.Start(context.Background())
	if err == nil {
		_ = rt.Close(context.Background())
		t.Fatal("expected Start to fail closed when the reclaim hook errors")
	}
	if rt.State() == RuntimeReady {
		t.Fatal("runtime must not be ready after a failed pre-admission reclamation")
	}
}

func TestReclaimHookErrorFailsStartClosed(t *testing.T) {
	it_fails_start_closed_when_the_hook_errors(t)
}

/* Stage-0 telemetry must expose the memory terms behind a decision. */
func TestAdmissionLogFieldsExposeMemoryPicture(t *testing.T) {
	report := FitReport{
		Decision:              FitDecisionFit,
		Fits:                  true,
		Host:                  HostProfile{OS: "linux", TotalMemoryBytes: 16 * byteGiB, AvailableMemoryBytes: 9 * byteGiB},
		AvailableMemoryBudget: 8 * byteGiB,
		EstimatedRuntimeBytes: 6 * byteGiB,
		EstimatedKVBytes:      1 * byteGiB,
		OSReserveBytes:        4 * byteGiB,
		ContextTokens:         4096,
	}
	fields := admissionLogFields(report)
	got := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		key, _ := fields[i].(string)
		got[key] = fields[i+1]
	}
	for _, key := range []string{"decision", "available_budget_bytes", "estimated_runtime_bytes", "available_bytes", "os_reserve_bytes"} {
		if _, ok := got[key]; !ok {
			t.Errorf("admission log fields missing %q", key)
		}
	}
	if got["available_budget_bytes"] != uint64(8*byteGiB) {
		t.Errorf("available_budget_bytes = %v, want %d", got["available_budget_bytes"], 8*byteGiB)
	}
}
