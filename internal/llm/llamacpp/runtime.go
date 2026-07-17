/*
runtime.go owns one llama-server process. Model acquisition and both preflight
checks happen before exec; process state is serialized so Start and Close
cannot create duplicate or orphaned children.
*/
package llamacpp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultBinary = "llama-server"
const defaultStartupTimeout = 300 * time.Second

const (
	boundedBatchSize  = 512
	boundedUBatchSize = 128
)

type RuntimeConfig struct {
	Binary         string
	ModelPath      string
	MMProjPath     string
	Ref            *Ref
	Downloader     *Downloader
	Profiler       Profiler
	FitPolicy      FitPolicy
	Host           string
	Port           int
	CtxSize        int
	Parallel       int
	GPULayers      int
	Jinja          bool
	ExtraArgs      []string
	StartupTimeout time.Duration
	/* APIKey is optional; an ephemeral high-entropy key is generated when empty. */
	APIKey string
	/* AdmissionLockDir overrides the per-user runtime lock directory (primarily for tests). */
	AdmissionLockDir string
	Logger           *slog.Logger
}

// RuntimeState is observable by a control plane without exposing exec.Cmd.
type RuntimeState string

const (
	RuntimeStopped   RuntimeState = "stopped"
	RuntimePreparing RuntimeState = "preparing"
	RuntimeStarting  RuntimeState = "starting"
	RuntimeReady     RuntimeState = "ready"
	RuntimeStopping  RuntimeState = "stopping"
	RuntimeFailed    RuntimeState = "failed"
)

