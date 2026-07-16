package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/rag"
)

type ragSearchEmbedder struct{}

func (ragSearchEmbedder) Identity() string { return "test:rag-search" }
func (ragSearchEmbedder) Dim() int         { return 2 }
func (ragSearchEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func TestHandleRAGSearch_RedactsVectorsAndSubjectsAndCapsK(t *testing.T) {
	embedder := ragSearchEmbedder{}
	svc, err := rag.NewService(t.TempDir(), map[string]llm.Embedder{embedder.Identity(): embedder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.Index.Replace(&rag.Collection{
		Name:       "test-docs",
		Kind:       rag.CollectionKindDocs,
		EmbedderID: embedder.Identity(),
		Dim:        embedder.Dim(),
		Chunks: []rag.Chunk{{
			ID:      "redaction-hit",
			Source:  "test.md",
			Text:    "server-redaction-canary",
			Subject: "must-not-cross-http",
			Vector:  []float32{1, 0},
		}},
	})

	server := &Server{rag: svc}
	req := httptest.NewRequest(http.MethodPost, "/v1/rag/search", bytes.NewBufferString(
		`{"query":"server-redaction-canary","k":100000}`,
	))
	recorder := httptest.NewRecorder()
	server.handleRAGSearch(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "server-redaction-canary") {
		t.Fatalf("test precondition: dense hit missing from response: %s", body)
	}
	if strings.Contains(body, `"vector"`) || strings.Contains(body, `"subject"`) || strings.Contains(body, "must-not-cross-http") {
		t.Fatalf("private search fields leaked over HTTP: %s", body)
	}
}
