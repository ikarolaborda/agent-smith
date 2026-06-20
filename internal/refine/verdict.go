/*
Package refine implements a bounded, opt-in refinement loop for vulnerability-
research answers: a strict judge (an independent model, gpt-5.x via the OpenAI
client) decides whether an answer is USABLE, and when it is not, the answer is
regenerated with a compact refinement brief and re-judged, up to a bounded number
of iterations.

The loop is deliberately anti-fabrication-first. "Usable" does NOT mean "found a
vulnerability": an honestly-scoped negative ("no confirmed vulnerability",
"needs a sanitizer build to confirm") is a PASS when it is grounded and well
labelled. Fabricated or unsupported specifics (invented CVE/CVSS/version/offset,
confidence without evidence, hypothesis dressed as fact) are a FAIL. The loop
never fakes a pass: if it cannot reach a usable answer within the budget it
returns the least-fabricated attempt together with the full per-round ledger and
an honest "did not reach usable" reason.

This package is opt-in and its core loop never edits answers token-by-token: each
round is a whole non-streaming generation. A caller may observe rounds as they are
judged via LoopConfig.OnRound (used by the server's SSE handler to stream
per-round progress); the loop itself takes no dependency on any transport.
*/
package refine

/*
Verdict is a structured judgement of a single candidate answer. It is the loop's
branch signal, so it is intentionally machine-shaped rather than prose. FailureModes
are short, stable tags (e.g. "fabricated-cve", "unscoped-confidence") used for
no-progress detection and least-fabricated selection across rounds.
*/
type Verdict struct {
	Usable       bool
	Reasons      string
	Fixes        []string
	FailureModes []string
}
