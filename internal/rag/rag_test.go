package rag_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
)

/* fakeEmbedder returns a deterministic 4-dim vector derived from the input. */
type fakeEmbedder struct{ id string }

func (f fakeEmbedder) Identity() string { return f.id }
func (f fakeEmbedder) Dim() int         { return 4 }
func (f fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		for j, r := range t {
			v[j%4] += float32(r%17) / 16.0
		}
		out[i] = v
	}
	return out, nil
}

func TestChunker_SplitsByHeadings(t *testing.T) {
	doc := "# A\nfirst\n\n## B\nsecond\n\n## C\nthird"
	chunks := rag.SplitMarkdown("source.md", doc, rag.DefaultChunkOptions)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}
	headings := map[string]bool{}
	for _, c := range chunks {
		headings[c.Heading] = true
	}
	for _, h := range []string{"A", "B", "C"} {
		if !headings[h] {
			t.Fatalf("missing heading %q", h)
		}
	}
}

func TestNormalizeMarkdown_NormalizesLineEndings(t *testing.T) {
	in := []byte("\xEF\xBB\xBFhello\r\nworld\rextra")
	out := rag.NormalizeMarkdown(in)
	if got := string(out); got != "hello\nworld\nextra" {
		t.Fatalf("got %q", got)
	}
}

func TestTopicRouter_MatchesKeywords(t *testing.T) {
	r := rag.DefaultTopicRouter()
	cases := map[string][]string{
		"how do I make a Laravel Form Request?": {"laravel"},
		"explain PHP enums":                     {"php"},
		"nestjs guard for roles":                {"nestjs"},
		"tailwind flex gap responsive":          {"tailwind-css"},
		"explain hexagonal architecture":        {"architectural-patterns"},
		"how do I use native:install in NativePHP?": {"native-php"},
		"goroutine vs sync.Mutex in Go":             {"cs-fundamentals"},
		"PHP fibers vs pcntl_fork":                  {"cs-fundamentals"},
		"how do I use context.WithTimeout in Go?":   {"go-lang"},
		"net/http HandlerFunc pattern":              {"go-lang"},
	}
	for q, want := range cases {
		got := r.Route(q)
		ok := false
		for _, g := range got {
			for _, w := range want {
				if g == w {
					ok = true
				}
			}
		}
		if !ok {
			t.Fatalf("query %q routed to %v, expected one of %v", q, got, want)
		}
	}
}

func TestIngestSearchRoundtrip(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "# Laravel Form Request\n\nUse the rules() method to declare validation.\n\n## Routing\n\nPublic URLs must use kebab-case.\n"
	if err := os.WriteFile(filepath.Join(srcDir, "manual.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	embedder := fakeEmbedder{id: "fake:test"}
	svc, err := rag.NewService(filepath.Join(dir, "rag"), map[string]llm.Embedder{embedder.Identity(): embedder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	col, err := svc.Ingest(context.Background(), "laravel", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(col.Chunks) == 0 {
		t.Fatal("no chunks")
	}
	if col.EmbedderID != "fake:test" {
		t.Fatalf("embedder_id = %q", col.EmbedderID)
	}

	results, err := svc.Search(context.Background(), "Laravel form request validation", rag.SearchOpts{K: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no search results")
	}
}

func TestIngest_RejectsEmbedderMismatch(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "m.md"), []byte("# h\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e1 := fakeEmbedder{id: "fake:one"}
	e2 := fakeEmbedder{id: "fake:two"}
	svc, err := rag.NewService(filepath.Join(dir, "rag"), map[string]llm.Embedder{
		e1.Identity(): e1,
		e2.Identity(): e2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(context.Background(), "x", srcDir, e1, rag.DefaultChunkOptions); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Ingest(context.Background(), "x", srcDir, e2, rag.DefaultChunkOptions)
	if err == nil || !strings.Contains(err.Error(), "embedder mismatch") {
		t.Fatalf("expected embedder mismatch error, got %v", err)
	}
}

func TestAugment_NoOpWhenNoUserMessage(t *testing.T) {
	svc, err := rag.NewService(t.TempDir(), map[string]llm.Embedder{"fake:test": fakeEmbedder{id: "fake:test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.Augment(context.Background(), "", "", false); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
