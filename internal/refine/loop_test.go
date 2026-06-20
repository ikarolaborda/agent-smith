package refine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

/* scriptedJudge returns pre-set verdicts per round and records the answers it saw. */
type scriptedJudge struct {
	verdicts []Verdict
	err      error
	seen     []string
	calls    int
}

func (j *scriptedJudge) Name() string { return "scripted" }

func (j *scriptedJudge) Judge(_ context.Context, _, answer string) (Verdict, error) {
	j.seen = append(j.seen, answer)
	if j.err != nil {
		return Verdict{}, j.err
	}
	v := j.verdicts[j.calls]
	if j.calls < len(j.verdicts)-1 {
		j.calls++
	}
	return v, nil
}

/* scriptedGen records the briefs it received and returns one answer per round. */
type scriptedGen struct {
	answers []string
	briefs  []string
	calls   int
}

func (g *scriptedGen) gen(_ context.Context, _, brief string) (string, error) {
	g.briefs = append(g.briefs, brief)
	a := g.answers[g.calls]
	if g.calls < len(g.answers)-1 {
		g.calls++
	}
	return a, nil
}

func TestRun_StopsOnUsable(t *testing.T) {
	gen := &scriptedGen{answers: []string{"first", "second"}}
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, FailureModes: []string{"unscoped-confidence"}},
		{Usable: true, Reasons: "grounded and well scoped"},
	}}

	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Usable || res.Reason != StopUsable {
		t.Fatalf("expected usable stop, got usable=%v reason=%s", res.Usable, res.Reason)
	}
	if res.FinalAnswer != "second" {
		t.Fatalf("final answer: %q", res.FinalAnswer)
	}
	if len(res.Rounds) != 2 {
		t.Fatalf("expected 2 rounds, got %d", len(res.Rounds))
	}
}

func TestRun_ExhaustionNeverReportsSuccess(t *testing.T) {
	gen := &scriptedGen{answers: []string{"a", "b", "c"}}
	/* Distinct failure modes each round so no-progress does not fire first. */
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, FailureModes: []string{"m1"}},
		{Usable: false, FailureModes: []string{"m2"}},
		{Usable: false, FailureModes: []string{"m3"}},
	}}

	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 3, MaxNoProgress: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Usable {
		t.Fatalf("exhaustion must never report success")
	}
	if res.Reason != StopMaxIters {
		t.Fatalf("expected max_iters reason, got %s", res.Reason)
	}
	if len(res.Rounds) != 3 {
		t.Fatalf("expected 3 rounds, got %d", len(res.Rounds))
	}
}

func TestRun_PicksLeastFabricatedOnExhaustion(t *testing.T) {
	gen := &scriptedGen{answers: []string{"messy", "clean", "messier"}}
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, FailureModes: []string{"a", "b", "c"}},
		{Usable: false, FailureModes: []string{"a"}},
		{Usable: false, FailureModes: []string{"a", "b", "c", "d"}},
	}}

	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 3, MaxNoProgress: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalAnswer != "clean" {
		t.Fatalf("expected least-fabricated 'clean', got %q", res.FinalAnswer)
	}
}

func TestRun_NoProgressTerminatesEarly(t *testing.T) {
	gen := &scriptedGen{answers: []string{"x", "x", "x", "x"}}
	same := Verdict{Usable: false, FailureModes: []string{"fabricated-cve", "unscoped"}}
	judge := &scriptedJudge{verdicts: []Verdict{same, same, same, same}}

	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 10, MaxNoProgress: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != StopNoProgress {
		t.Fatalf("expected no_progress, got %s", res.Reason)
	}
	/* Round 1 sets the signature, rounds 2 and 3 are the two repeats that trip it. */
	if len(res.Rounds) != 3 {
		t.Fatalf("expected early stop at 3 rounds, got %d", len(res.Rounds))
	}
}

func TestRun_JudgeErrorIsFailClosed(t *testing.T) {
	gen := &scriptedGen{answers: []string{"a", "b"}}
	judge := &scriptedJudge{err: errors.New("boom")}

	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 2, MaxNoProgress: 5})
	if err != nil {
		t.Fatalf("Run should not surface a judge error: %v", err)
	}
	if res.Usable {
		t.Fatalf("judge error must never pass")
	}
	for _, r := range res.Rounds {
		if r.Verdict.Usable {
			t.Fatalf("judge-error round marked usable")
		}
	}
}

