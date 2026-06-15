package rag

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* idEmbedder is a minimal embedder identified only by its Identity string. */
type idEmbedder struct{ id string }

func (e idEmbedder) Identity() string { return e.id }
func (e idEmbedder) Dim() int         { return 4 }
func (e idEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}

func serviceWith(embedders map[string]llm.Embedder, corpusEmbedderID string) *Service {
	s := &Service{Index: NewIndex(), Embedders: embedders}
	if corpusEmbedderID != "" {
		s.Index.Replace(&Collection{Name: "docs", EmbedderID: corpusEmbedderID, Dim: 4})
	}
	return s
}

/*
TestPickEmbedder_CorpusMatchDisambiguates is the regression for the "ambiguous
embedder choice" warning: two providers are registered, but the loaded corpus
was built with only one — the query must use that one, not error.
*/
func TestPickEmbedder_CorpusMatchDisambiguates(t *testing.T) {
	ollama := idEmbedder{id: "ollama:nomic-embed-text"}
	openai := idEmbedder{id: "openai:text-embedding-3-small"}
	s := serviceWith(map[string]llm.Embedder{ollama.Identity(): ollama, openai.Identity(): openai}, ollama.Identity())

	e, err := s.pickEmbedder("", nil)
	if err != nil {
		t.Fatalf("pickEmbedder errored despite single-embedder corpus: %v", err)
	}
	if e.Identity() != ollama.Identity() {
		t.Errorf("picked %q, want corpus embedder %q", e.Identity(), ollama.Identity())
	}
}

/* Explicit id always wins. */
func TestPickEmbedder_ExplicitID(t *testing.T) {
	a := idEmbedder{id: "a"}
	b := idEmbedder{id: "b"}
	s := serviceWith(map[string]llm.Embedder{a.Identity(): a, b.Identity(): b}, a.Identity())
	e, err := s.pickEmbedder("b", nil)
	if err != nil || e.Identity() != "b" {
		t.Fatalf("explicit id: got (%v, %v), want b", embID(e), err)
	}
}

/* Single registered embedder needs no corpus to disambiguate. */
func TestPickEmbedder_SingleRegistered(t *testing.T) {
	a := idEmbedder{id: "only"}
	s := serviceWith(map[string]llm.Embedder{a.Identity(): a}, "")
	if e, err := s.pickEmbedder("", nil); err != nil || e.Identity() != "only" {
		t.Fatalf("single embedder: got (%v, %v), want only", embID(e), err)
	}
}

/* No corpus + multiple registered embedders is genuinely ambiguous and must error. */
func TestPickEmbedder_AmbiguousWithoutCorpus(t *testing.T) {
	a := idEmbedder{id: "a"}
	b := idEmbedder{id: "b"}
	s := serviceWith(map[string]llm.Embedder{a.Identity(): a, b.Identity(): b}, "")
	if _, err := s.pickEmbedder("", nil); err == nil {
		t.Fatal("expected ambiguous-embedder error when no corpus disambiguates")
	}
}

/*
TestSearch_EmptyCorpusIsNoOp is the regression for the observed warning: with no
collections loaded, Search must return cleanly without selecting an embedder,
even when several embedders are registered.
*/
func TestSearch_EmptyCorpusIsNoOp(t *testing.T) {
	a := idEmbedder{id: "a"}
	b := idEmbedder{id: "b"}
	s := serviceWith(map[string]llm.Embedder{a.Identity(): a, b.Identity(): b}, "")
	s.Router = DefaultTopicRouter()

	hits, err := s.Search(context.Background(), "how do I configure a postgres connection pool", SearchOpts{})
	if err != nil {
		t.Fatalf("empty-corpus Search should not error, got: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("empty-corpus Search should return no hits, got %d", len(hits))
	}
}

func embID(e llm.Embedder) string {
	if e == nil {
		return "<nil>"
	}
	return e.Identity()
}
