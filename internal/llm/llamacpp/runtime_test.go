package llamacpp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

/*
TestMain doubles as a fake llama-server: when LLAMACPP_FAKE is set the test
binary re-executes into a tiny HTTP server instead of running the suite, which
lets the runtime supervisor be tested against a real child process with no
external llama.cpp dependency (the standard "exec self" pattern).
*/
func TestMain(m *testing.M) {
	if mode := os.Getenv("LLAMACPP_FAKE"); mode != "" {
		fakeServerMain(mode)
		return
	}
	os.Exit(m.Run())
}

/* fakeServerMain parses --port from argv and serves /health per the mode. */
func fakeServerMain(mode string) {
	if mode == "crash" {
		os.Exit(1)
	}
	args := os.Args
	port := ""
	apiKey := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--port" {
			port = args[i+1]
		}
		if args[i] == "--api-key-file" {
			raw, err := os.ReadFile(args[i+1])
			if err == nil {
				apiKey = strings.TrimSpace(string(raw))
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if mode == "unready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if apiKey == "" || r.Header.Get("Authorization") != "Bearer "+apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"id": "fake-model", "object": "model"}},
		})
	})
	if mode == "exit-after-ready" {
		go func() {
			time.Sleep(1200 * time.Millisecond)
			os.Exit(7)
		}()
	}
	_ = http.ListenAndServe(net.JoinHostPort("127.0.0.1", port), mux)
	os.Exit(0)
}

func startFake(t *testing.T, mode string, timeout time.Duration) *Runtime {
	t.Helper()
	t.Setenv("LLAMACPP_FAKE", mode)
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("GGUF-test-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(RuntimeConfig{
		Binary:           os.Args[0],
		ModelPath:        model,
		Profiler:         ampleProfiler(),
		StartupTimeout:   timeout,
		AdmissionLockDir: t.TempDir(),
	})
	return rt
}

func it_starts_and_stops_the_server(t *testing.T) {
	rt := startFake(t, "ok", 10*time.Second)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := rt.BaseURL()
	if base == "" {
		t.Fatal("BaseURL empty after Start")
	}

	/* The endpoint answers while running. */
	health := base[:len(base)-len("/v1")] + "/health"
	resp, err := http.Get(health)
	if err != nil {
		t.Fatalf("health GET: %v", err)
	}
	_ = resp.Body.Close()

	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	/* After Close the port should stop accepting within a short grace. */
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get(health); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("server still reachable after Close")
}

func TestRuntimeStartStop(t *testing.T) { it_starts_and_stops_the_server(t) }

func it_fails_fast_when_child_crashes(t *testing.T) {
	rt := startFake(t, "crash", 10*time.Second)
	err := rt.Start(context.Background())
	if err == nil {
		_ = rt.Close(context.Background())
		t.Fatal("expected Start to fail when the server exits before ready")
	}
}

func TestRuntimeCrash(t *testing.T) { it_fails_fast_when_child_crashes(t) }

func it_times_out_when_never_ready(t *testing.T) {
	rt := startFake(t, "unready", 1200*time.Millisecond)
	err := rt.Start(context.Background())
	if err == nil {
		_ = rt.Close(context.Background())
		t.Fatal("expected Start to time out when /health never returns 200")
	}
}

func TestRuntimeReadinessTimeout(t *testing.T) { it_times_out_when_never_ready(t) }