type Runtime struct {
	cfg    RuntimeConfig
	logger *slog.Logger

	startMu sync.Mutex
	closeMu sync.Mutex
	mu      sync.RWMutex

	cmd         *exec.Cmd
	baseURL     string
	apiKey      string
	apiKeyFile  string
	state       RuntimeState
	started     bool
	closing     bool
	vision      bool
	waitDone    chan struct{}
	waitErr     error
	lastErr     error
	startCancel context.CancelFunc
	admission   *fileLock
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	/*
		Always pass the values that were admitted. Omitting --ctx-size or
		--parallel would delegate to version/model-dependent llama.cpp defaults
		and could allocate more KV memory than the fit report reserved.
	*/
	if cfg.CtxSize == 0 {
		cfg.CtxSize = defaultContextTokens
	}
	if cfg.Parallel == 0 {
		cfg.Parallel = defaultParallelRequests
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	return &Runtime{cfg: cfg, logger: cfg.Logger, state: RuntimeStopped}
}

func (r *Runtime) Start(ctx context.Context) error {
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	if r.state == RuntimeReady && r.started {
		r.mu.Unlock()
		return nil
	}
	if r.closing {
		r.mu.Unlock()
		return errors.New("llamacpp: runtime is closing")
	}
	startCtx, cancel := context.WithCancel(ctx)
	r.startCancel = cancel
	r.state = RuntimePreparing
	r.started = false
	r.lastErr = nil
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.startCancel = nil
		r.mu.Unlock()
	}()

	if err := validateRuntimeConfig(r.cfg); err != nil {
		return r.failStart(err)
	}
	host := r.cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if err := validateLoopbackHost(host); err != nil {
		return r.failStart(err)
	}
	apiKey, err := runtimeAPIKey(r.cfg.APIKey)
	if err != nil {
		return r.failStart(err)
	}
	r.mu.Lock()
	r.apiKey = apiKey
	r.mu.Unlock()
	binary := r.cfg.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return r.failStart(fmt.Errorf("llamacpp: %q not found on PATH (install llama.cpp / build llama-server): %w", binary, err))
	}

	local, err := r.resolveArtifacts(startCtx)
	if err != nil {
		return r.failStart(err)
	}
	lockDir, err := r.admissionLockDirectory(local)
	if err != nil {
		return r.failStart(err)
	}
	if err := ensureLockDirectory(lockDir, true); err != nil {
		return r.failStart(fmt.Errorf("llamacpp: prepare runtime admission-lock directory: %w", err))
	}
	admission, err := acquireLockInDirectory(startCtx, lockDir, ".agent-smith-runtime.lock")
	if err != nil {
		return r.failStart(fmt.Errorf("llamacpp: acquire global runtime admission lock: %w", err))
	}
	admissionOwned := true
	defer func() {
		if admissionOwned {
			_ = admission.Close()
		}
	}()
	apiKeyFile, err := createRuntimeAPIKeyFile(lockDir, apiKey)
	if err != nil {
		return r.failStart(err)
	}
	defer func() {
		if removeErr := os.Remove(apiKeyFile); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			r.logger.Warn("llamacpp: remove ephemeral API-key file", "err", removeErr)
		}
		r.mu.Lock()
		if r.apiKeyFile == apiKeyFile {
			r.apiKeyFile = ""
		}
		r.mu.Unlock()
	}()
	r.mu.Lock()
	r.apiKeyFile = apiKeyFile
	r.mu.Unlock()
	if err := verifyLocalArtifactsAgainstManifest(local); err != nil {
		return r.failStart(fmt.Errorf("llamacpp: final remote artifact verification failed: %w", err))
	}
	// Re-inspect committed local files and current host availability immediately
	// before exec. This catches pressure changes during a long download and also
	// protects caller-supplied model paths.
	report, err := InspectLocal(startCtx, r.runtimeProfiler(), LocalPreflightRequest{
		ModelFiles:    local.ModelFiles,
		MMProjPath:    local.MMProj,
		ContextTokens: r.cfg.CtxSize,
		Parallel:      r.cfg.Parallel,
		GPULayers:     r.cfg.GPULayers,
		FitPolicy:     r.cfg.FitPolicy,
	})
	if err != nil {
		return r.failStart(err)
	}
	/*
		Discrete-GPU offload is now admitted by the fit gate against detected VRAM
		(see fit.go), so a requested offload either passes the report or is
		rejected with a VRAM reason — no blanket non-Apple refusal. A GPU layer
		count with no detected accelerator still runs (llama.cpp clamps -ngl and
		falls back to CPU), but we surface it so the operator can tell.
	*/
	if !report.Fits {
		return r.failStart(&FitError{Report: report})
	}
	if r.cfg.GPULayers > 0 && !report.Host.GPU.HasUsableGPU() && !report.Host.AppleUnifiedMemory {
		r.logger.Warn("llamacpp: gpu_layers requested but no accelerator with known VRAM was detected; llama.cpp will fall back to CPU for unplaced layers",
			"gpu_layers", r.cfg.GPULayers, "gpu_backend", report.Host.GPU.Backend)
	}
	if err := startCtx.Err(); err != nil {
		return r.failStart(err)
	}

	port := r.cfg.Port
	if port == 0 {
		port, err = freePort(host)
		if err != nil {
			return r.failStart(fmt.Errorf("llamacpp: pick free port: %w", err))
		}
	}
	args := r.buildArgsArtifacts(local, host, port)
	r.logger.Info("llamacpp: launching llama-server", "binary", binary, "model", local.Model, "mmproj", local.MMProj, "host", host, "port", port)
	cmd := exec.Command(binary, args...)
	configureProcess(cmd)
	// Keep a duplicate of the admission-lock descriptor in the child. If the Go
	// supervisor crashes, the OS lock remains held until llama-server itself exits.
	cmd.ExtraFiles = append(cmd.ExtraFiles, admission.File())
	cmd.Env = sanitizedLlamaEnvironment(os.Environ())
	cmd.Stdout = newLogWriter(r.logger, "stdout")
	cmd.Stderr = newLogWriter(r.logger, "stderr")
	waitDone := make(chan struct{})

	r.mu.Lock()
	if r.closing || startCtx.Err() != nil {
		r.mu.Unlock()
		if err := startCtx.Err(); err != nil {
			return r.failStart(err)
		}
		return r.failStart(errors.New("llamacpp: runtime closed before process launch"))
	}
	r.state = RuntimeStarting
	if err := cmd.Start(); err != nil {
		r.mu.Unlock()
		return r.failStart(fmt.Errorf("llamacpp: start %s: %w", binary, err))
	}
	r.cmd = cmd
	r.baseURL = fmt.Sprintf("http://%s/v1", net.JoinHostPort(host, strconv.Itoa(port)))
	r.waitDone = waitDone
	r.waitErr = nil
	r.vision = local.MMProj != ""
	r.admission = admission
	admissionOwned = false
	r.mu.Unlock()

	go r.reap(cmd, waitDone)
	serverURL := fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
	if err := r.waitReady(startCtx, serverURL, apiKey, waitDone); err != nil {
		_ = stopCommand(context.Background(), cmd, waitDone)
		return r.failStart(err)
	}

	r.mu.Lock()
	if r.cmd != cmd || r.state != RuntimeStarting {
		err := r.waitErr
		if err == nil {
			err = errors.New("process left starting state")
		}
		r.mu.Unlock()
		return r.failStart(fmt.Errorf("llamacpp: llama-server exited while becoming ready: %w", err))
	}
	r.state = RuntimeReady
	r.started = true
	r.mu.Unlock()
	r.logger.Info("llamacpp: llama-server ready", "endpoint", r.BaseURL(), "vision", r.SupportsVision())
	return nil
}

