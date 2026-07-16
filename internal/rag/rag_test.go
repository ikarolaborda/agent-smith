package rag_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
)

type dynamicDimEmbedder struct {
	id string

	mu    sync.Mutex
	dim   int
	calls int
}

func (e *dynamicDimEmbedder) Identity() string { return e.id }
func (e *dynamicDimEmbedder) Dim() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dim
}
func (e *dynamicDimEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls += len(texts)
	if len(texts) > 0 && e.dim == 0 {
		e.dim = 3
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0}
	}
	return out, nil
}
func (e *dynamicDimEmbedder) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

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
		"how do I make a Laravel Form Request?":                     {"laravel"},
		"explain PHP enums":                                         {"php"},
		"nestjs guard for roles":                                    {"nestjs"},
		"tailwind flex gap responsive":                              {"tailwind-css"},
		"explain hexagonal architecture":                            {"architectural-patterns"},
		"how do I use native:install in NativePHP?":                 {"native-php"},
		"goroutine vs sync.Mutex in Go":                             {"cs-fundamentals"},
		"PHP fibers vs pcntl_fork":                                  {"cs-fundamentals"},
		"how do I use context.WithTimeout in Go?":                   {"go-lang"},
		"net/http HandlerFunc pattern":                              {"go-lang"},
		"refactor this OOP code using SOLID and Clean Architecture": {"software-engineering"},
		"explain TCP/IP DNS and TLS routing":                        {"computer-networks"},
		"threat model this CVE against OWASP exploitation":          {"cybersecurity"},
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

func TestChunker_UTF8SafeHardSplitAndOverlap(t *testing.T) {
	doc := "# Unicode\né🙂漢字é🙂漢"
	chunks := rag.SplitMarkdown("unicode.md", doc, rag.ChunkOptions{MaxChars: 2, OverlapChars: 1})
	if len(chunks) < 3 {
		t.Fatalf("expected several hard-split chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk.Text) {
			t.Fatalf("chunk %d contains invalid UTF-8: %q", i, chunk.Text)
		}
		maxRunes := 2
		if i < len(chunks)-1 {
			maxRunes += 3 /* two overlap separators plus one overlap rune */
		}
		if got := utf8.RuneCountInString(chunk.Text); got > maxRunes {
			t.Fatalf("chunk %d has %d runes, want <= %d: %q", i, got, maxRunes, chunk.Text)
		}
	}
}