func TestRuntimeConcurrentStartIsIdempotent(t *testing.T) {
	rt := startFake(t, "ok", 10*time.Second)
	const callers = 6
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { errs <- rt.Start(context.Background()) }()
	}
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Start: %v", err)
		}
	}
	if rt.State() != RuntimeReady {
		t.Fatalf("state = %s", rt.State())
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDetectsExitAfterReady(t *testing.T) {
	rt := startFake(t, "exit-after-ready", 10*time.Second)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Wait(ctx); err == nil {
		t.Fatal("expected child exit error")
	}
	if rt.State() != RuntimeFailed || rt.BaseURL() != "" || rt.Err() == nil {
		t.Fatalf("post-exit state=%s base=%q err=%v", rt.State(), rt.BaseURL(), rt.Err())
	}
}

func it_reports_a_free_port(t *testing.T) {
	p, err := freePort("127.0.0.1")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if p <= 0 {
		t.Fatalf("freePort returned %d", p)
	}
	/* The port must be bindable right after selection. */
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
	if err != nil {
		t.Fatalf("port %d not bindable: %v", p, err)
	}
	_ = l.Close()
}

func TestFreePort(t *testing.T) { it_reports_a_free_port(t) }

func TestLocalSplitPreflightRequiresEveryShard(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "model-Q4_K_M-00001-of-00002.gguf")
	if err := os.WriteFile(first, []byte("GGUF-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InspectLocal(context.Background(), ampleProfiler(), LocalPreflightRequest{ModelFiles: []string{first}})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete split rejection, got %v", err)
	}
	second := filepath.Join(dir, "model-Q4_K_M-00002-of-00002.gguf")
	if err := os.WriteFile(second, []byte("GGUF-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := InspectLocal(context.Background(), ampleProfiler(), LocalPreflightRequest{ModelFiles: []string{first}})
	if err != nil || !report.Fits || report.ModelBytes != uint64(len("GGUF-one")+len("GGUF-two")) {
		t.Fatalf("complete split report=%+v err=%v", report, err)
	}
}

func TestProtectedArgsAndEnvironment(t *testing.T) {
	for _, args := range [][]string{{"--model", "evil.gguf"}, {"--host=0.0.0.0"}, {"-ngl", "999"}, {"--tools", "all"}, {"--cache-ram=8192"}, {"--api-key-file", "/tmp/key"}} {
		if err := validateExtraArgs(args); err == nil {
			t.Fatalf("expected protected argument rejection for %v", args)
		}
	}
	env := sanitizedLlamaEnvironment([]string{
		"PATH=/bin", "LLAMA_ARG_MODEL=evil", "LLAMA_ARG_HOST=0.0.0.0",
		"HF_TOKEN=secret", "OPENAI_API_KEY=secret", "AWS_SECRET_ACCESS_KEY=secret",
	})
	if len(env) != 1 || env[0] != "PATH=/bin" {
		t.Fatalf("sanitized environment = %v", env)
	}
}

func TestRuntimeAlwaysPassesAdmittedDefaults(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{ModelPath: "/tmp/model.gguf"})
	rt.apiKey = strings.Repeat("k", 32)
	rt.apiKeyFile = "/private/key-file"
	args := strings.Join(rt.buildArgs("/tmp/model.gguf", "127.0.0.1", 8080), " ")
	if !strings.Contains(args, "--ctx-size 4096") || !strings.Contains(args, "--parallel 1") {
		t.Fatalf("runtime omitted admitted defaults: %s", args)
	}
	for _, required := range []string{"--api-key-file /private/key-file", "--cache-ram 0", "--batch-size 512", "--ubatch-size 128", "--offline", "--no-ui", "--no-slots", "-ngl 0"} {
		if !strings.Contains(args, required) {
			t.Fatalf("runtime omitted protected argument %q: %s", required, args)
		}
	}
}

func TestRuntimeAPIKeyFileIsPrivate(t *testing.T) {
	path, err := createRuntimeAPIKeyFile(t.TempDir(), strings.Repeat("x", 32))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("API-key file mode = %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != strings.Repeat("x", 32) {
		t.Fatalf("API-key file content invalid: %q, err=%v", raw, err)
	}
}

func TestRuntimeAPIKeyValidationAndGeneration(t *testing.T) {
	generated, err := runtimeAPIKey("")
	if err != nil || len(generated) < 40 {
		t.Fatalf("generated key = %q, err=%v", generated, err)
	}
	configured := strings.Repeat("x", 32)
	got, err := runtimeAPIKey(configured)
	if err != nil || got != configured {
		t.Fatalf("configured key = %q, err=%v", got, err)
	}
	for _, bad := range []string{"short", strings.Repeat("x", 24) + ",second", strings.Repeat("x", 24) + "\n"} {
		if _, err := runtimeAPIKey(bad); err == nil {
			t.Fatalf("unsafe API key %q accepted", bad)
		}
	}
}

func TestRuntimeConfigRejectsAmbiguousOrNegativeResources(t *testing.T) {
	ref := Ref{Repo: "u/n"}
	for _, cfg := range []RuntimeConfig{
		{ModelPath: "/tmp/model.gguf", Ref: &ref, CtxSize: 4096, Parallel: 1},
		{ModelPath: "/tmp/model.gguf", CtxSize: -1, Parallel: 1},
		{ModelPath: "/tmp/model.gguf", CtxSize: 4096, Parallel: -1},
		{ModelPath: "/tmp/model.gguf", CtxSize: 4096, Parallel: 1, Port: 70000},
	} {
		if err := validateRuntimeConfig(cfg); err == nil {
			t.Fatalf("expected unsafe config rejection: %+v", cfg)
		}
	}
}

func TestRuntimeAdmissionLockSerializesChildren(t *testing.T) {
	t.Setenv("LLAMACPP_FAKE", "ok")
	dir := t.TempDir()
	lockDir := t.TempDir()
	model := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(model, []byte("GGUF-test-model"), 0o600); err != nil {
		t.Fatal(err)
	}
	newRuntime := func() *Runtime {
		return NewRuntime(RuntimeConfig{
			Binary: os.Args[0], ModelPath: model, Profiler: ampleProfiler(), StartupTimeout: 10 * time.Second,
			AdmissionLockDir: lockDir,
		})
	}
	first := newRuntime()
	if err := first.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())

	second := newRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	err := second.Start(ctx)
	cancel()
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("second runtime bypassed admission lock: %v", err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("second runtime could not start after lock release: %v", err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