func validateRuntimeConfig(cfg RuntimeConfig) error {
	hasLocal := strings.TrimSpace(cfg.ModelPath) != ""
	hasRemote := cfg.Ref != nil && strings.TrimSpace(cfg.Ref.Repo) != ""
	if hasLocal == hasRemote {
		return errors.New("llamacpp: runtime requires exactly one of ModelPath or Ref")
	}
	if hasRemote && cfg.MMProjPath != "" {
		return errors.New("llamacpp: MMProjPath is only valid with a local ModelPath; use Ref.MMProjFile for a repository")
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return fmt.Errorf("llamacpp: port must be between 0 and 65535, got %d", cfg.Port)
	}
	if cfg.CtxSize <= 0 {
		return fmt.Errorf("llamacpp: context size must be positive, got %d", cfg.CtxSize)
	}
	if cfg.Parallel <= 0 {
		return fmt.Errorf("llamacpp: parallel sequence count must be positive, got %d", cfg.Parallel)
	}
	if cfg.GPULayers < 0 {
		return fmt.Errorf("llamacpp: GPU layer count must be non-negative, got %d", cfg.GPULayers)
	}
	if cfg.StartupTimeout < 0 {
		return fmt.Errorf("llamacpp: startup timeout must be non-negative, got %s", cfg.StartupTimeout)
	}
	return validateExtraArgs(cfg.ExtraArgs)
}

func (r *Runtime) failStart(err error) error {
	r.mu.Lock()
	if r.state != RuntimeStopping && r.state != RuntimeStopped {
		r.state = RuntimeFailed
	}
	r.started = false
	r.lastErr = err
	r.mu.Unlock()
	return err
}

func (r *Runtime) reap(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait() // exactly one goroutine owns Wait for this child
	var admission *fileLock
	r.mu.Lock()
	if r.cmd == cmd {
		admission = r.admission
		r.admission = nil
		r.waitErr = err
		r.started = false
		r.baseURL = ""
		if r.state == RuntimeStopping || r.closing {
			r.state = RuntimeStopped
		} else {
			r.state = RuntimeFailed
			if err == nil {
				r.lastErr = errors.New("llamacpp: llama-server exited unexpectedly")
			} else {
				r.lastErr = fmt.Errorf("llamacpp: llama-server exited unexpectedly: %w", err)
			}
		}
	}
	r.mu.Unlock()
	if admission != nil {
		if lockErr := admission.Close(); lockErr != nil {
			r.logger.Error("llamacpp: release runtime admission lock", "err", lockErr)
		}
	}
	close(done)
}

func (r *Runtime) resolveArtifacts(ctx context.Context) (LocalArtifacts, error) {
	if r.cfg.ModelPath != "" {
		return LocalArtifacts{
			Model:      r.cfg.ModelPath,
			ModelFiles: []string{r.cfg.ModelPath},
			MMProj:     r.cfg.MMProjPath,
		}, nil
	}
	if r.cfg.Ref == nil || r.cfg.Ref.Repo == "" {
		return LocalArtifacts{}, errors.New("llamacpp: no model configured (set model_path or repo)")
	}
	if r.cfg.Downloader == nil {
		return LocalArtifacts{}, errors.New("llamacpp: repo configured but no downloader")
	}
	// Use a value copy so runtime-specific context/parallel sizing is applied to
	// the before-download gate without mutating a downloader shared by callers.
	downloader := *r.cfg.Downloader
	downloader.ContextTokens = r.cfg.CtxSize
	downloader.Parallel = r.cfg.Parallel
	if r.cfg.Profiler != nil {
		downloader.Profiler = r.cfg.Profiler
	}
	if r.cfg.FitPolicy.KVBytesPerToken != 0 {
		downloader.FitPolicy = r.cfg.FitPolicy
	}
	return downloader.EnsureArtifacts(ctx, *r.cfg.Ref)
}

