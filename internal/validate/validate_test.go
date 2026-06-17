package validate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

/* fakeReviewer returns a canned verdict or error. */
type fakeReviewer struct {
	name    string
	verdict string
	err     error
}

func (f fakeReviewer) Name() string { return f.name }
func (f fakeReviewer) Review(context.Context, string) (string, error) {
	return f.verdict, f.err
}

func TestIsVulnResearch(t *testing.T) {
	yes := []string{
		"This is exploitable via a buffer overflow.",
		"Consider CVE-2021-44228 in your stack.",
		"A SQL injection in the login form.",
		"Potential remote code execution (RCE).",
		"CWE-79 cross-site scripting issue.",
	}
	no := []string{
		"How do I format a date in Go?",
		"Refactor this controller into a service.",
		"The weather is nice today.",
	}
	for _, s := range yes {
		if !IsVulnResearch(s) {
			t.Fatalf("expected vuln-research: %q", s)
		}
	}
	for _, s := range no {
		if IsVulnResearch(s) {
			t.Fatalf("expected NOT vuln-research: %q", s)
		}
	}
}

/* countingReviewer fails the test if Review is ever called. */
type countingReviewer struct{ t *testing.T }

func (countingReviewer) Name() string { return "Counter" }
func (r countingReviewer) Review(context.Context, string) (string, error) {
	r.t.Fatalf("reviewer must NOT be invoked for a non-vuln answer (offline-first egress gate)")
	return "", nil
}

func TestValidator_NonVulnNeverInvokesReviewers(t *testing.T) {
	v := NewCrossProviderValidator([]Reviewer{countingReviewer{t: t}})
	note, err := v.Verify(context.Background(), "just a plain question about pointers")
	if err != nil || note != "" {
		t.Fatalf("non-vuln answer must yield no note: %q / %v", note, err)
	}
}

func TestValidator_AggregatesReviewerVerdicts(t *testing.T) {
	v := NewCrossProviderValidator([]Reviewer{
		fakeReviewer{name: "OpenAI", verdict: "The CVE looks fabricated."},
		fakeReviewer{name: "Claude (Max)", verdict: "Agree, no NVD record exists."},
	})
	note, err := v.Verify(context.Background(), "I found a new buffer overflow, likely CVE-2099-0001.")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, want := range []string{"Cross-provider validation", "NOT authoritative", "OpenAI: The CVE looks fabricated.", "Claude (Max): Agree"} {
		if !strings.Contains(note, want) {
			t.Fatalf("aggregated note missing %q: %q", want, note)
		}
	}
}

func TestValidator_ReviewerFailureIsSoft(t *testing.T) {
	v := NewCrossProviderValidator([]Reviewer{
		fakeReviewer{name: "OpenAI", err: errors.New("rate limited")},
		fakeReviewer{name: "Claude (Max)", verdict: "Looks plausible."},
	})
	note, err := v.Verify(context.Background(), "exploit for an out-of-bounds read")
	if err != nil {
		t.Fatalf("reviewer failure must not error out: %v", err)
	}
	if !strings.Contains(note, "OpenAI: could not validate") {
		t.Fatalf("failed reviewer must degrade softly: %q", note)
	}
	if !strings.Contains(note, "Claude (Max): Looks plausible.") {
		t.Fatalf("the surviving reviewer must still report: %q", note)
	}
}

func TestValidator_NoReviewersYieldsNil(t *testing.T) {
	if v := NewCrossProviderValidator(nil); v != nil {
		t.Fatalf("expected nil validator when no reviewers")
	}
	if v := NewCrossProviderValidator([]Reviewer{nil, nil}); v != nil {
		t.Fatalf("nil reviewers must be dropped, leaving a nil validator")
	}
}

func TestValidator_TruncatesLongAnswer(t *testing.T) {
	var got string
	rec := recordingReviewer{seen: &got}
	v := NewCrossProviderValidator([]Reviewer{rec}, WithMaxAnswerBytes(50))
	long := "exploit " + strings.Repeat("A", 500)
	if _, err := v.Verify(context.Background(), long); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("answer sent to reviewer must be truncated to 50 bytes, got %d", len(got))
	}
}

/* recordingReviewer captures the answer it was given. */
type recordingReviewer struct{ seen *string }

func (recordingReviewer) Name() string { return "Rec" }
func (r recordingReviewer) Review(_ context.Context, answer string) (string, error) {
	*r.seen = answer
	return "ok", nil
}
