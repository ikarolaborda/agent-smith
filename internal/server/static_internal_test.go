package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandler_MissingAssetIs404NotIndexFallback(t *testing.T) {
	h := staticHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	/*
		A missing hashed asset must 404. Falling back to index.html (text/html)
		makes the browser reject the module script on a MIME mismatch and blanks
		the SPA, while masking a stale/incomplete embedded build behind a 200.
	*/
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/does-not-exist-abc123.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: status = %d, want 404; body starts %q", rec.Code, rec.Body.String()[:min(60, rec.Body.Len())])
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("missing asset must not be served as text/html: %q", rec.Header().Get("Content-Type"))
	}
}

func TestStaticHandler_IndexFallbackIsNoCache(t *testing.T) {
	h := staticHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	/* A client-side route falls back to index.html, which must be no-cache. */
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("spa route: status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("index.html fallback Cache-Control = %q, want no-cache", cc)
	}
}

func TestIsAssetPath(t *testing.T) {
	asset := []string{"/assets/index-abc.js", "/favicon.svg", "/x.css", "/f.woff2", "/a.map"}
	route := []string{"/", "/chat", "/some/route", "/settings"}
	for _, p := range asset {
		if !isAssetPath(p) {
			t.Errorf("isAssetPath(%q) = false, want true", p)
		}
	}
	for _, p := range route {
		if isAssetPath(p) {
			t.Errorf("isAssetPath(%q) = true, want false", p)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