func (r *Runtime) runtimeProfiler() Profiler {
	if r.cfg.Profiler != nil {
		return r.cfg.Profiler
	}
	if r.cfg.Downloader != nil {
		return r.cfg.Downloader.profiler()
	}
	return SystemProfiler{}
}

func (r *Runtime) admissionLockDirectory(local LocalArtifacts) (string, error) {
	if strings.TrimSpace(r.cfg.AdmissionLockDir) != "" {
		return r.cfg.AdmissionLockDir, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		return "", fmt.Errorf("llamacpp: determine per-user runtime admission-lock directory: %w", err)
	}
	_ = local
	return filepath.Join(cacheDir, "agent-smith", "locks"), nil
}

// LocalPreflightRequest supports caller-supplied files as well as every shard
// returned by EnsureArtifacts.
type LocalPreflightRequest struct {
	ModelFiles    []string
	MMProjPath    string
	ContextTokens int
	Parallel      int
	GPULayers     int
	FitPolicy     FitPolicy
}

// InspectLocal validates regular GGUF files and profiles the live host. It does
// not execute llama-server and is safe for a UI preview or a final launch gate.
func InspectLocal(ctx context.Context, profiler Profiler, req LocalPreflightRequest) (FitReport, error) {
	if len(req.ModelFiles) == 0 {
		return FitReport{}, errors.New("llamacpp: local preflight has no model files")
	}
	modelFiles, err := expandLocalSplit(req.ModelFiles)
	if err != nil {
		return FitReport{}, err
	}
	var modelBytes uint64
	for _, path := range modelFiles {
		size, err := inspectGGUF(path)
		if err != nil {
			return FitReport{}, err
		}
		var overflow bool
		modelBytes, overflow = add64(modelBytes, size)
		if overflow {
			return FitReport{}, errors.New("llamacpp: local model size overflow")
		}
	}
	var mmprojBytes uint64
	if req.MMProjPath != "" {
		var err error
		mmprojBytes, err = inspectGGUF(req.MMProjPath)
		if err != nil {
			return FitReport{}, fmt.Errorf("llamacpp: inspect mmproj: %w", err)
		}
	}
	if profiler == nil {
		profiler = SystemProfiler{}
	}
	diskPath := filepath.Dir(req.ModelFiles[0])
	host, err := profiler.Profile(ctx, diskPath)
	if err != nil {
		return FitReport{}, fmt.Errorf("llamacpp: host preflight failed: %w", err)
	}
	policy := req.FitPolicy
	if policy.KVBytesPerToken == 0 {
		policy = DefaultFitPolicy()
	}
	return EstimateFitWithPolicy(host, FitRequest{
		ModelBytes:    modelBytes,
		MMProjBytes:   mmprojBytes,
		ContextTokens: req.ContextTokens,
		Parallel:      req.Parallel,
		GPULayers:     req.GPULayers,
		VRAMBytes:     host.GPU.VRAMBytes,
		GPUUnified:    host.GPU.Unified,
	}, policy), nil
}

// expandLocalSplit prevents a manually configured first shard from being sized
// as though it were the whole model. Downloader-supplied manifests already
// provide every shard; a single local shard must be shard one and every sibling
// must exist before the host fit calculation proceeds.
func expandLocalSplit(paths []string) ([]string, error) {
	if len(paths) != 1 {
		return paths, nil
	}
	path := paths[0]
	match := splitGGUF.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return paths, nil
	}
	index, _ := strconv.Atoi(match[2])
	total, _ := strconv.Atoi(match[3])
	if index != 1 || total < 1 {
		return nil, fmt.Errorf("llamacpp: local split model must point to shard 00001, got %q", path)
	}
	out := make([]string, 0, total)
	for i := 1; i <= total; i++ {
		name := fmt.Sprintf("%s-%05d-of-%05d.gguf", match[1], i, total)
		candidate := filepath.Join(filepath.Dir(path), name)
		if _, err := os.Stat(candidate); err != nil {
			return nil, fmt.Errorf("llamacpp: local split model is incomplete; shard %q is required: %w", candidate, err)
		}
		out = append(out, candidate)
	}
	return out, nil
}

