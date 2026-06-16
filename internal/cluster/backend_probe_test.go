package cluster

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
fakeOpenAIServer stands in for an exo / llama-server / MLX-sidecar endpoint: it
answers GET /v1/models for probing and streams SSE chat deltas for chat.
*/
func fakeOpenAIServer(t *testing.T, deltas []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"test-model"}]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, d := range deltas {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", d)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})
	return httptest.NewServer(mux)
}

func TestHTTPBackendProbeAndChat(t *testing.T) {
	deltas := []string{"static ", "analysis ", "finding"}
	srv := fakeOpenAIServer(t, deltas)
	defer srv.Close()

	b := &httpBackend{
		name:    "test",
		baseURL: srv.URL + "/v1",
		logger:  discardLogger(),
		metrics: NewCollector(),
		http:    srv.Client(),
	}

	t.Run("it_probes_a_reachable_endpoint", func(t *testing.T) {
		ok, detail := b.probeEndpoint(context.Background())
		if !ok {
			t.Fatalf("probe failed: %s", detail)
		}
	})

	t.Run("it_reports_health_from_the_endpoint", func(t *testing.T) {
		h, err := b.Health(context.Background())
		if err != nil || !h.Healthy {
			t.Fatalf("health = %+v err=%v", h, err)
		}
	})

	t.Run("it_streams_chunks_and_records_metrics", func(t *testing.T) {
		var got strings.Builder
		err := b.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi there"}}},
			TokenStreamFunc(func(c llm.StreamChunk) { got.WriteString(c.Delta) }))
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
		if got.String() != strings.Join(deltas, "") {
			t.Fatalf("streamed %q, want %q", got.String(), strings.Join(deltas, ""))
		}
		snap := b.metrics.Snapshot()
		if snap.GeneratedTokens != len(deltas) {
			t.Errorf("generated tokens = %d, want %d", snap.GeneratedTokens, len(deltas))
		}
		if snap.Backend != "test" {
			t.Errorf("backend metric = %q, want test", snap.Backend)
		}
		if snap.PromptTokens == 0 {
			t.Error("prompt tokens estimate should be > 0")
		}
	})
}

func TestProbeReportsMissingBinaries(t *testing.T) {
	cfg := testConfig()
	/* Use binary names that will not exist on PATH in CI. */
	cfg.Runtime.Exo.Binary = "exo-not-installed-xyz"
	cfg.Runtime.Llama.Server = "llama-server-not-installed-xyz"

	exo := newExoBackend(cfg.Runtime, discardLogger(), NewCollector())
	caps, err := exo.Probe(context.Background())
	if err != nil {
		t.Fatalf("exo probe: %v", err)
	}
	if caps.Installed {
		t.Error("exo should report not installed when the binary is absent and no endpoint is set")
	}
	if caps.Diagnostic == "" {
		t.Error("exo probe should carry a diagnostic when not installed")
	}

	llama := newLlamaBackend(cfg.Runtime, discardLogger(), NewCollector())
	lcaps, err := llama.Probe(context.Background())
	if err != nil {
		t.Fatalf("llama probe: %v", err)
	}
	if lcaps.Installed {
		t.Error("llama should report not installed when llama-server is absent")
	}
}
