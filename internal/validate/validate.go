/*
Package validate provides a mandatory-but-advisory cross-provider validation
layer for vulnerability-research answers. Where internal/verify checks cited CVE
ids against the NVD primary source (which only covers ALREADY-documented
vulnerabilities), this layer sends the model's finished answer to one or more
INDEPENDENT models — OpenAI via its API, and Anthropic via the operator's Claude
Max subscription through the Claude Code CLI — and asks each to critique the
security claims for accuracy and fabrication. That covers the case the NVD gate
cannot: an answer attempting to document a NEW vulnerability with no NVD record.

The layer is opt-in, fail-soft, and strictly NON-AUTHORITATIVE: a second-opinion
model is itself fallible, so its verdict is appended as a labelled advisory, never
used to rewrite, block, or adjudicate the original answer. It runs only on answers
that look like vulnerability research, to bound egress and cost.
*/
package validate

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

/* DefaultMaxAnswerBytes caps how much of an answer is sent to each reviewer. */
const DefaultMaxAnswerBytes = 6000

/*
	DefaultReviewerTimeout bounds a single reviewer call. The Claude CLI path is a

real model round-trip and is meaningfully slower than an API call, so this is
generous on purpose; the whole layer is an opt-in post-answer advisory, not on the
latency-critical generation path.
*/
const DefaultReviewerTimeout = 40 * time.Second

/*
Reviewer is one independent second opinion. Name identifies it in the advisory;
Review returns a short plain-text verdict on the answer's security claims, or an
error (which the validator folds into a soft "could not validate" note).
*/
type Reviewer interface {
	Name() string
	Review(ctx context.Context, answer string) (string, error)
}

/*
CrossProviderValidator implements the agent.Verifier contract structurally
(Verify(ctx, text) (string, error)) without importing the agent package. It runs
the configured reviewers concurrently over a vulnerability-research answer and
returns a labelled, non-destructive advisory note.
*/
type CrossProviderValidator struct {
	reviewers      []Reviewer
	maxAnswerBytes int
	timeout        time.Duration
}

/* Option configures a CrossProviderValidator. */
type Option func(*CrossProviderValidator)

/* WithTimeout overrides the per-reviewer timeout. */
func WithTimeout(d time.Duration) Option {
	return func(v *CrossProviderValidator) {
		if d > 0 {
			v.timeout = d
		}
	}
}

/* WithMaxAnswerBytes overrides how much of the answer is sent to reviewers. */
func WithMaxAnswerBytes(n int) Option {
	return func(v *CrossProviderValidator) {
		if n > 0 {
			v.maxAnswerBytes = n
		}
	}
}

/*
NewCrossProviderValidator builds a validator over the given reviewers (nil
reviewers are dropped). It returns nil when no reviewer is available, so callers
can treat "no validator" uniformly.
*/
func NewCrossProviderValidator(reviewers []Reviewer, opts ...Option) *CrossProviderValidator {
	live := make([]Reviewer, 0, len(reviewers))
	for _, r := range reviewers {
		if r != nil {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return nil
	}
	v := &CrossProviderValidator{
		reviewers:      live,
		maxAnswerBytes: DefaultMaxAnswerBytes,
		timeout:        DefaultReviewerTimeout,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

/*
Verify runs the cross-provider validation when the answer looks like
vulnerability research and returns a non-destructive advisory note (empty when
the answer is not vuln-research, so callers can append unconditionally). The
returned error is always nil: reviewer failures are folded into soft per-provider
notes, never surfaced as a blocking error.
*/
func (v *CrossProviderValidator) Verify(ctx context.Context, text string) (string, error) {
	if v == nil || len(v.reviewers) == 0 || !IsVulnResearch(text) {
		return "", nil
	}

	answer := text
	if len(answer) > v.maxAnswerBytes {
		answer = answer[:v.maxAnswerBytes]
	}

	type result struct {
		name    string
		verdict string
	}
	results := make([]result, len(v.reviewers))
	var wg sync.WaitGroup
	for i, r := range v.reviewers {
		wg.Add(1)
		go func(i int, r Reviewer) {
			defer wg.Done()
			rctx, cancel := context.WithTimeout(ctx, v.timeout)
			defer cancel()
			verdict, err := r.Review(rctx, answer)
			if err != nil || strings.TrimSpace(verdict) == "" {
				results[i] = result{name: r.Name(), verdict: "could not validate (provider unavailable or timed out); treat as unconfirmed."}
				return
			}
			results[i] = result{name: r.Name(), verdict: collapse(verdict)}
		}(i, r)
	}
	wg.Wait()

	var b strings.Builder
	b.WriteString("\n\n[Cross-provider validation — independent model opinions, NOT authoritative; a second model can be wrong too]")
	for _, res := range results {
		b.WriteString("\n- ")
		b.WriteString(res.name)
		b.WriteString(": ")
		b.WriteString(res.verdict)
	}
	return b.String(), nil
}

/* collapse normalises a reviewer verdict to a single tidy line, capped in length. */
func collapse(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 600
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

/*
vulnKeywordRe matches the vocabulary of vulnerability research with word
boundaries, so the validator fires on answers attempting to document a new or
existing vulnerability even when no CVE id is present.
*/
var vulnKeywordRe = regexp.MustCompile(`(?i)\b(?:` +
	`vulnerabilit(?:y|ies)|exploit(?:able|ation)?|CVE-\d{4}-\d{4,7}|CWE-\d+|` +
	`remote code execution|RCE|arbitrary code|privilege escalation|` +
	`buffer overflow|use[- ]after[- ]free|out[- ]of[- ]bounds|` +
	`SQL injection|command injection|path traversal|deserialization|` +
	`zero[- ]day|0-day|proof[- ]of[- ]concept|security advisory|` +
	`memory corruption|heap overflow|stack overflow|XXE|SSRF` +
	`)\b`)

/* IsVulnResearch reports whether text reads like vulnerability research. */
func IsVulnResearch(text string) bool {
	return vulnKeywordRe.MatchString(text)
}
