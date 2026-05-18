package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

/*
fakeDDG is a small lite-style HTML payload mirroring the structure
DDGSearcher.parseDDGLite expects: a table with result-link anchors and
result-snippet cells.
*/
const fakeDDG = `<html><body><table>
<tr><td><a class="result-link" href="https://go.dev/ref/spec#Select_statements">Go select statement &mdash; go.dev</a></td></tr>
<tr><td class="result-snippet">A &quot;select&quot; statement chooses which of a set of possible send or receive operations will proceed.</td></tr>
<tr><td><a class="result-link" href="https://pkg.go.dev/context">context package - pkg.go.dev</a></td></tr>
<tr><td class="result-snippet">Package context defines the Context type, which carries deadlines, cancellation signals, and other request-scoped values.</td></tr>
</table></body></html>`

func newFakeServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestDDG_ParsesLiteResults(t *testing.T) {
	results := parseDDGLite(fakeDDG, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !strings.Contains(results[0].Title, "Go select statement") {
		t.Fatalf("title parse: %q", results[0].Title)
	}
	if results[0].URL != "https://go.dev/ref/spec#Select_statements" {
		t.Fatalf("url parse: %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "select") {
		t.Fatalf("snippet parse: %q", results[0].Snippet)
	}
}

func TestSanitize_StripsHTMLAndCollapsesWhitespace(t *testing.T) {
	got := sanitizeText("  <b>Hello</b>  &amp;   world  ", 0)
	if got != "Hello & world" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_TruncatesWithEllipsis(t *testing.T) {
	got := sanitizeText(strings.Repeat("a", 200), 50)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis, got %q", got)
	}
	if len(got) > 60 {
		t.Fatalf("expected truncation, got len=%d", len(got))
	}
}

func TestSanitize_RemovesZeroWidthAndControl(t *testing.T) {
	hidden := "norm​al‮text\x00here"
	got := sanitizeText(hidden, 0)
	if got != "normaltexthere" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeSnippet_StripsURLsInBody(t *testing.T) {
	got := sanitizeSnippet("Visit https://evil.example/x for more info", 0)
	if strings.Contains(got, "evil.example") {
		t.Fatalf("URL not stripped: %q", got)
	}
	if !strings.Contains(got, "[link]") {
		t.Fatalf("expected [link] marker: %q", got)
	}
}

func TestSanitizeURL_RejectsNonHTTP(t *testing.T) {
	if got := sanitizeURL("javascript:alert(1)", 300); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := sanitizeURL("https://example.com/x", 300); got != "https://example.com/x" {
		t.Fatalf("expected pass-through, got %q", got)
	}
}

func TestCache_RespectsTTL(t *testing.T) {
	c := NewCache(20*time.Millisecond, 4)
	c.Put("ddg", 3, "Hello World", []Result{{Title: "t"}})
	if _, ok := c.Get("ddg", 3, "  hello   world "); !ok {
		t.Fatal("expected hit on normalized key")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("ddg", 3, "hello world"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestCache_KeyIncludesBackendAndK(t *testing.T) {
	c := NewCache(time.Minute, 8)
	c.Put("ddg", 3, "q", []Result{{Title: "a"}})
	if _, ok := c.Get("brave", 3, "q"); ok {
		t.Fatal("different backend should miss")
	}
	if _, ok := c.Get("ddg", 5, "q"); ok {
		t.Fatal("different k should miss")
	}
}

func TestCache_EvictsOldestOnCapacity(t *testing.T) {
	c := NewCache(time.Minute, 2)
	c.Put("ddg", 3, "a", []Result{{Title: "A"}})
	c.Put("ddg", 3, "b", []Result{{Title: "B"}})
	c.Put("ddg", 3, "c", []Result{{Title: "C"}})
	if _, ok := c.Get("ddg", 3, "a"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := c.Get("ddg", 3, "b"); !ok {
		t.Fatal("b should remain")
	}
	if _, ok := c.Get("ddg", 3, "c"); !ok {
		t.Fatal("c should remain")
	}
}

func TestDDGSearcher_RoundTripAgainstFakeServer(t *testing.T) {
	ts := newFakeServer(t, fakeDDG, 200)
	s := NewDDGSearcher()
	s.HTTP = ts.Client()
	/* override endpoint by wrapping the round-tripper to redirect to ts.URL */
	s.HTTP.Transport = rewriteRT{base: s.HTTP.Transport, target: ts.URL}
	results, err := s.Search(context.Background(), "go select", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if !strings.Contains(results[0].URL, "go.dev") {
		t.Fatalf("URL: %q", results[0].URL)
	}
}

func TestDDGSearcher_NoResultsErrorOnEmptyBody(t *testing.T) {
	ts := newFakeServer(t, "<html><body></body></html>", 200)
	s := NewDDGSearcher()
	s.HTTP = ts.Client()
	s.HTTP.Transport = rewriteRT{base: s.HTTP.Transport, target: ts.URL}
	_, err := s.Search(context.Background(), "anything", 3)
	if err == nil || err != ErrNoResults {
		t.Fatalf("expected ErrNoResults, got %v", err)
	}
}

/* rewriteRT redirects any outgoing request to a fixed target host, used to point ddg.go at httptest. */
type rewriteRT struct {
	base   http.RoundTripper
	target string
}

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.base == nil {
		r.base = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	if u, err := req.URL.Parse(r.target); err == nil {
		clone.URL.Scheme = u.Scheme
		clone.URL.Host = u.Host
		clone.URL.Path = u.Path
		clone.Host = u.Host
	}
	return r.base.RoundTrip(clone)
}
