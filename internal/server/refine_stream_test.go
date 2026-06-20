package server

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/refine"
)

/* nopFlusher satisfies http.Flusher for an in-memory SSE encoder. */
type nopFlusher struct{}

func (nopFlusher) Flush() {}

/* fakeJudge returns scripted verdicts per call. */
type fakeJudge struct {
	verdicts []refine.Verdict
	calls    int
}

func (fakeJudge) Name() string { return "fake" }

func (j *fakeJudge) Judge(_ context.Context, _, _ string) (refine.Verdict, error) {
	v := j.verdicts[j.calls]
	if j.calls < len(j.verdicts)-1 {
		j.calls++
	}
	return v, nil
}

func runStreamRefine(t *testing.T, task string, answers []string, judge refine.Judge, maxIters int) string {
	t.Helper()
	var buf bytes.Buffer
	enc := newSSEEncoder(&buf, nopFlusher{})
	calls := 0
	gen := func(_ context.Context, _, _ string) (string, error) {
		a := answers[calls]
		if calls < len(answers)-1 {
			calls++
		}
		return a, nil
	}
	streamRefine(context.Background(), enc, "id-1", "model-x", 0, task, gen, judge, refine.LoopConfig{MaxIters: maxIters, MaxNoProgress: 5})
	return buf.String()
}

func TestStreamRefine_UsableEmitsRoundsThenSummaryThenContent(t *testing.T) {
	judge := &fakeJudge{verdicts: []refine.Verdict{
		{Usable: false, Reasons: "unsupported", FailureModes: []string{"fabricated-cve"}},
		{Usable: true, Reasons: "grounded"},
	}}
	out := runStreamRefine(t, "assess exif OoB", []string{"draft", "final-good"}, judge, 5)

	if strings.Count(out, "event: refine_round") != 2 {
		t.Fatalf("expected 2 refine_round events:\n%s", out)
	}
	summaryIdx := strings.Index(out, "event: refine_summary")
	if summaryIdx < 0 {
		t.Fatalf("missing refine_summary:\n%s", out)
	}
	if !strings.Contains(out, `"status":"usable"`) {
		t.Fatalf("summary should carry status=usable:\n%s", out)
	}
	contentIdx := strings.Index(out, "final-good")
	if contentIdx < 0 {
		t.Fatalf("final answer not streamed:\n%s", out)
	}
	/* Anti-fabrication ordering: the safety label (summary) must precede the content. */
	if summaryIdx > contentIdx {
		t.Fatalf("refine_summary must be emitted before the final content")
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "[DONE]") {
		t.Fatalf("stream must terminate with [DONE]:\n%s", out)
	}
}

func TestStreamRefine_ExhaustionLabelsLeastFabricated(t *testing.T) {
	judge := &fakeJudge{verdicts: []refine.Verdict{
		{Usable: false, FailureModes: []string{"a", "b"}},
		{Usable: false, FailureModes: []string{"a"}},
	}}
	out := runStreamRefine(t, "task", []string{"messy", "cleaner"}, judge, 2)

	if !strings.Contains(out, `"status":"least_fabricated"`) {
		t.Fatalf("exhaustion must be labelled least_fabricated, never usable:\n%s", out)
	}
	if strings.Contains(out, `"status":"usable"`) {
		t.Fatalf("a non-usable run must not report usable:\n%s", out)
	}
	if !strings.Contains(out, "cleaner") {
		t.Fatalf("least-fabricated attempt should be the final content:\n%s", out)
	}
}

func TestStreamRefine_NilJudgeIsHardError(t *testing.T) {
	var buf bytes.Buffer
	enc := newSSEEncoder(&buf, nopFlusher{})
	gen := func(_ context.Context, _, _ string) (string, error) { return "should-not-run", nil }
	streamRefine(context.Background(), enc, "id", "m", 0, "task", gen, nil, refine.LoopConfig{})
	out := buf.String()
	if !strings.Contains(out, "event: error") {
		t.Fatalf("nil judge must emit an error event:\n%s", out)
	}
	if strings.Contains(out, "should-not-run") || strings.Contains(out, "refine_summary") {
		t.Fatalf("nil judge must not return an unevaluated answer:\n%s", out)
	}
}

func TestClampRefineTimeout(t *testing.T) {
	if d := clampRefineTimeout(0); d != defaultRefineRoundTimeout {
		t.Fatalf("0 should map to default, got %s", d)
	}
	if d := clampRefineTimeout(10); d != minRefineRoundTimeout {
		t.Fatalf("below-min should clamp up, got %s", d)
	}
	if d := clampRefineTimeout(100000); d != maxRefineRoundTimeout {
		t.Fatalf("above-max should clamp down, got %s", d)
	}
	if d := clampRefineTimeout(300); d != 300*time.Second {
		t.Fatalf("in-range should pass through, got %s", d)
	}
}
