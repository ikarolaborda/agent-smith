/*
Package verify provides primary-source verification of security claims that
appear in model output. Its job is the independent, system-enforced half of the
anti-fabrication guardrail: after a model produces a final answer, the verifier
checks any CVE identifiers it cites against the authoritative NIST NVD REST API
and returns a NON-DESTRUCTIVE advisory note. It never rewrites or deletes model
text — it appends a clearly-labelled verification block so a fabricated or
misattributed CVE is visible to the operator.

The verifier is opt-in and fail-soft: it performs network I/O only when it is
configured AND the answer actually cites a CVE, and a transport failure yields a
soft "could not verify" note, never a fabrication claim. Only NVD's own
authoritative "no such record" (totalResults == 0) is treated as a likely
fabrication signal.
*/
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

/* DefaultBaseURL is the NIST NVD CVE REST API 2.0 root. */
const DefaultBaseURL = "https://services.nvd.nist.gov/rest/json/cves/2.0"

/* DefaultMaxIDs caps how many distinct CVE ids are verified per answer, bounding egress. */
const DefaultMaxIDs = 12

/* defaultTimeout bounds a single NVD lookup so verification never stalls a response. */
const defaultTimeout = 8 * time.Second

/*
cveRe matches CVE identifiers with word boundaries so ids embedded in longer
tokens are not picked up accidentally. NVD ids are CVE-YYYY-NNNN.. (4+ digit
sequence). Matching is case-insensitive; ids are normalised to upper case.
*/
var cveRe = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)

/* httpDoer is the subset of *http.Client the verifier needs; lets tests inject a fake. */
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

/*
NVDVerifier checks CVE identifiers found in text against the NVD primary source.
It satisfies the agent.Verifier contract structurally (Verify(ctx, text) (string,
error)) without importing the agent package, so there is no import cycle.

Results are cached per normalised CVE id for the process lifetime; authoritative
outcomes (found / absent) are cached, transport failures are not, so a transient
NVD outage does not stick.
*/
type NVDVerifier struct {
	client    httpDoer
	baseURL   string
	apiKey    string
	maxIDs    int
	userAgent string

	mu    sync.Mutex
	cache map[string]lookup
}

/* Option configures an NVDVerifier. */
type Option func(*NVDVerifier)

/* WithHTTPClient overrides the HTTP client (used by tests to inject a fake transport). */
func WithHTTPClient(c httpDoer) Option {
	return func(v *NVDVerifier) {
		if c != nil {
			v.client = c
		}
	}
}

/* WithBaseURL overrides the NVD endpoint root (used by tests to point at a stub server). */
func WithBaseURL(u string) Option {
	return func(v *NVDVerifier) {
		if u != "" {
			v.baseURL = u
		}
	}
}

/*
WithAPIKey sets the NVD apiKey sent as the documented "apiKey" request header.
A key is optional but raises NVD's rate limit (≈5→50 requests / 30s); an empty
key simply omits the header.
*/
func WithAPIKey(k string) Option {
	return func(v *NVDVerifier) { v.apiKey = strings.TrimSpace(k) }
}

/* WithMaxIDs overrides the per-answer cap on verified ids. */
func WithMaxIDs(n int) Option {
	return func(v *NVDVerifier) {
		if n > 0 {
			v.maxIDs = n
		}
	}
}

