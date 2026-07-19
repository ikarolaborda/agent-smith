package convomem

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

/*
fakeEmbedder maps text to a bag-of-words vector over a fixed vocabulary, so texts
sharing words are close in cosine — enough to exercise semantic recall
deterministically and offline.
*/
type fakeEmbedder struct{ vocab []string }

func newFakeEmbedder() *fakeEmbedder {
	return &fakeEmbedder{vocab: []string{"vault", "code", "zebra", "weather", "sunny", "parser", "refactor", "bug"}}
}

func (f *fakeEmbedder) Identity() string { return "fake:bow" }
func (f *fakeEmbedder) Dim() int         { return len(f.vocab) }

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		words := strings.Fields(strings.ToLower(t))
		vec := make([]float32, len(f.vocab))
		for j, term := range f.vocab {
			for _, w := range words {
				if w == term {
					vec[j]++
				}
			}
		}
		out[i] = vec
	}
	return out, nil
}

func newTestStore(t *testing.T, dim int) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mem.sqlite")
	s, err := Open(path, dim)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_AddRejectsWrongDim(t *testing.T) {
	s := newTestStore(t, 4)
	if _, err := s.Add(context.Background(), "p", "user", "x", []float32{1, 2, 3}); err == nil {
		t.Error("expected dim-mismatch error")
	}
}

func TestMemory_RecallRanksSemanticMatchFirst(t *testing.T) {
	emb := newFakeEmbedder()
	m := &Memory{Store: newTestStore(t, emb.Dim()), Embedder: emb}
	ctx := context.Background()

	if err := m.Remember(ctx, "alice", "user", "the vault access code is zebra"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remember(ctx, "alice", "user", "the weather today is sunny"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remember(ctx, "alice", "user", "please refactor the parser bug"); err != nil {
		t.Fatal(err)
	}

	got, err := m.Recall(ctx, "alice", "what was the vault code", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected recall hits")
	}
	if !strings.Contains(got[0].Content, "vault access code") {
		t.Errorf("top recall = %q, want the vault turn", got[0].Content)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Errorf("results not sorted by score: %v", got)
		}
	}
}

func TestMemory_RecallIsProfileScoped(t *testing.T) {
	emb := newFakeEmbedder()
	m := &Memory{Store: newTestStore(t, emb.Dim()), Embedder: emb}
	ctx := context.Background()
	_ = m.Remember(ctx, "alice", "user", "the vault code is zebra")
	_ = m.Remember(ctx, "bob", "user", "the parser has a bug")

	got, err := m.Recall(ctx, "bob", "vault code", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, tn := range got {
		if strings.Contains(tn.Content, "vault") {
			t.Errorf("bob's recall leaked alice's turn: %q", tn.Content)
		}
	}
}

func TestMemory_RecallBlockRendersOrEmpty(t *testing.T) {
	emb := newFakeEmbedder()
	m := &Memory{Store: newTestStore(t, emb.Dim()), Embedder: emb}
	ctx := context.Background()
	_ = m.Remember(ctx, "alice", "user", "the vault access code is zebra")

	block := m.RecallBlock(ctx, "alice", "vault code", 3, 0.1)
	if !strings.Contains(block, "Relevant earlier conversation") || !strings.Contains(block, "vault access code") {
		t.Errorf("expected a rendered memory block, got: %q", block)
	}
	// An off-vocabulary query has zero cosine everywhere → filtered out.
	if empty := m.RecallBlock(ctx, "alice", "quaternion topology", 3, 0.1); empty != "" {
		t.Errorf("expected empty block for irrelevant query, got: %q", empty)
	}
}