func inspectGGUF(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("llamacpp: model file %q is not accessible: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() || fi.Size() < 4 {
		return 0, fmt.Errorf("llamacpp: model file %q is not a non-empty regular file", path)
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return 0, fmt.Errorf("llamacpp: model file %q does not have GGUF magic", path)
	}
	return uint64(fi.Size()), nil
}

func (r *Runtime) buildArgs(modelPath, host string, port int) []string {
	return r.buildArgsArtifacts(LocalArtifacts{Model: modelPath, MMProj: r.cfg.MMProjPath}, host, port)
}

func (r *Runtime) buildArgsArtifacts(local LocalArtifacts, host string, port int) []string {
	args := []string{
		"--model", local.Model,
		"--host", host,
		"--port", strconv.Itoa(port),
		"--api-key-file", r.apiKeyFile,
		/* Bound or disable version-dependent facilities not included in admission. */
		"--batch-size", strconv.Itoa(boundedBatchSize),
		"--ubatch-size", strconv.Itoa(boundedUBatchSize),
		"--cache-ram", "0",
		"--no-cache-prompt",
		"--no-cache-idle-slots",
		"--offline",
		"--no-ui",
		"--no-slots",
	}
	if local.MMProj != "" {
		args = append(args, "--mmproj", local.MMProj)
	}
	if r.cfg.CtxSize > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(r.cfg.CtxSize))
	}
	if r.cfg.Parallel > 0 {
		args = append(args, "--parallel", strconv.Itoa(r.cfg.Parallel))
	}
	/* Zero is meaningful: never inherit llama.cpp's evolving auto-offload default. */
	args = append(args, "-ngl", strconv.Itoa(r.cfg.GPULayers))
	if r.cfg.Jinja {
		args = append(args, "--jinja")
	}
	return append(args, r.cfg.ExtraArgs...)
}

var protectedArgs = map[string]struct{}{
	"-m": {}, "--model": {}, "--model-url": {}, "-mu": {}, "-hf": {}, "-hfr": {}, "--hf-repo": {}, "-hff": {}, "--hf-file": {},
	"--hf-token": {}, "-hft": {}, "--docker-repo": {}, "-dr": {},
	"--mmproj": {}, "--mmproj-url": {}, "--host": {}, "--port": {}, "--reuse-port": {}, "--api-key": {}, "--api-key-file": {},
	"-c": {}, "--ctx-size": {}, "-np": {}, "--parallel": {}, "-ngl": {}, "--n-gpu-layers": {}, "--gpu-layers": {},
	"-b": {}, "--batch-size": {}, "-ub": {}, "--ubatch-size": {}, "-ctk": {}, "--cache-type-k": {}, "-ctv": {}, "--cache-type-v": {},
	"--kv-unified": {}, "--no-kv-unified": {}, "--no-kv-offload": {}, "--kv-offload": {}, "--swa-full": {},
	"--cache-ram": {}, "-cram": {}, "--cache-prompt": {}, "--no-cache-prompt": {}, "--cache-reuse": {},
	"--cache-idle-slots": {}, "--no-cache-idle-slots": {}, "--ctx-checkpoints": {}, "--swa-checkpoints": {},
	"-ts": {}, "--tensor-split": {}, "--split-mode": {}, "--main-gpu": {}, "--device": {},
	"--rpc": {}, "--rpc-server-host": {}, "--rpc-batch-size": {}, "--mlock": {}, "--mmap": {}, "--no-mmap": {},
	"-md": {}, "--model-draft": {}, "--model-vocoder": {}, "--mmproj-vocoder": {},
	"--lora": {}, "--lora-scaled": {}, "--control-vector": {}, "--control-vector-scaled": {}, "--override-tensor": {},
	"--offline": {}, "--ui": {}, "--no-ui": {}, "--webui": {}, "--no-webui": {},
	"--ui-mcp-proxy": {}, "--webui-mcp-proxy": {}, "--tools": {}, "--media-path": {},
	"--path": {}, "--api-prefix": {}, "--props": {}, "--slots": {}, "--no-slots": {}, "--slot-save-path": {},
	"--models-dir": {}, "--models-preset": {}, "--models-max": {}, "--models-autoload": {}, "--no-models-autoload": {},
}

