package verify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

/* roundTripFunc adapts a function to httpDoer for stubbing NVD responses. */
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

const foundV31 = `{"totalResults":1,"vulnerabilities":[{"cve":{"id":"CVE-2021-44228",
"descriptions":[{"lang":"en","value":"Apache Log4j2 JNDI features do not protect against attacker-controlled LDAP."}],
"metrics":{"cvssMetricV31":[{"cvssData":{"version":"3.1","baseScore":10.0,"baseSeverity":"CRITICAL"}}],
"cvssMetricV2":[{"baseSeverity":"HIGH","cvssData":{"version":"2.0","baseScore":9.3}}]}}}]}`

const foundV2Only = `{"totalResults":1,"vulnerabilities":[{"cve":{"id":"CVE-2008-0001",
"descriptions":[{"lang":"en","value":"Old issue."}],
"metrics":{"cvssMetricV2":[{"baseSeverity":"MEDIUM","cvssData":{"version":"2.0","baseScore":5.0}}]}}}]}`

const foundNoMetrics = `{"totalResults":1,"vulnerabilities":[{"cve":{"id":"CVE-2024-9999",
"descriptions":[{"lang":"en","value":"Awaiting analysis."}],"metrics":{}}}]}`

const absent = `{"totalResults":0,"vulnerabilities":[]}`

func TestVerify_NoCVEReturnsEmpty(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("must not call NVD when no CVE is present")
		return nil, nil
	})))
	note, err := v.Verify(context.Background(), "just a normal answer about TLS and PHP")
	if err != nil || note != "" {
		t.Fatalf("expected empty note no error, got %q / %v", note, err)
	}
}

func TestVerify_FoundSurfacesAuthoritativeCVSS(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, foundV31), nil
	})))
	note, err := v.Verify(context.Background(), "You should patch CVE-2021-44228 immediately.")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, want := range []string{"CVE-2021-44228", "found in NVD", "CVSS 10.0", "CRITICAL", "(v3.1)"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing %q: %q", want, note)
		}
	}
}

func TestVerify_AbsentFlagsLikelyFabrication(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, absent), nil
	})))
	note, _ := v.Verify(context.Background(), "The relevant issue is CVE-2021-30642.")
	if !strings.Contains(note, "NOT found in NVD") || !strings.Contains(note, "likely fabricated") {
		t.Fatalf("absent CVE must be flagged as likely fabricated: %q", note)
	}
}

func TestVerify_TransportFailureIsSoftNotFabrication(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: timeout")
	})))
	note, err := v.Verify(context.Background(), "See CVE-2021-44228.")
	if err != nil {
		t.Fatalf("transport failure must not error out: %v", err)
	}
	if !strings.Contains(note, "could not verify") || strings.Contains(note, "fabricated") {
		t.Fatalf("transport failure must be soft, never a fabrication claim: %q", note)
	}
}

func TestVerify_Http5xxIsUnverifiable(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusServiceUnavailable, "upstream down"), nil
	})))
	note, _ := v.Verify(context.Background(), "CVE-2021-44228 again.")
	if !strings.Contains(note, "could not verify") {
		t.Fatalf("5xx must yield unverifiable note: %q", note)
	}
}

func TestVerify_V2OnlyAndMissingMetricsDegrade(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.RawQuery, "CVE-2008-0001") {
			return jsonResp(http.StatusOK, foundV2Only), nil
		}
		return jsonResp(http.StatusOK, foundNoMetrics), nil
	})))
	note, _ := v.Verify(context.Background(), "Compare CVE-2008-0001 and CVE-2024-9999.")
	if !strings.Contains(note, "CVSS 5.0") || !strings.Contains(note, "(v2.0)") {
		t.Fatalf("v2-only record must surface its v2 score: %q", note)
	}
	if !strings.Contains(note, "CVE-2024-9999: found in NVD; no CVSS score published") {
		t.Fatalf("missing-metrics record must degrade to a minimal found note: %q", note)
	}
}

func TestVerify_DedupesAndCachesAndSendsAPIKey(t *testing.T) {
	var calls int32
	var sawKey int32
	v := NewNVDVerifier(
		WithAPIKey("secret-key"),
		WithHTTPClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			if req.Header.Get("apiKey") == "secret-key" {
				atomic.AddInt32(&sawKey, 1)
			}
			return jsonResp(http.StatusOK, foundV31), nil
		})),
	)
	/* Same id thrice in one answer, then a second answer — exactly one NVD call total. */
	if _, err := v.Verify(context.Background(), "CVE-2021-44228 cve-2021-44228 CVE-2021-44228"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if _, err := v.Verify(context.Background(), "again CVE-2021-44228"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 NVD call (dedup + cache), got %d", got)
	}
	if atomic.LoadInt32(&sawKey) != 1 {
		t.Fatalf("apiKey header was not sent to NVD")
	}
}

func TestVerify_CapsIDsToBoundEgress(t *testing.T) {
	var calls int32
	v := NewNVDVerifier(
		WithMaxIDs(2),
		WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			return jsonResp(http.StatusOK, absent), nil
		})),
	)
	_, _ = v.Verify(context.Background(), "CVE-2021-0001 CVE-2021-0002 CVE-2021-0003 CVE-2021-0004")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected egress capped at 2, got %d", got)
	}
}

func TestVerify_RateLimitAndForbiddenAreSoftNotAbsent(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResp(status, "rate limited"), nil
		})))
		note, _ := v.Verify(context.Background(), "CVE-2021-44228")
		if !strings.Contains(note, "could not verify") || strings.Contains(note, "fabricated") {
			t.Fatalf("status %d must be soft unverifiable, never absent/fabricated: %q", status, note)
		}
	}
}

func TestVerify_MalformedJSONIsSoft(t *testing.T) {
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, "{not json"), nil
	})))
	note, _ := v.Verify(context.Background(), "CVE-2021-44228")
	if !strings.Contains(note, "could not verify") {
		t.Fatalf("malformed JSON must be soft unverifiable: %q", note)
	}
}

func TestVerify_TransportFailureNotCachedRecoversOnRetry(t *testing.T) {
	var n int32
	v := NewNVDVerifier(WithHTTPClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return nil, errors.New("timeout")
		}
		return jsonResp(http.StatusOK, foundV31), nil
	})))
	first, _ := v.Verify(context.Background(), "CVE-2021-44228")
	if !strings.Contains(first, "could not verify") {
		t.Fatalf("first attempt should be soft: %q", first)
	}
	second, _ := v.Verify(context.Background(), "CVE-2021-44228")
	if !strings.Contains(second, "found in NVD") {
		t.Fatalf("transport failure must not be cached; retry should resolve: %q", second)
	}
}

func TestExtractCVEIDs_BoundaryAndCase(t *testing.T) {
	got := extractCVEIDs("xCVE-2021-1111 should not match inside a token, but CVE-2021-2222 and cve-2021-2222 dedupe", 0)
	if len(got) != 1 || got[0] != "CVE-2021-2222" {
		t.Fatalf("boundary/case/dedup extraction wrong: %v", got)
	}
}
