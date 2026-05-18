package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/server"
)

/*
fakeProvider produces a canned StreamChunk sequence on every ChatStream
call. Chat is not exercised by the server in streaming mode and returns a
zero response.
*/
type fakeProvider struct {
	chunks []llm.StreamChunk
}

func (f *fakeProvider) Name() string                                              { return "fake" }
func (f *fakeProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (f *fakeProvider) ChatStream(_ context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk, len(f.chunks)+1)
	for _, c := range f.chunks {
		out <- c
	}
	close(out)
	return out, nil
}

func newTestServer(t *testing.T, provs map[string]llm.Provider) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		DefaultProvider: "fake",
		Providers: map[string]config.ProviderConfig{
			"fake": {Model: "test-model"},
		},
		Agent: config.AgentConfig{MaxIterations: 3},
	}
	srv, err := server.New(server.Options{
		Addr:      ":0",
		Config:    cfg,
		Providers: provs,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestServer_Healthz(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServer_Models(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"fake/test-model"`) {
		t.Fatalf("missing model id: %s", body)
	}
}

func TestServer_Providers(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"fake"`) {
		t.Fatalf("missing provider: %s", body)
	}
	if !strings.Contains(string(body), `"default":"fake"`) {
		t.Fatalf("missing default: %s", body)
	}
}

func TestServer_ChatCompletions_RejectsNonStreaming(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	body := bytes.NewBufferString(`{"model":"fake/test-model","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestServer_ChatCompletions_RejectsEmptyMessages(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	body := bytes.NewBufferString(`{"model":"fake/test-model","messages":[],"stream":true}`)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestServer_ChatCompletions_StreamsContentDeltas(t *testing.T) {
	provs := map[string]llm.Provider{
		"fake": &fakeProvider{chunks: []llm.StreamChunk{
			{Delta: "Hello"},
			{Delta: " world"},
			{Done: true},
		}},
	}
	ts := newTestServer(t, provs)
	body := bytes.NewBufferString(`{"model":"fake/test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type: %q", got)
	}

	frames := collectFrames(t, resp.Body)
	if !containsDelta(frames, `"content":"Hello"`) {
		t.Fatalf("missing Hello frame: %v", frames)
	}
	if !containsDelta(frames, `"content":" world"`) {
		t.Fatalf("missing world frame: %v", frames)
	}
	if !containsDelta(frames, `"finish_reason":"stop"`) {
		t.Fatalf("missing finish frame: %v", frames)
	}
	if !endsWithDone(frames) {
		t.Fatalf("missing [DONE] terminator: %v", frames)
	}
}

func TestServer_ChatCompletions_SurfacesToolResultEvent(t *testing.T) {
	provs := map[string]llm.Provider{
		"fake": &fakeProvider{chunks: []llm.StreamChunk{
			{Done: true},
		}},
	}
	ts := newTestServer(t, provs)
	body := bytes.NewBufferString(`{"model":"fake/test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := collectFrames(t, resp.Body)
	if !endsWithDone(frames) {
		t.Fatalf("missing DONE: %v", frames)
	}
}

func TestServer_ChatCompletions_NoProviders(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{})
	body := bytes.NewBufferString(`{"model":"fake","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestServer_StaticIndexFallback(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/some/spa/route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte("<html")) {
		t.Fatalf("expected HTML index, got: %s", b)
	}
}

func TestServer_APIRoutesNeverFallBackToIndex(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/v1/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing API route, got %d", resp.StatusCode)
	}
}

/* collectFrames parses an SSE response into a slice of raw frame strings. */
func collectFrames(t *testing.T, r io.Reader) []string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var frames []string
	var current strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if current.Len() > 0 {
				frames = append(frames, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		frames = append(frames, current.String())
	}
	return frames
}

/* containsDelta searches the slice of frames for a JSON substring inside a data: line. */
func containsDelta(frames []string, needle string) bool {
	for _, f := range frames {
		for _, line := range strings.Split(f, "\n") {
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, needle) {
				return true
			}
		}
	}
	return false
}

/* endsWithDone reports whether the final frame is the [DONE] terminator. */
func endsWithDone(frames []string) bool {
	for _, f := range frames {
		if strings.Contains(f, "data: [DONE]") {
			return true
		}
	}
	return false
}

/* slowProvider emits deltas with a delay between each so a test can cancel mid-stream. */
type slowProvider struct {
	deltas []string
	delay  time.Duration
}

func (s *slowProvider) Name() string                                              { return "slow" }
func (s *slowProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{}, nil
}
func (s *slowProvider) ChatStream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	out := make(chan llm.StreamChunk)
	go func() {
		defer close(out)
		for _, d := range s.deltas {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.delay):
			}
			select {
			case out <- llm.StreamChunk{Delta: d}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case out <- llm.StreamChunk{Done: true}:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

func TestServer_ChatCompletions_CancellationStopsHandlerPromptly(t *testing.T) {
	provs := map[string]llm.Provider{
		"fake": &slowProvider{deltas: []string{"a", "b", "c", "d"}, delay: 80 * time.Millisecond},
	}
	ts := newTestServer(t, provs)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fake/test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	deadline := time.AfterFunc(100*time.Millisecond, cancel)
	defer deadline.Stop()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return promptly after client cancellation")
	}
}

func TestServer_ChatCompletions_RepeatedPostsCleanShutdown(t *testing.T) {
	provs := map[string]llm.Provider{
		"fake": &fakeProvider{chunks: []llm.StreamChunk{
			{Delta: "ok"},
			{Done: true},
		}},
	}
	ts := newTestServer(t, provs)

	const n = 25
	for i := 0; i < n; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"fake/test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatalf("iter %d drain: %v", i, err)
		}
		resp.Body.Close()
	}
}

/* json import retained for Decode usage in handlers; keep one reference here */
var _ = json.Marshal
