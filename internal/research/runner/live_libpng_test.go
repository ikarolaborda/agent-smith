package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

const (
	libpngVulnerableRevision = "2b978915d82377df13fcbb1fb56660195ded868a"
	libpngFixedRevision      = "fbed16182b92eeb3a06d96e49f0836d450318098"
	libpngReproducerSHA256   = "fdeb6ef7e80ebebd2b92d2fdb6855073dce06b7d5e1d27012532dd738cfaa595"
)

// TestLiveLibPNGKnownBugCalibration is an opt-in real-backend benchmark. It
// proves that the public input trips CVE-2025-64720 on the exact vulnerable
// tag and is clean on the upstream revision containing the finalized fix.
func TestLiveLibPNGKnownBugCalibration(t *testing.T) {
	imageDigest := strings.TrimSpace(os.Getenv("AGENT_SMITH_LIVE_LIBPNG_IMAGE"))
	vulnerableSource := strings.TrimSpace(os.Getenv("AGENT_SMITH_LIVE_LIBPNG_VULNERABLE_SOURCE"))
	fixedSource := strings.TrimSpace(os.Getenv("AGENT_SMITH_LIVE_LIBPNG_FIXED_SOURCE"))
	if imageDigest == "" || vulnerableSource == "" || fixedSource == "" {
		t.Skip("set AGENT_SMITH_LIVE_LIBPNG_IMAGE, AGENT_SMITH_LIVE_LIBPNG_VULNERABLE_SOURCE, and AGENT_SMITH_LIVE_LIBPNG_FIXED_SOURCE")
	}
	if !digestPattern.MatchString(imageDigest) {
		t.Fatal("AGENT_SMITH_LIVE_LIBPNG_IMAGE must be an exact sha256 image ID")
	}
	requireCleanRevision(t, vulnerableSource, libpngVulnerableRevision)
	requireCleanRevision(t, fixedSource, libpngFixedRevision)

	ctx := context.Background()
	repository, campaign := liveLibPNGStore(t)
	defer repository.Close()
	backend := NewDockerBackend(DockerOptions{RequireRootless: false})
	broker, err := NewBroker(Options{
		Backend: backend, Journal: repository, Artifacts: repository,
		StagingRoot:            filepath.Join(repository.Root(), "live-staging"),
		MaxCapturedOutputBytes: 128 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(ctx); err != nil {
		t.Fatalf("live Docker preflight: %v", err)
	}
	defer broker.Close()

	manifest := liveLibPNGManifest(imageDigest)
	inputArtifact, evidenceDir := ingestLiveLibPNGReproducer(t, ctx, repository, campaign.ID)
	vulnerableBuild := runLiveLibPNGBuild(t, ctx, broker, repository, manifest, campaign, vulnerableSource, libpngVulnerableRevision)
	fixedBuild := runLiveLibPNGBuild(t, ctx, broker, repository, manifest, campaign, fixedSource, libpngFixedRevision)
	smoke := runLiveLibPNGSmoke(t, ctx, broker, manifest, campaign, vulnerableSource, vulnerableBuild, libpngVulnerableRevision)
	if smoke.Status != domain.RunCompleted {
		t.Fatalf("independent seed failed on vulnerable build: %#v\n%s", smoke, artifactTextByRole(t, ctx, repository, smoke.ArtifactIDs, "stderr_log"))
	}

	vulnerable := runLiveLibPNGReproducer(t, ctx, broker, manifest, campaign, vulnerableSource, vulnerableBuild, evidenceDir, inputArtifact.ID, libpngVulnerableRevision)
	vulnerableLog := artifactTextByRole(t, ctx, repository, vulnerable.ArtifactIDs, "stderr_log")
	if vulnerable.Status != domain.RunFailed {
		t.Fatalf("vulnerable revision did not fail: %#v", vulnerable)
	}
	for _, signature := range []string{"AddressSanitizer: global-buffer-overflow", "READ of size 2", "png_image_read_composite", "png_sRGB_base"} {
		if !strings.Contains(vulnerableLog, signature) {
			t.Fatalf("vulnerable stderr missing %q:\n%s", signature, vulnerableLog)
		}
	}

	fixed := runLiveLibPNGReproducer(t, ctx, broker, manifest, campaign, fixedSource, fixedBuild, evidenceDir, inputArtifact.ID, libpngFixedRevision)
	fixedLog := artifactTextByRole(t, ctx, repository, fixed.ArtifactIDs, "stderr_log")
	if fixed.Status != domain.RunCompleted {
		t.Fatalf("fixed revision failed: %#v\n%s", fixed, fixedLog)
	}
	if strings.Contains(fixedLog, "AddressSanitizer:") {
		t.Fatalf("fixed revision retained sanitizer signal:\n%s", fixedLog)
	}
}

func liveLibPNGManifest(imageDigest string) domain.ApparatusManifest {
	limit := domain.ResourceBudget{
		MaxWallSeconds: 180, MaxMemoryBytes: 2 << 30, MaxCPUSeconds: 360,
		MaxDiskBytes: 1 << 30, MaxInodes: 131072, MaxPIDs: 64, MaxConcurrent: 1,
	}
	return domain.ApparatusManifest{
		SchemaVersion: 1, ID: "libpng-cve-2025-64720-v1", Name: "libpng known-bug libFuzzer benchmark", Version: "1.0.0",
		ImageDigest: imageDigest, Engine: "libfuzzer", Sanitizers: []string{"address"}, Architectures: []string{"amd64"},
		Harnesses:   []domain.HarnessManifest{{Name: "libpng_read_fuzzer", Binary: "/build/fuzz_target"}},
		Operations:  []domain.Operation{domain.OperationBuild, domain.OperationSmokeTest, domain.OperationReproduce},
		Environment: map[string]string{"ASAN_OPTIONS": "abort_on_error=1:symbolize=1"}, Limits: limit,
	}
}

func liveLibPNGStore(t *testing.T) (*store.Store, domain.Campaign) {
	t.Helper()
	ctx := context.Background()
	repository, err := store.Open(ctx, store.Config{Root: t.TempDir(), MaxArtifactBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	budget := domain.ResourceBudget{
		MaxWallSeconds: 180, MaxMemoryBytes: 2 << 30, MaxCPUSeconds: 360,
		MaxDiskBytes: 1 << 30, MaxInodes: 131072, MaxPIDs: 64, MaxConcurrent: 1,
	}
	scope := domain.AuthorizationScope{
		SchemaVersion: 1, ID: "scope-libpng-live", OperatorID: "operator", Purpose: "known-bug calibration",
		TargetRepository: "https://github.com/pnggroup/libpng", AllowedRevisions: []string{libpngVulnerableRevision, libpngFixedRevision},
		WorkspaceRoots: []string{repository.Root()}, AllowedOperations: []domain.Operation{domain.OperationBuild, domain.OperationSmokeTest, domain.OperationReproduce},
		AllowedApparatusIDs: []string{"libpng-cve-2025-64720-v1"}, Budget: budget, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := repository.CreateScope(ctx, scope); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	campaign, err := repository.CreateCampaign(ctx, domain.Campaign{
		SchemaVersion: 1, ID: "campaign-libpng-live", ScopeID: scope.ID, Name: "libpng CVE-2025-64720 calibration",
		State: domain.CampaignDraft, Budget: budget, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	return repository, campaign
}

func runLiveLibPNGBuild(t *testing.T, ctx context.Context, broker *Broker, repository *store.Store, manifest domain.ApparatusManifest, campaign domain.Campaign, source, revision string) string {
	t.Helper()
	job, err := apparatus.NewJob(manifest, apparatus.JobRequest{
		CampaignID: campaign.ID, ScopeID: campaign.ScopeID, Operation: domain.OperationBuild,
		Harness: "libpng_read_fuzzer", Revision: revision, Sanitizer: "address", SourceDir: source,
		Budget:        domain.ResourceBudget{MaxWallSeconds: 120, MaxMemoryBytes: 2 << 30, MaxCPUSeconds: 240, MaxDiskBytes: 1 << 30, MaxInodes: 131072, MaxPIDs: 64, MaxConcurrent: 1},
		CorrelationID: "live-build-" + revision[:12],
	})
	if err != nil {
		t.Fatal(err)
	}
	result := submitAndWaitLiveLibPNG(t, ctx, broker, job, 150*time.Second)
	if result.Status != domain.RunCompleted {
		t.Fatalf("build %s failed: %#v\n%s", revision, result, artifactTextByRole(t, ctx, repository, result.ArtifactIDs, "stderr_log"))
	}
	buildDir := t.TempDir()
	materializeArtifactRole(t, ctx, repository, result.ArtifactIDs, "harness_binary", filepath.Join(buildDir, "fuzz_target"), 0o555)
	return buildDir
}

func runLiveLibPNGReproducer(t *testing.T, ctx context.Context, broker *Broker, manifest domain.ApparatusManifest, campaign domain.Campaign, source, buildDir, evidenceDir, inputArtifactID, revision string) domain.RunResult {
	t.Helper()
	job, err := apparatus.NewJob(manifest, apparatus.JobRequest{
		CampaignID: campaign.ID, ScopeID: campaign.ScopeID, InputArtifactID: inputArtifactID,
		Operation: domain.OperationReproduce, Harness: "libpng_read_fuzzer", Revision: revision, Sanitizer: "address",
		SourceDir: source, BuildDir: buildDir, EvidenceDir: evidenceDir,
		Budget:        domain.ResourceBudget{MaxWallSeconds: 15, MaxMemoryBytes: 1 << 30, MaxCPUSeconds: 15, MaxDiskBytes: 64 << 20, MaxInodes: 1024, MaxPIDs: 32, MaxConcurrent: 1},
		CorrelationID: "live-reproduce-" + revision[:12],
	})
	if err != nil {
		t.Fatal(err)
	}
	return submitAndWaitLiveLibPNG(t, ctx, broker, job, 30*time.Second)
}

func runLiveLibPNGSmoke(t *testing.T, ctx context.Context, broker *Broker, manifest domain.ApparatusManifest, campaign domain.Campaign, source, buildDir, revision string) domain.RunResult {
	t.Helper()
	job, err := apparatus.NewJob(manifest, apparatus.JobRequest{
		CampaignID: campaign.ID, ScopeID: campaign.ScopeID, Operation: domain.OperationSmokeTest,
		Harness: "libpng_read_fuzzer", Revision: revision, Sanitizer: "address", SourceDir: source, BuildDir: buildDir,
		Budget:        domain.ResourceBudget{MaxWallSeconds: 15, MaxMemoryBytes: 1 << 30, MaxCPUSeconds: 15, MaxDiskBytes: 64 << 20, MaxInodes: 1024, MaxPIDs: 32, MaxConcurrent: 1},
		CorrelationID: "live-smoke-" + revision[:12],
	})
	if err != nil {
		t.Fatal(err)
	}
	return submitAndWaitLiveLibPNG(t, ctx, broker, job, 30*time.Second)
}

func submitAndWaitLiveLibPNG(t *testing.T, ctx context.Context, broker *Broker, job domain.WorkerJob, timeout time.Duration) domain.RunResult {
	t.Helper()
	run, err := broker.Submit(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := broker.Wait(waitCtx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func ingestLiveLibPNGReproducer(t *testing.T, ctx context.Context, repository *store.Store, campaignID string) (domain.Artifact, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve live test path")
	}
	fixturePath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "apparatus", "libpng-known-bug", "testdata", "cve-2025-64720", "reproducer.b64")
	encoded, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	if digest != libpngReproducerSHA256 {
		t.Fatalf("reproducer sha256=%s", digest)
	}
	artifact, err := repository.PutArtifact(ctx, domain.Artifact{
		SchemaVersion: 1, CampaignID: campaignID, Role: "known_benchmark_input", MediaType: "image/png", Sensitivity: "public",
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	evidenceDir := t.TempDir()
	materializeArtifactRole(t, ctx, repository, []string{artifact.ID}, "known_benchmark_input", filepath.Join(evidenceDir, "reproducer"), 0o444)
	return artifact, evidenceDir
}

func materializeArtifactRole(t *testing.T, ctx context.Context, repository *store.Store, artifactIDs []string, role, destination string, mode os.FileMode) {
	t.Helper()
	for _, artifactID := range artifactIDs {
		metadata, input, err := repository.OpenArtifact(ctx, artifactID)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Role != role {
			input.Close()
			continue
		}
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			_, err = io.Copy(output, input)
		}
		input.Close()
		if output != nil {
			if closeErr := output.Close(); err == nil {
				err = closeErr
			}
		}
		if err == nil {
			err = os.Chmod(destination, mode)
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("artifact role %q not found", role)
}

func artifactTextByRole(t *testing.T, ctx context.Context, repository *store.Store, artifactIDs []string, role string) string {
	t.Helper()
	for _, artifactID := range artifactIDs {
		metadata, input, err := repository.OpenArtifact(ctx, artifactID)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.Role != role {
			input.Close()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(input, 1<<20))
		input.Close()
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	return ""
}

func requireCleanRevision(t *testing.T, source, revision string) {
	t.Helper()
	absolute, err := filepath.Abs(source)
	if err != nil {
		t.Fatal(err)
	}
	head, err := exec.Command("git", "-C", absolute, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != revision {
		t.Fatalf("source %s is not exact revision %s", absolute, revision)
	}
	status, err := exec.Command("git", "-C", absolute, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil || len(status) != 0 {
		t.Fatalf("source %s is not a clean tracked worktree: %s", absolute, status)
	}
}