/* NewNVDVerifier constructs a verifier with sane defaults; pass options to override. */
func NewNVDVerifier(opts ...Option) *NVDVerifier {
	v := &NVDVerifier{
		client: &http.Client{
			Timeout: defaultTimeout,
			/*
				Refuse redirects: the endpoint is fixed, so a redirect could only
				come from a compromised upstream/proxy, and following it would leak
				the apiKey header to an unintended host. ErrUseLastResponse stops
				the client at the 3xx without following it.
			*/
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		baseURL:   DefaultBaseURL,
		maxIDs:    DefaultMaxIDs,
		userAgent: "agent-smith-cve-verify/1.0",
		cache:     map[string]lookup{},
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

/*
Verify extracts CVE ids from text, checks each against NVD, and returns a
non-destructive advisory note to append to the answer. It returns an empty
string (and nil error) when the text cites no CVE, so callers can append
unconditionally. The returned error is reserved for caller-visible internal
failures; NVD lookup misses and transport errors are folded into the note as
soft language, never surfaced as errors.
*/
func (v *NVDVerifier) Verify(ctx context.Context, text string) (string, error) {
	if v == nil || strings.TrimSpace(text) == "" {
		return "", nil
	}
	ids := extractCVEIDs(text, v.maxIDs)
	if len(ids) == 0 {
		return "", nil
	}

	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, "- "+v.lookupNote(ctx, id))
	}

	var b strings.Builder
	b.WriteString("\n\n[CVE verification — checked against the NIST NVD primary source]\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\nNVD confirms identity/score only; it does not confirm that a cited CVE actually applies to the product or version under discussion — verify applicability against the NVD record.")
	return b.String(), nil
}

/* lookupStatus is the three-class verdict for one CVE id. */
type lookupStatus int

const (
	statusFound lookupStatus = iota
	statusAbsent
	statusUnverifiable
)

/* lookup is one CVE's verification outcome (cacheable for found/absent). */
type lookup struct {
	status   lookupStatus
	cvss     string
	severity string
	cvssVer  string
	desc     string
}

/*
lookupNote returns the human-readable advisory fragment for one id, consulting
the cache first. Transport failures are returned as soft, uncached notes.
*/
func (v *NVDVerifier) lookupNote(ctx context.Context, id string) string {
	v.mu.Lock()
	cached, ok := v.cache[id]
	v.mu.Unlock()
	if ok {
		return renderLookup(id, cached)
	}

	res := v.fetch(ctx, id)
	if res.status != statusUnverifiable {
		v.mu.Lock()
		v.cache[id] = res
		v.mu.Unlock()
	}
	return renderLookup(id, res)
}

/* renderLookup turns a lookup outcome into the advisory line for one id. */
func renderLookup(id string, l lookup) string {
	switch l.status {
	case statusFound:
		out := id + ": found in NVD"
		if l.cvss != "" {
			out += "; CVSS " + l.cvss
			if l.severity != "" {
				out += " " + l.severity
			}
			if l.cvssVer != "" {
				out += " (v" + l.cvssVer + ")"
			}
		} else {
			out += "; no CVSS score published"
		}
		if l.desc != "" {
			out += `. NVD: "` + l.desc + `"`
		}
		return out
	case statusAbsent:
		return id + ": NOT found in NVD — likely fabricated or mistyped. Do not rely on it without a primary source."
	default:
		return id + ": could not verify (NVD unreachable); treat as unconfirmed."
	}
}

/* fetch performs one NVD lookup and classifies the outcome. */
func (v *NVDVerifier) fetch(ctx context.Context, id string) lookup {
	reqURL := v.baseURL + "?" + url.Values{"cveId": {id}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return lookup{status: statusUnverifiable}
	}
	req.Header.Set("User-Agent", v.userAgent)
	if v.apiKey != "" {
		req.Header.Set("apiKey", v.apiKey)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return lookup{status: statusUnverifiable}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return lookup{status: statusUnverifiable}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return lookup{status: statusUnverifiable}
	}
	return parseNVD(body)
}

/* nvdResponse mirrors the subset of the NVD 2.0 schema the verifier reads. */
type nvdResponse struct {
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE struct {
			ID           string `json:"id"`
			Descriptions []struct {
				Lang  string `json:"lang"`
				Value string `json:"value"`
			} `json:"descriptions"`
			Metrics struct {
				V31 []nvdMetric `json:"cvssMetricV31"`
				V30 []nvdMetric `json:"cvssMetricV30"`
				V2  []nvdMetric `json:"cvssMetricV2"`
			} `json:"metrics"`
		} `json:"cve"`
	} `json:"vulnerabilities"`
}

/*
nvdMetric covers both v3.x (baseSeverity inside cvssData) and v2 (baseSeverity
at the metric level), so severity extraction tolerates either shape.
*/
type nvdMetric struct {
	BaseSeverity string `json:"baseSeverity"`
	CvssData     struct {
		Version      string  `json:"version"`
		BaseScore    float64 `json:"baseScore"`
		BaseSeverity string  `json:"baseSeverity"`
	} `json:"cvssData"`
}

/*
parseNVD turns an NVD response body into a lookup. totalResults == 0 (or no
vulnerabilities) is the authoritative-absent signal. A found record degrades
gracefully when metrics or descriptions are missing.
*/
func parseNVD(body []byte) lookup {
	var r nvdResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return lookup{status: statusUnverifiable}
	}
	if r.TotalResults == 0 || len(r.Vulnerabilities) == 0 {
		return lookup{status: statusAbsent}
	}

	cve := r.Vulnerabilities[0].CVE
	out := lookup{status: statusFound, desc: snippet(pickDescription(cve.Descriptions))}

	if m, ok := pickMetric(cve.Metrics.V31, cve.Metrics.V30, cve.Metrics.V2); ok {
		out.cvss = fmt.Sprintf("%.1f", m.CvssData.BaseScore)
		out.cvssVer = m.CvssData.Version
		if m.CvssData.BaseSeverity != "" {
			out.severity = m.CvssData.BaseSeverity
		} else {
			out.severity = m.BaseSeverity
		}
	}
	return out
}

/* pickMetric returns the highest-preference available CVSS metric: v3.1 > v3.0 > v2. */
func pickMetric(v31, v30, v2 []nvdMetric) (nvdMetric, bool) {
	for _, set := range [][]nvdMetric{v31, v30, v2} {
		if len(set) > 0 {
			return set[0], true
		}
	}
	return nvdMetric{}, false
}

/* pickDescription returns the English description, falling back to the first available. */
func pickDescription(ds []struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}) string {
	for _, d := range ds {
		if strings.EqualFold(d.Lang, "en") {
			return d.Value
		}
	}
	if len(ds) > 0 {
		return ds[0].Value
	}
	return ""
}

/* snippet collapses whitespace and truncates a description for the advisory line. */
func snippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 180
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

/*
extractCVEIDs returns the distinct CVE ids in text, upper-cased and in first-seen
order, capped at maxIDs to bound egress.
*/
func extractCVEIDs(text string, maxIDs int) []string {
	matches := cveRe.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	order := make([]string, 0, len(matches))
	for _, m := range matches {
		id := strings.ToUpper(m)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		order = append(order, id)
		if maxIDs > 0 && len(order) >= maxIDs {
			break
		}
	}
	return order
}
