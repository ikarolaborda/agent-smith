package context7

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
newTestServer stands in for the Context7 API: it answers the search path with a
single library match and the context path with plain-text docs, recording how
many times each was hit so caching can be asserted.
*/
func newTestServer(t *testing.T, searchBody, contextBody string) (*httptest.Server, *int, *int) {
	t.Helper()
	var searchHits, contextHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, searchPath):
			searchHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(searchBody))
		case strings.HasSuffix(r.URL.Path, contextPath):
			contextHits++
			_, _ = w.Write([]byte(contextBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &searchHits, &contextHits
}

func TestLibraryDocs_TextResponse(t *testing.T) {
	srv, _, _ := newTestServer(t,
		`[{"id":"/vercel/next.js","title":"Next.js"}]`,
		"App Router uses the app/ directory.\nUse generateStaticParams for SSG.",
	)
	c := New("test-key", srv.URL)

	docs, err := c.LibraryDocs(context.Background(), "how do I use the next.js app router")
	if err != nil {
		t.Fatalf("LibraryDocs: %v", err)
	}
	if docs.Library != "/vercel/next.js" {
		t.Errorf("library = %q, want /vercel/next.js", docs.Library)
	}
	if !strings.Contains(docs.Text, "App Router") {
		t.Errorf("docs text missing content: %q", docs.Text)
	}
}

func TestLibraryDocs_WrappedSearchAndJSONContext(t *testing.T) {
	srv, _, _ := newTestServer(t,
		`{"results":[{"id":"/facebook/react","name":"React"}]}`,
		`[{"title":"Hooks","content":"useEffect runs after render."}]`,
	)
	c := New("test-key", srv.URL)

	docs, err := c.LibraryDocs(context.Background(), "react hooks cleanup")
	if err != nil {
		t.Fatalf("LibraryDocs: %v", err)
	}
	if docs.Library != "/facebook/react" {
		t.Errorf("library = %q, want /facebook/react", docs.Library)
	}
	if !strings.Contains(docs.Text, "useEffect runs after render") {
		t.Errorf("docs text missing joined snippet: %q", docs.Text)
	}
}

func TestLibraryDocs_NoLibraryMatch(t *testing.T) {
	srv, _, _ := newTestServer(t, `[]`, "unused")
	c := New("test-key", srv.URL)

	if _, err := c.LibraryDocs(context.Background(), "some unmatched query text"); err != ErrNoLibrary {
		t.Fatalf("err = %v, want ErrNoLibrary", err)
	}
}

func TestLibraryDocs_NoAPIKey(t *testing.T) {
	c := New("", DefaultBaseURL)
	if _, err := c.LibraryDocs(context.Background(), "anything technical here"); err != ErrNoAPIKey {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestLibraryDocs_CachesAcrossCalls(t *testing.T) {
	srv, searchHits, contextHits := newTestServer(t,
		`[{"id":"/vercel/next.js"}]`,
		"docs body",
	)
	c := New("test-key", srv.URL)

	q := "next.js routing question"
	if _, err := c.LibraryDocs(context.Background(), q); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.LibraryDocs(context.Background(), q); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if *searchHits != 1 || *contextHits != 1 {
		t.Errorf("expected 1 search + 1 context hit (cache), got search=%d context=%d", *searchHits, *contextHits)
	}
}

func TestCapText(t *testing.T) {
	if got := capText("  short  ", 100); got != "short" {
		t.Errorf("capText short = %q", got)
	}
	long := strings.Repeat("line one two three\n", 500)
	got := capText(long, MaxDocsBytes)
	if len(got) > MaxDocsBytes {
		t.Errorf("capText len = %d, want <= %d", len(got), MaxDocsBytes)
	}
	if strings.HasSuffix(got, "lin") {
		t.Errorf("capText should cut on newline, not mid-word: %q", got[len(got)-10:])
	}
}

func TestJoinSnippets(t *testing.T) {
	got := joinSnippets([]byte(`[{"title":"A","content":"alpha"},{"content":"beta"}]`))
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Errorf("joinSnippets missing content: %q", got)
	}
	if got := joinSnippets([]byte(`not json`)); got != "" {
		t.Errorf("joinSnippets(non-json) = %q, want empty", got)
	}
}