func validateExtraArgs(args []string) error {
	for _, arg := range args {
		flag := strings.ToLower(strings.TrimSpace(arg))
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag = flag[:i]
		}
		if _, protected := protectedArgs[flag]; protected {
			return fmt.Errorf("llamacpp: extra argument %q overrides a protected model, resource, or network setting", arg)
		}
	}
	return nil
}

func sanitizedLlamaEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if _, allowed := allowedLlamaEnvironment[name]; !allowed {
			continue
		}
		out = append(out, entry)
	}
	return out
}

/* Keep credentials and arbitrary loader/config overrides out of the native parser process. */
var allowedLlamaEnvironment = map[string]struct{}{
	"HOME": {}, "PATH": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {},
	"LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "TZ": {}, "NO_COLOR": {},
	"CUDA_VISIBLE_DEVICES": {}, "HIP_VISIBLE_DEVICES": {}, "ROCR_VISIBLE_DEVICES": {},
	"GGML_CUDA_ENABLE_UNIFIED_MEMORY": {}, "GGML_CUDA_NO_VMM": {},
	"GGML_CUDA_FORCE_MMQ": {}, "GGML_CUDA_FORCE_CUBLAS": {}, "GGML_VK_VISIBLE_DEVICES": {},
	"OMP_NUM_THREADS": {}, "KMP_AFFINITY": {},
	/* Used only by this package's exec-self runtime tests. */
	"LLAMACPP_FAKE": {},
}

func runtimeAPIKey(configured string) (string, error) {
	key := strings.TrimSpace(configured)
	if key != "" {
		if key != configured || len(key) < 24 || strings.ContainsAny(key, ",\r\n\t ") {
			return "", errors.New("llamacpp: configured local API key must be at least 24 non-whitespace characters and contain no comma")
		}
		return key, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("llamacpp: generate ephemeral local API key: %w", err)
	}
	return "asm_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func createRuntimeAPIKeyFile(dir, key string) (string, error) {
	f, err := os.CreateTemp(dir, ".llama-api-key-*.txt")
	if err != nil {
		return "", fmt.Errorf("llamacpp: create private API-key file: %w", err)
	}
	path := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return "", fmt.Errorf("llamacpp: secure API-key file: %w", err)
	}
	if _, err := io.WriteString(f, key+"\n"); err != nil {
		cleanup()
		return "", fmt.Errorf("llamacpp: write API-key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("llamacpp: sync API-key file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("llamacpp: close API-key file: %w", err)
	}
	return path, nil
}

func validateLoopbackHost(host string) error {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("llamacpp: host %q is not an explicit loopback address", host)
	}
	return nil
}

