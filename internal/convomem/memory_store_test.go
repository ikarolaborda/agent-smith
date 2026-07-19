package convomem

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestMemStore(t *testing.T) *MemoryStore {
	t.Helper()
	m, err := OpenMemoryStore(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestMemoryStore_PutCandidatesDeleteSubjectScoped(t *testing.T) {
	m := newTestMemStore(t)
	ctx := context.Background()

	put := func(id, subj, text string) {
		if err := m.Put(ctx, MemoryRecord{ID: id, Subject: subj, Kind: "project_fact", Text: text, Vector: []float32{1, 0, 0}, Importance: 0.4, CreatedAt: "2026-01-01T00:00:00Z", LastAccessed: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	put("a1", "alice", "alice fact one")
	put("a2", "alice", "alice fact two")
	put("b1", "bob", "bob fact")

	alice, err := m.Candidates(ctx, "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 2 {
		t.Fatalf("alice candidates = %d, want 2", len(alice))
	}
	for _, r := range alice {
		if r.Subject != "alice" {
			t.Errorf("cross-profile leak: got subject %q", r.Subject)
		}
		if len(r.Vector) != 3 {
			t.Errorf("vector not round-tripped: %v", r.Vector)
		}
	}

	// Delete is subject-scoped: bob cannot delete alice's row.
	ok, err := m.Delete(ctx, "bob", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("bob deleted alice's memory — subject scoping broken")
	}
	ok, _ = m.Delete(ctx, "alice", "a1")
	if !ok {
		t.Error("owner delete did not remove the row")
	}
	if got, _ := m.Count(ctx); got != 2 {
		t.Errorf("count after delete = %d, want 2", got)
	}
}

func TestMemoryStore_EmbedderPinningFailsClosed(t *testing.T) {
	m := newTestMemStore(t)
	ctx := context.Background()
	if _, _, ok, _ := m.Embedder(ctx); ok {
		t.Fatal("fresh store should have no embedder pinned")
	}
	if err := m.SetEmbedder(ctx, "openai:text-embedding-3-small", 1536); err != nil {
		t.Fatal(err)
	}
	// Same identity re-agrees.
	if err := m.SetEmbedder(ctx, "openai:text-embedding-3-small", 1536); err != nil {
		t.Errorf("re-agreement should succeed: %v", err)
	}
	// A different embedder or dim must fail closed.
	if err := m.SetEmbedder(ctx, "ollama:nomic-embed-text", 768); err == nil {
		t.Error("expected embedder-mismatch error")
	}
	id, dim, ok, err := m.Embedder(ctx)
	if err != nil || !ok || id != "openai:text-embedding-3-small" || dim != 1536 {
		t.Errorf("Embedder() = %q/%d ok=%v err=%v", id, dim, ok, err)
	}
}