func TestRun_FeedsBoundedBriefForward(t *testing.T) {
	gen := &scriptedGen{answers: []string{"a", "b"}}
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, Reasons: "unsupported CVE", Fixes: []string{"cite NVD", "drop CVSS", "extra1", "extra2"}, FailureModes: []string{"fabricated-cve"}},
		{Usable: true},
	}}

	_, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(gen.briefs) < 2 {
		t.Fatalf("expected a second round with a brief")
	}
	if gen.briefs[0] != "" {
		t.Fatalf("first brief must be empty, got %q", gen.briefs[0])
	}
	brief := gen.briefs[1]
	if !strings.Contains(brief, "unsupported CVE") {
		t.Fatalf("brief should carry the reviewer concern: %q", brief)
	}
	/* Bounded to at most two fixes. */
	if strings.Contains(brief, "extra1") || strings.Contains(brief, "extra2") {
		t.Fatalf("brief carried more than two fixes: %q", brief)
	}
}

func TestBuildBrief_NeverInducesFabrication(t *testing.T) {
	brief := strings.ToLower(buildBrief(Verdict{
		Reasons: "x",
		Fixes:   []string{"ground the claim"},
	}))
	forbidden := []string{"find a vuln", "find a vulnerability", "be more confident", "more confident", "must find", "discover a"}
	for _, f := range forbidden {
		if strings.Contains(brief, f) {
			t.Fatalf("brief contains fabrication-inducing phrase %q: %q", f, brief)
		}
	}
	for _, want := range []string{"grounded", "scoped", "honest negative"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief missing expected guidance %q: %q", want, brief)
		}
	}
}

func TestRun_OnRoundFiresPerRoundInOrder(t *testing.T) {
	gen := &scriptedGen{answers: []string{"a", "b", "c"}}
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, FailureModes: []string{"m1"}},
		{Usable: false, FailureModes: []string{"m2"}},
		{Usable: true},
	}}
	var seen []int
	_, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{
		MaxIters:      5,
		OnRound:       func(r Round) { seen = append(seen, r.Iter) },
		MaxNoProgress: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 3 || seen[0] != 1 || seen[1] != 2 || seen[2] != 3 {
		t.Fatalf("OnRound should fire once per round in order, got %v", seen)
	}
}

func TestSignature_NormalizesSetSemantics(t *testing.T) {
	/* Reordered, recased, padded, and duplicated tags must canonicalise identically. */
	a := signature([]string{"Fabricated-CVE", " unscoped "})
	b := signature([]string{"unscoped", "fabricated-cve", "FABRICATED-CVE"})
	if a != b {
		t.Fatalf("signatures should match across order/case/dupes: %q vs %q", a, b)
	}
	if signature(nil) != "" {
		t.Fatalf("empty modes should yield empty signature")
	}
}

func TestRun_NoProgressIgnoresOrderingJitter(t *testing.T) {
	gen := &scriptedGen{answers: []string{"x", "x", "x", "x"}}
	/* Same set, different order/case each round — must still trip no-progress. */
	judge := &scriptedJudge{verdicts: []Verdict{
		{Usable: false, FailureModes: []string{"fabricated-cve", "unscoped"}},
		{Usable: false, FailureModes: []string{"unscoped", "Fabricated-CVE"}},
		{Usable: false, FailureModes: []string{"FABRICATED-CVE", "unscoped"}},
	}}
	res, err := Run(context.Background(), "task", gen.gen, judge, LoopConfig{MaxIters: 10, MaxNoProgress: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reason != StopNoProgress {
		t.Fatalf("expected no_progress despite ordering jitter, got %s", res.Reason)
	}
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantUsable bool
		wantModes  int
	}{
		{"usable", "VERDICT: USABLE\nREASONS: grounded\nFIXES: NONE\nFAILURE_MODES: NONE", true, 0},
		{"not usable", "VERDICT: NOT_USABLE\nREASONS: bad\nFIXES: cite source\nFAILURE_MODES: fabricated-cve;unscoped", false, 2},
		{"not usable substring safe", "VERDICT: NOT_USABLE", false, 0},
		{"unparsable fails closed", "the model rambled without a verdict", false, 1},
		{"empty fails closed", "", false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := parseVerdict(c.raw)
			if v.Usable != c.wantUsable {
				t.Fatalf("usable: got %v want %v", v.Usable, c.wantUsable)
			}
			if len(v.FailureModes) != c.wantModes {
				t.Fatalf("failure modes: got %d (%v) want %d", len(v.FailureModes), v.FailureModes, c.wantModes)
			}
		})
	}
}