func (r *Runtime) waitReady(ctx context.Context, serverURL, apiKey string, waitDone <-chan struct{}) error {
	timeout := r.cfg.StartupTimeout
	if timeout <= 0 {
		timeout = defaultStartupTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: 3 * time.Second}
	for {
		select {
		case <-waitDone:
			r.mu.RLock()
			err := r.waitErr
			r.mu.RUnlock()
			if err == nil {
				err = errors.New("process exited")
			}
			return fmt.Errorf("llamacpp: llama-server exited before ready: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("llamacpp: llama-server not ready after %s", timeout)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/health", nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			healthy := resp.StatusCode >= 200 && resp.StatusCode < 300
			_ = resp.Body.Close()
			if !healthy {
				continue
			}
			/* /health is public upstream; authenticate an actual API route too. */
			modelsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/v1/models", nil)
			if err != nil {
				continue
			}
			modelsReq.Header.Set("Authorization", "Bearer "+apiKey)
			modelsResp, err := client.Do(modelsReq)
			if err != nil {
				continue
			}
			var payload struct {
				Object string            `json:"object"`
				Data   []json.RawMessage `json:"data"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(modelsResp.Body, 1<<20)).Decode(&payload)
			ready := modelsResp.StatusCode >= 200 && modelsResp.StatusCode < 300 && decodeErr == nil && payload.Object == "list" && len(payload.Data) > 0
			_ = modelsResp.Body.Close()
			if ready {
				/*
					The authenticated probe proves the key WORKS, not that the
					server REJECTS an unauthenticated request. If --api-key-file is
					unsupported/ignored by this build, the endpoint is open to every
					local process while the probe still passes. Confirm a keyless
					request to a PROTECTED route is refused with 401/403.

					The route matters: llama.cpp deliberately leaves /health and the
					model-listing endpoints (/models, /v1/models) public even when a
					key is set, so probing those yields a 200 that says nothing about
					enforcement. /v1/chat/completions IS behind the api-key
					middleware, which rejects an unauthenticated request with 401
					before doing any inference, so it is both correct and cheap.
				*/
				if apiKey != "" {
					body := strings.NewReader(`{"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`)
					unauthReq, uerr := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/v1/chat/completions", body)
					if uerr == nil {
						unauthReq.Header.Set("Content-Type", "application/json")
						if unauthResp, derr := client.Do(unauthReq); derr == nil {
							code := unauthResp.StatusCode
							_ = unauthResp.Body.Close()
							if code >= 200 && code < 300 {
								return fmt.Errorf("llamacpp: llama-server answered an unauthenticated /v1/chat/completions request (status %d); the API key is not being enforced — is --api-key-file supported by this build?", code)
							}
							if code != http.StatusUnauthorized && code != http.StatusForbidden {
								/*
									Neither a clear accept (2xx) nor a clear reject
									(401/403): don't block a working server on an
									ambiguous status, but surface it.
								*/
								r.logger.Warn("llamacpp: could not confirm api-key enforcement from readiness probe", "status", code)
							}
						}
					}
				}
				return nil
			}
		}
	}
}

func (r *Runtime) BaseURL() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.baseURL
}

/* APIKey returns the per-launch credential for the supervised loopback API. */
func (r *Runtime) APIKey() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.apiKey
}

func (r *Runtime) State() RuntimeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *Runtime) Err() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastErr
}

func (r *Runtime) SupportsVision() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.vision
}

// Wait observes the sole process-reaper result without ever calling Cmd.Wait.
func (r *Runtime) Wait(ctx context.Context) error {
	r.mu.RLock()
	done := r.waitDone
	lastErr := r.lastErr
	r.mu.RUnlock()
	if done == nil {
		if lastErr != nil {
			return lastErr
		}
		return errors.New("llamacpp: runtime has not launched a process")
	}
	select {
	case <-done:
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) Close(ctx context.Context) error {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()

	r.mu.Lock()
	r.closing = true
	r.state = RuntimeStopping
	if r.startCancel != nil {
		r.startCancel()
	}
	r.mu.Unlock()

	// If Start is still resolving/downloading, cancellation makes it unwind;
	// this barrier guarantees it cannot launch after Close has inspected cmd.
	r.startMu.Lock()
	r.startMu.Unlock()

	r.mu.RLock()
	cmd := r.cmd
	done := r.waitDone
	r.mu.RUnlock()
	if cmd == nil || cmd.Process == nil || done == nil {
		r.mu.Lock()
		r.state = RuntimeStopped
		r.started = false
		r.baseURL = ""
		r.mu.Unlock()
		return nil
	}
	err := stopCommand(ctx, cmd, done)
	r.mu.Lock()
	r.state = RuntimeStopped
	r.started = false
	r.baseURL = ""
	r.mu.Unlock()
	if err == nil {
		r.logger.Info("llamacpp: llama-server stopped", "pid", cmd.Process.Pid)
	}
	return err
}

func stopCommand(ctx context.Context, cmd *exec.Cmd, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	_ = interruptProcess(cmd)
	grace := time.NewTimer(5 * time.Second)
	defer grace.Stop()
	select {
	case <-done:
		return nil
	case <-grace.C:
	case <-ctx.Done():
	}
	_ = killProcess(cmd)
	hard := time.NewTimer(5 * time.Second)
	defer hard.Stop()
	select {
	case <-done:
		return nil
	case <-hard.C:
		return errors.New("llamacpp: process did not exit after SIGKILL/termination")
	}
}

func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
