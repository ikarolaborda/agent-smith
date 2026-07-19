package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBrokerPersistsTypedResultAndArtifacts(t *testing.T) {
	ctx := context.Background()
	repository, campaign := runnerStore(t)
	defer repository.Close()
	backend := &FakeBackend{ExecuteFunc: func(_ context.Context, job domain.WorkerJob, staging string) (Execution, error) {
		if err := os.WriteFile(filepath.Join(staging, "crash-input"), []byte("boom"), 0o600); err != nil {
			return Execution{}, err
		}
		return Execution{
			Status: domain.RunCompleted, Stdout: []byte("123456"), Stderr: []byte("asan"),
			Usage:     domain.ResourceUsage{WallMillis: 12, MaxRSSBytes: 2048},
			Apparatus: domain.ApparatusIdentity{ManifestID: "apparatus-1", Harness: "parser", Sanitizer: "address"},
		}, nil
	}}
	broker, err := NewBroker(Options{Backend: backend, Journal: repository, Artifacts: repository, StagingRoot: filepath.Join(repository.Root(), "staging"), MaxCapturedOutputBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	job := validJob(t, campaign)
	run, err := broker.Submit(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := broker.Wait(waitCtx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.RunCompleted || result.IsolationAssurance != "deterministic_test_double" {
		t.Fatalf("result=%#v", result)
	}
	if len(result.ArtifactIDs) != 3 || !result.Output.StdoutTruncated || result.Output.BytesDropped != 2 {
		t.Fatalf("artifacts/output=%#v", result)
	}
	persisted, err := repository.GetRun(ctx, run.ID)
	if err != nil || persisted.Status != domain.RunCompleted || len(persisted.ArtifactIDs) != 3 {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
	roles := map[string]bool{}
	for _, id := range persisted.ArtifactIDs {
		artifact, err := repository.GetArtifact(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		roles[artifact.Role] = true
	}
	for _, role := range []string{"stdout_log", "stderr_log", "crashing_input"} {
		if !roles[role] {
			t.Fatalf("missing role %s in %#v", role, roles)
		}
	}
}

func TestBrokerRecoversInterruptedRun(t *testing.T) {
	ctx := context.Background()
	repository, campaign := runnerStore(t)
	defer repository.Close()
	job := validJob(t, campaign)
	job.ID, job.RunID, job.Status = "job-recover", "run-recover", domain.RunRunning
	job.SchemaVersion, job.CreatedAt, job.UpdatedAt = 1, time.Now().UTC(), time.Now().UTC()
	started := job.CreatedAt
	run := domain.ExperimentRun{SchemaVersion: 1, ID: job.RunID, CampaignID: campaign.ID, ScopeID: campaign.ScopeID, Operation: job.Operation, Status: domain.RunRunning, CreatedAt: started, StartedAt: &started}
	if err := repository.CreateJobAndRun(ctx, job, run); err != nil {
		t.Fatal(err)
	}
	backend := &FakeBackend{}
	broker, err := NewBroker(Options{Backend: backend, Journal: repository, Artifacts: repository, StagingRoot: filepath.Join(repository.Root(), "staging")})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := broker.Wait(waitCtx, run.ID)
	if err != nil || result.Status != domain.RunCompleted || backend.CallCount() != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, backend.CallCount(), err)
	}
}

func TestCollectorRejectsUnallowlistedAndSymlinkOutput(t *testing.T) {
	ctx := context.Background()
	repository, campaign := runnerStore(t)
	defer repository.Close()
	collector := NewCollector(repository, 100)
	job := validJob(t, campaign)
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(ctx, job, staging, Execution{}, Assurance{Isolation: "test"}); err == nil || !strings.Contains(err.Error(), "unallowlisted") {
		t.Fatalf("unallowlisted error=%v", err)
	}
	if err := os.Remove(filepath.Join(staging, "unexpected")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(staging, "crash-target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(staging, "crash-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := collector.Collect(ctx, job, staging, Execution{}, Assurance{Isolation: "test"}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestInvalidJobFailsClosed(t *testing.T) {
	job := domain.WorkerJob{CampaignID: "campaign", ScopeID: "scope", Operation: domain.OperationFuzz, AuditCorrelationID: "audit", ImageDigest: "latest"}
	if err := validateJob(job); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("error=%v", err)
	}
}

func TestDockerBackendPreflightAndHardenedArgv(t *testing.T) {
	executor := &recordingExecutor{digest: testDigest, rootless: true, seccomp: true, cgroupVersion: 2, runtimes: `{"runsc":{}}`}
	backend := NewDockerBackend(DockerOptions{Executor: executor, RequireRootless: true, Runtime: "runsc", OutputLimit: 4})
	assurance, err := backend.Preflight(context.Background())
	if err != nil || assurance.Isolation != "gvisor" || !assurance.Rootless || assurance.Seccomp != "builtin" || assurance.CgroupVersion != 2 {
		t.Fatalf("assurance=%#v err=%v", assurance, err)
	}
	job := domain.WorkerJob{
		RunID: "run-1", CampaignID: "campaign", ScopeID: "scope", Operation: domain.OperationFuzz, ImageDigest: testDigest,
		Arguments: map[string]string{"harness": "parser"}, Budget: domain.ResourceBudget{MaxMemoryBytes: 1024, MaxPIDs: 8},
	}
	execution, err := backend.Execute(context.Background(), job, t.TempDir())
	if err != nil || execution.Status != domain.RunCompleted || !execution.StdoutTruncated {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	argv := strings.Join(executor.lastRun(), " ")
	for _, required := range []string{"--network none", "--read-only", "--cap-drop ALL", "no-new-privileges", "--runtime runsc", testDigest, "/apparatus/dispatch fuzz", "--harness parser"} {
		if !strings.Contains(argv, required) {
			t.Fatalf("argv missing %q: %s", required, argv)
		}
	}
	if strings.Contains(argv, "sh -c") {
		t.Fatalf("shell found in argv: %s", argv)
	}
}

func TestDockerBackendRequiresRootlessWhenConfigured(t *testing.T) {
	backend := NewDockerBackend(DockerOptions{Executor: &recordingExecutor{seccomp: true, cgroupVersion: 2}, RequireRootless: true})
	if _, err := backend.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "rootless") {
		t.Fatalf("error=%v", err)
	}
}

func TestDockerBackendRequiresSeccompAndCgroups(t *testing.T) {
	backend := NewDockerBackend(DockerOptions{Executor: &recordingExecutor{rootless: true, cgroupVersion: 2}, RequireRootless: true})
	if _, err := backend.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "seccomp") {
		t.Fatalf("seccomp error=%v", err)
	}
	backend = NewDockerBackend(DockerOptions{Executor: &recordingExecutor{rootless: true, seccomp: true}, RequireRootless: true})
	if _, err := backend.Preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "cgroup") {
		t.Fatalf("cgroup error=%v", err)
	}
}

type recordingExecutor struct {
	mu            sync.Mutex
	calls         [][]string
	digest        string
	rootless      bool
	seccomp       bool
	cgroupVersion int
	runtimes      string
}

func (e *recordingExecutor) Run(_ context.Context, _ string, args []string, stdout, _ io.Writer) error {
	e.mu.Lock()
	e.calls = append(e.calls, append([]string(nil), args...))
	e.mu.Unlock()
	switch {
	case len(args) >= 1 && args[0] == "info" && strings.Contains(args[len(args)-1], "SecurityOptions"):
		options := make([]string, 0, 2)
		if e.rootless {
			options = append(options, "name=rootless")
		}
		if e.seccomp {
			options = append(options, "name=seccomp,profile=builtin")
		}
		encoded, _ := json.Marshal(options)
		_, _ = stdout.Write(encoded)
	case len(args) >= 1 && args[0] == "info" && strings.Contains(args[len(args)-1], "CgroupDriver"):
		if e.cgroupVersion > 0 {
			_, _ = fmt.Fprintf(stdout, `"systemd" %d`, e.cgroupVersion)
		}
	case len(args) >= 1 && args[0] == "info":
		_, _ = io.WriteString(stdout, e.runtimes)
	case len(args) >= 2 && args[0] == "image" && args[1] == "inspect":
		_, _ = io.WriteString(stdout, e.digest+"\n")
	case len(args) >= 1 && args[0] == "run":
		_, _ = io.WriteString(stdout, "123456")
	}
	return nil
}

func (e *recordingExecutor) lastRun() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for index := len(e.calls) - 1; index >= 0; index-- {
		if len(e.calls[index]) > 0 && e.calls[index][0] == "run" {
			return e.calls[index]
		}
	}
	return nil
}

func runnerStore(t *testing.T) (*store.Store, domain.Campaign) {
	t.Helper()
	ctx := context.Background()
	repository, err := store.Open(ctx, store.Config{Root: t.TempDir(), MaxArtifactBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scope := domain.AuthorizationScope{
		SchemaVersion: 1, ID: "scope", OperatorID: "operator", Purpose: "test", TargetRepository: "repo",
		AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{repository.Root()}, AllowedOperations: []domain.Operation{domain.OperationFuzz},
		Budget: domain.ResourceBudget{MaxWallSeconds: 10, MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20, MaxPIDs: 16}, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := repository.CreateScope(ctx, scope); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	campaign, err := repository.CreateCampaign(ctx, domain.Campaign{SchemaVersion: 1, ID: "campaign", ScopeID: scope.ID, Name: "test", State: domain.CampaignDraft, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	return repository, campaign
}

func validJob(t *testing.T, campaign domain.Campaign) domain.WorkerJob {
	t.Helper()
	mount := t.TempDir()
	return domain.WorkerJob{
		CampaignID: campaign.ID, ScopeID: campaign.ScopeID, Operation: domain.OperationFuzz, ImageDigest: testDigest,
		AuditCorrelationID: "correlation", Mounts: []domain.JobMount{{Name: "source", HostPath: mount, ContainerPath: "/source", ReadOnly: true}},
		ArtifactRules: []domain.ArtifactRule{{Role: "crashing_input", MediaType: "application/octet-stream", Glob: "crash-*", MaxCount: 2, MaxBytes: 1024}},
		Budget:        domain.ResourceBudget{MaxWallSeconds: 5, MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20, MaxPIDs: 16},
	}
}
