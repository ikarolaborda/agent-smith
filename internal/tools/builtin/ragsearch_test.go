package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/rag"
)

type fakeSearcher struct {
	gotQuery  string
	gotFilter []string
	results   []rag.SearchResult
	err       error
}

func (f *fakeSearcher) Search(_ context.Context, query string, opts rag.SearchOpts) ([]rag.SearchResult, error) {
	f.gotQuery = query
	f.gotFilter = opts.Filter
	return f.results, f.err
}

func result(id, source, heading, text string, score float32) rag.SearchResult {
	return rag.SearchResult{
		Collection: "docs",
		Chunk:      rag.Chunk{ID: id, Source: source, Heading: heading, Text: text},
		Score:      score,
	}
}

func TestRAGSearchToolCitesUntrustedEvidence(t *testing.T) {
	fake := &fakeSearcher{results: []rag.SearchResult{
		result("c1", "manual.md", "Auth", "Use bearer tokens.", 0.72),
		result("c2", "manual.md", "", "Rotate keys often.", 0.61),
	}}
	tool := NewRAGSearchTool(fake)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"how does auth work","collection":"docs"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fake.gotQuery != "how does auth work" {
		t.Errorf("query not passed through: %q", fake.gotQuery)
	}
	if len(fake.gotFilter) != 1 || fake.gotFilter[0] != "docs" {
		t.Errorf("collection not forwarded as filter: %v", fake.gotFilter)
	}
	/* Trust boundary must be explicit in the rendered evidence. */
	if !strings.Contains(strings.ToUpper(out), "UNTRUSTED") {
		t.Errorf("evidence must be labelled untrusted:\n%s", out)
	}
	/* Chunk ids must be citable. */
	if !strings.Contains(out, "chunk=c1") || !strings.Contains(out, "chunk=c2") {
		t.Errorf("chunk ids missing from output:\n%s", out)
	}
	if !strings.Contains(out, "Use bearer tokens.") {
		t.Errorf("excerpt missing:\n%s", out)
	}
}

func TestRAGSearchToolEmptyQueryRejected(t *testing.T) {
	tool := NewRAGSearchTool(&fakeSearcher{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("expected an error for an empty query")
	}
}

func TestRAGSearchToolNoResults(t *testing.T) {
	tool := NewRAGSearchTool(&fakeSearcher{results: nil})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"nothing here"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(out), "no passages found") {
		t.Errorf("expected a no-results message, got: %s", out)
	}
}

func TestRAGSearchToolTruncatesLongExcerpt(t *testing.T) {
	long := strings.Repeat("x", defaultRAGExcerptBytes+200)
	tool := NewRAGSearchTool(&fakeSearcher{results: []rag.SearchResult{result("c1", "s", "h", long, 0.5)}})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("long excerpt should be truncated with an ellipsis")
	}
	if strings.Count(out, "x") > defaultRAGExcerptBytes {
		t.Errorf("excerpt exceeded the byte cap")
	}
}