func TestChunker_IDIncludesFinalOverlap(t *testing.T) {
	left := rag.SplitMarkdown("same.md", "# H\nabcXYZ", rag.ChunkOptions{MaxChars: 3, OverlapChars: 2})
	right := rag.SplitMarkdown("same.md", "# H\nabcUVZ", rag.ChunkOptions{MaxChars: 3, OverlapChars: 2})
	if len(left) != 2 || len(right) != 2 {
		t.Fatalf("test precondition: chunk counts = %d and %d, want 2", len(left), len(right))
	}
	if left[0].ID == right[0].ID {
		t.Fatalf("first chunk IDs are equal even though final overlap differs: %q vs %q", left[0].Text, right[0].Text)
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

func TestStoreAndIngestRejectUnsafeCollectionNames(t *testing.T) {
	store, err := rag.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unsafe := []string{"", ".hidden", "..", "../escape", "a..b", "a/b", `a\\b`, "name with space"}
	for _, name := range unsafe {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Path(name); err == nil {
				t.Fatalf("Path accepted unsafe collection %q", name)
			}
			if _, err := store.Load(name); err == nil {
				t.Fatalf("Load accepted unsafe collection %q", name)
			}
			if err := store.Save(&rag.Collection{Name: name}); err == nil {
				t.Fatalf("Save accepted unsafe collection %q", name)
			}
		})
	}

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "manual.md"), []byte("# Safe\ntext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	embedder := fakeEmbedder{id: "fake:test"}
	svc, err := rag.NewService(t.TempDir(), map[string]llm.Embedder{embedder.Identity(): embedder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ingest(context.Background(), "../escape", srcDir, embedder, rag.DefaultChunkOptions); err == nil {
		t.Fatal("Ingest accepted traversal collection name")
	}
	if _, err := store.Path("php-modern_8.3"); err != nil {
		t.Fatalf("safe collection slug rejected: %v", err)
	}
}

func TestStoreLoadRejectsMismatchedDecodedCollectionName(t *testing.T) {
	store, err := rag.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Path("safe-name")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"name":"injected-name","embedder_id":"fake","dim":1,"chunks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("safe-name"); err == nil || !strings.Contains(err.Error(), "mismatched name") {
		t.Fatalf("expected decoded-name mismatch, got %v", err)
	}
}

func TestIngest_ReplacesSnapshotAndDropsDeletedSources(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(srcDir, "keep.md")
	deletedPath := filepath.Join(srcDir, "deleted.md")
	if err := os.WriteFile(keepPath, []byte("# Keep\ncurrent snapshot fact\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deletedPath, []byte("# Delete\nstale fact must disappear\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	embedder := fakeEmbedder{id: "fake:test"}
	svc, err := rag.NewService(filepath.Join(dir, "rag"), map[string]llm.Embedder{embedder.Identity(): embedder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Ingest(context.Background(), "snapshot", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Chunks) != 2 {
		t.Fatalf("initial chunks = %d, want 2", len(first.Chunks))
	}

	if err := os.Remove(deletedPath); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Ingest(context.Background(), "snapshot", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Chunks) != 1 || second.Chunks[0].Source != "keep.md" {
		t.Fatalf("re-ingested snapshot retained deleted source: %#v", second.Chunks)
	}
	if strings.Contains(second.Chunks[0].Text, "stale fact") {
		t.Fatalf("deleted content survived snapshot replacement: %q", second.Chunks[0].Text)
	}

	/* Deleting the final Markdown file intentionally clears the collection. */
	if err := os.Remove(keepPath); err != nil {
		t.Fatal(err)
	}
	empty, err := svc.Ingest(context.Background(), "snapshot", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Chunks) != 0 {
		t.Fatalf("empty source snapshot retained %d chunks", len(empty.Chunks))
	}
}

func TestIngest_EmptyDynamicDimensionSnapshotCanLaterBePopulated(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	embedder := &dynamicDimEmbedder{id: "dynamic:test"}
	svc, err := rag.NewService(filepath.Join(dir, "rag"), map[string]llm.Embedder{embedder.Identity(): embedder}, nil)
	if err != nil {
		t.Fatal(err)
	}

	empty, err := svc.Ingest(context.Background(), "dynamic-empty", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Dim != 0 || len(empty.Chunks) != 0 {
		t.Fatalf("empty collection = dim %d, chunks %d", empty.Dim, len(empty.Chunks))
	}
	hits, err := svc.Search(context.Background(), "anything", rag.SearchOpts{
		Filter: []string{"dynamic-empty"},
		K:      1,
	})
	if err != nil {
		t.Fatalf("search empty collection: %v", err)
	}
	if len(hits) != 0 || embedder.callCount() != 0 {
		t.Fatalf("empty collection search hits=%d embed_calls=%d, want zero", len(hits), embedder.callCount())
	}

	if err := os.WriteFile(filepath.Join(srcDir, "manual.md"), []byte("# Dynamic\nnow populated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	populated, err := svc.Ingest(context.Background(), "dynamic-empty", srcDir, embedder, rag.DefaultChunkOptions)
	if err != nil {
		t.Fatalf("populate zero-dimension snapshot: %v", err)
	}
	if populated.Dim != 3 || len(populated.Chunks) != 1 {
		t.Fatalf("populated collection = dim %d, chunks %d", populated.Dim, len(populated.Chunks))
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
