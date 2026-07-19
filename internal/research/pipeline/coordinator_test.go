package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

func TestCoordinatorMaterializesBuildAndGroupsCrashIdempotently(t *testing.T) {
	ctx := context.Background()
	repository, campaign := pipelineStore(t)
	defer repository.Close()
	coordinator, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}

	binary, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-build", Role: "harness_binary", MediaType: "application/x-executable"}, strings.NewReader("fixture-binary"))
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-build", Role: "build_provenance", MediaType: "application/json"}, strings.NewReader(`{"compiler":"clang fixture","compiler_flags":"-fsanitize=address"}`))
	if err != nil {
		t.Fatal(err)
	}
	buildJob := domain.WorkerJob{RunID: "run-build", CampaignID: campaign.ID, Operation: domain.OperationBuild, ImageDigest: testDigest,
		Arguments: map[string]string{"manifest": "apparatus", "harness": "parser", "revision": "abc", "sanitizer": "address"}, AuditCorrelationID: "correlation", CreatedAt: time.Now().UTC()}
	buildResult := domain.RunResult{RunID: buildJob.RunID, Operation: buildJob.Operation, Status: domain.RunCompleted, ArtifactIDs: []string{binary.ID, provenance.ID}, IsolationAssurance: "test"}
	if err := coordinator.Ingest(ctx, buildJob, buildResult); err != nil {
		t.Fatal(err)
	}
	build, err := repository.GetBuild(ctx, buildJob.RunID)
	if err != nil || build.Toolchain["compiler"] != "clang fixture" || build.Provenance["harness"] != "parser" {
		t.Fatalf("build=%#v err=%v", build, err)
	}
	buildDir, err := BuildDirectory(repository.Root(), campaign.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(buildDir, "fuzz_target")); err != nil || string(data) != "fixture-binary" {
		t.Fatalf("materialized data=%q err=%v", data, err)
	}
	if _, err := VerifiedBuildDirectory(ctx, repository, campaign.ID, build.ID); err != nil {
		t.Fatal(err)
	}

	crashInput, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-fuzz", Role: "crashing_input", MediaType: "application/octet-stream"}, strings.NewReader("SMIT"))
	if err != nil {
		t.Fatal(err)
	}
	log := `==8==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1
WRITE of size 1 at 0x1 thread T0
    #0 0x51933a in LLVMFuzzerTestOneInput /source/fuzz_target.cc:16:38
SUMMARY: AddressSanitizer: heap-buffer-overflow /source/fuzz_target.cc:16:38 in LLVMFuzzerTestOneInput
`
	stderr, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-fuzz", Role: "stderr_log", MediaType: "text/plain"}, strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	fuzzJob := domain.WorkerJob{RunID: "run-fuzz", CampaignID: campaign.ID, BuildID: build.ID, Operation: domain.OperationFuzz, AuditCorrelationID: "correlation"}
	fuzzResult := domain.RunResult{RunID: fuzzJob.RunID, Operation: fuzzJob.Operation, Status: domain.RunFailed, ArtifactIDs: []string{crashInput.ID, stderr.ID}}
	if err := coordinator.Ingest(ctx, fuzzJob, fuzzResult); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Ingest(ctx, fuzzJob, fuzzResult); err != nil {
		t.Fatal(err)
	}
	observations, err := repository.ListCrashes(ctx, campaign.ID, 10)
	if err != nil || len(observations) != 1 || !observations[0].SecurityRelevant || observations[0].InputArtifactID != crashInput.ID {
		t.Fatalf("observations=%#v err=%v", observations, err)
	}
	groups, err := repository.ListCrashGroups(ctx, campaign.ID, 10)
	if err != nil || len(groups) != 1 || len(groups[0].ObservationIDs) != 1 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}

	corpus, err := PrepareCorpus(repository.Root(), campaign.ID, "parser")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("corpus mode=%v", info.Mode().Perm())
	}
	materialized := filepath.Join(buildDir, "fuzz_target")
	if err := os.Chmod(materialized, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(materialized, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifiedBuildDirectory(ctx, repository, campaign.ID, build.ID); err == nil {
		t.Fatal("tampered materialized build was accepted")
	}
}

func TestCoordinatorAdvancesReplayAndMinimizationEvidence(t *testing.T) {
	ctx := context.Background()
	repository, campaign := pipelineStore(t)
	defer repository.Close()
	coordinator, err := New(repository, 3)
	if err != nil {
		t.Fatal(err)
	}
	previousVersion := campaign.Version
	campaign.State = domain.CampaignFuzzing
	campaign.Version++
	campaign.UpdatedAt = time.Now().UTC()
	if err := repository.UpdateCampaign(ctx, campaign, previousVersion); err != nil {
		t.Fatal(err)
	}

	const crashLog = `==8==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1
WRITE of size 1 at 0x1 thread T0
    #0 0x51933a in LLVMFuzzerTestOneInput /source/fuzz_target.cc:16:38
SUMMARY: AddressSanitizer: heap-buffer-overflow /source/fuzz_target.cc:16:38 in LLVMFuzzerTestOneInput
`
	original, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-fuzz-state", Role: "crashing_input", MediaType: "application/octet-stream"}, strings.NewReader("SMITH"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-fuzz-state", Role: "stderr_log", MediaType: "text/plain"}, strings.NewReader(crashLog))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveRun(ctx, domain.ExperimentRun{SchemaVersion: 1, ID: "run-fuzz-state", CampaignID: campaign.ID, ScopeID: "scope", BuildID: "build", Operation: domain.OperationFuzz, Status: domain.RunFailed, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Ingest(ctx,
		domain.WorkerJob{RunID: "run-fuzz-state", CampaignID: campaign.ID, BuildID: "build", Operation: domain.OperationFuzz, AuditCorrelationID: "fuzz"},
		domain.RunResult{RunID: "run-fuzz-state", Operation: domain.OperationFuzz, Status: domain.RunFailed, ArtifactIDs: []string{original.ID, stderr.ID}},
	); err != nil {
		t.Fatal(err)
	}
	campaign, err = repository.GetCampaign(ctx, campaign.ID)
	if err != nil || campaign.State != domain.CampaignCrashObserved {
		t.Fatalf("campaign after fuzz=%#v err=%v", campaign, err)
	}
	evidenceDir, err := PrepareEvidence(ctx, repository, campaign.ID, original.ID, domain.OperationReproduce)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(evidenceDir, "reproducer")); err != nil || string(data) != "SMITH" {
		t.Fatalf("reproducer=%q err=%v", data, err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		runID := fmt.Sprintf("run-reproduce-%d", attempt)
		attemptLog, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: runID, Role: "stderr_log", MediaType: "text/plain"}, strings.NewReader(crashLog))
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveRun(ctx, domain.ExperimentRun{SchemaVersion: 1, ID: runID, CampaignID: campaign.ID, ScopeID: "scope", BuildID: "build", InputArtifactID: original.ID, Operation: domain.OperationReproduce, Status: domain.RunFailed, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Ingest(ctx,
			domain.WorkerJob{RunID: runID, CampaignID: campaign.ID, BuildID: "build", InputArtifactID: original.ID, Operation: domain.OperationReproduce, AuditCorrelationID: runID},
			domain.RunResult{RunID: runID, Operation: domain.OperationReproduce, Status: domain.RunFailed, ArtifactIDs: []string{attemptLog.ID}},
		); err != nil {
			t.Fatal(err)
		}
	}
	campaign, err = repository.GetCampaign(ctx, campaign.ID)
	if err != nil || campaign.State != domain.CampaignReproduced {
		t.Fatalf("campaign after replay=%#v err=%v", campaign, err)
	}

	minimized, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-minimize", Role: "minimized_input", MediaType: "application/octet-stream"}, strings.NewReader("SMIT"))
	if err != nil {
		t.Fatal(err)
	}
	minimizeLog, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-minimize", Role: "stderr_log", MediaType: "text/plain"}, strings.NewReader(crashLog))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveRun(ctx, domain.ExperimentRun{SchemaVersion: 1, ID: "run-minimize", CampaignID: campaign.ID, ScopeID: "scope", BuildID: "build", InputArtifactID: original.ID, Operation: domain.OperationMinimize, Status: domain.RunFailed, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Ingest(ctx,
		domain.WorkerJob{RunID: "run-minimize", CampaignID: campaign.ID, BuildID: "build", InputArtifactID: original.ID, Operation: domain.OperationMinimize, AuditCorrelationID: "minimize"},
		domain.RunResult{RunID: "run-minimize", Operation: domain.OperationMinimize, Status: domain.RunFailed, ArtifactIDs: []string{minimized.ID, minimizeLog.ID}},
	); err != nil {
		t.Fatal(err)
	}
	campaign, err = repository.GetCampaign(ctx, campaign.ID)
	if err != nil || campaign.State != domain.CampaignMinimized {
		t.Fatalf("campaign after minimization=%#v err=%v", campaign, err)
	}
	groups, err := repository.ListCrashGroups(ctx, campaign.ID, 10)
	if err != nil || len(groups) != 1 || groups[0].MinimizedArtifactID != minimized.ID {
		t.Fatalf("groups after minimization=%#v err=%v", groups, err)
	}
	addresses, err := PrepareEvidence(ctx, repository, campaign.ID, stderr.ID, domain.OperationSymbolize)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(addresses, "addresses.txt")); err != nil || !strings.Contains(string(data), "0x51933a") {
		t.Fatalf("addresses=%q err=%v", data, err)
	}
	symbolized, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-symbolize", Role: "symbolized_stack", MediaType: "text/plain"}, strings.NewReader("LLVMFuzzerTestOneInput\n/source/fuzz_target.cc:16:38\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Ingest(ctx,
		domain.WorkerJob{RunID: "run-symbolize", CampaignID: campaign.ID, BuildID: "build", InputArtifactID: stderr.ID, Operation: domain.OperationSymbolize, AuditCorrelationID: "symbolize"},
		domain.RunResult{RunID: "run-symbolize", Operation: domain.OperationSymbolize, Status: domain.RunCompleted, ArtifactIDs: []string{symbolized.ID}},
	); err != nil {
		t.Fatal(err)
	}
	campaign, err = repository.GetCampaign(ctx, campaign.ID)
	if err != nil || campaign.State != domain.CampaignPrimitiveAssessed {
		t.Fatalf("campaign after primitive assessment=%#v err=%v", campaign, err)
	}
	primitives, err := repository.ListPrimitives(ctx, campaign.ID, 10)
	if err != nil || len(primitives) != 1 || primitives[0].Operation != domain.PrimitiveOOBWrite || primitives[0].AttackerControl.Known {
		t.Fatalf("primitives=%#v err=%v", primitives, err)
	}
	findings, err := repository.ListFindings(ctx, campaign.ID, 10)
	if err != nil || len(findings) != 1 || findings[0].Label != domain.FindingPrimitiveConfirmed || findings[0].CWE != "CWE-787" {
		t.Fatalf("findings=%#v err=%v", findings, err)
	}
	comparisonBuild := domain.Build{SchemaVersion: 1, ID: "comparison-build", CampaignID: campaign.ID, Status: string(domain.RunCompleted), Provenance: map[string]string{"target_revision": "abc"}, CreatedAt: time.Now().UTC()}
	if err := repository.SaveBuild(ctx, comparisonBuild); err != nil {
		t.Fatal(err)
	}
	comparison, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-compare", Role: "revision_comparison", MediaType: "application/json"}, strings.NewReader(`{"schema_version":1,"target_revision":"abc","reproducer_exit_code":0,"signal_absent":true}`))
	if err != nil {
		t.Fatal(err)
	}
	comparisonLog, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: "run-compare", Role: "revision_comparison_log", MediaType: "text/plain"}, strings.NewReader("completed without sanitizer signal\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Ingest(ctx,
		domain.WorkerJob{RunID: "run-compare", CampaignID: campaign.ID, BuildID: comparisonBuild.ID, InputArtifactID: groups[0].MinimizedArtifactID, Operation: domain.OperationCompareBranch, Arguments: map[string]string{"revision": "abc"}, AuditCorrelationID: "compare"},
		domain.RunResult{RunID: "run-compare", Operation: domain.OperationCompareBranch, Status: domain.RunCompleted, ArtifactIDs: []string{comparison.ID, comparisonLog.ID}},
	); err != nil {
		t.Fatal(err)
	}
	checks, err := repository.ListRevisionChecks(ctx, campaign.ID, 10)
	if err != nil || len(checks) != 1 || checks[0].FindingID != findings[0].ID || checks[0].Revision != "abc" || checks[0].Status != "unaffected" || checks[0].BuildID != comparisonBuild.ID {
		t.Fatalf("revision checks=%#v err=%v", checks, err)
	}
}

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCoordinatorBindsPatchedBuildToExactArtifactBytes(t *testing.T) {
	ctx := context.Background()
	repository, campaign := pipelineStore(t)
	defer repository.Close()
	coordinator, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "candidate_patch", MediaType: "text/x-diff"}, strings.NewReader("--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n"))
	if err != nil {
		t.Fatal(err)
	}
	makeBuildArtifacts := func(runID, patchSHA string) (domain.Artifact, domain.Artifact) {
		t.Helper()
		binary, putErr := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: runID, Role: "harness_binary", MediaType: "application/x-executable"}, strings.NewReader("fixed"))
		if putErr != nil {
			t.Fatal(putErr)
		}
		provenance, putErr := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, RunID: runID, Role: "build_provenance", MediaType: "application/json"}, strings.NewReader(fmt.Sprintf(`{"patch_sha256":%q}`, patchSHA)))
		if putErr != nil {
			t.Fatal(putErr)
		}
		return binary, provenance
	}
	binary, provenance := makeBuildArtifacts("patched-build", patch.ContentID)
	job := domain.WorkerJob{RunID: "patched-build", CampaignID: campaign.ID, PatchArtifactID: patch.ID, Operation: domain.OperationBuild, Arguments: map[string]string{"manifest": "apparatus", "harness": "parser", "revision": "abc", "sanitizer": "address"}}
	result := domain.RunResult{RunID: job.RunID, Operation: job.Operation, Status: domain.RunCompleted, ArtifactIDs: []string{binary.ID, provenance.ID}}
	if err := coordinator.Ingest(ctx, job, result); err != nil {
		t.Fatal(err)
	}
	build, err := repository.GetBuild(ctx, job.RunID)
	if err != nil || build.Provenance["patch_artifact_id"] != patch.ID || build.Provenance["patch_sha256"] != patch.ContentID {
		t.Fatalf("build=%#v err=%v", build, err)
	}
	badBinary, badProvenance := makeBuildArtifacts("mismatched-patched-build", "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	job.RunID = "mismatched-patched-build"
	result.RunID, result.ArtifactIDs = job.RunID, []string{badBinary.ID, badProvenance.ID}
	if err := coordinator.Ingest(ctx, job, result); err == nil {
		t.Fatal("build provenance for different patch bytes was accepted")
	}
}

func TestEquivalentRootCauseIgnoresBranchLineDriftButNotOperationDrift(t *testing.T) {
	left := domain.CrashObservation{Class: domain.ObservationASanMemory, BugType: "heap-buffer-overflow", Access: "write", AccessSize: 1, Frames: []domain.StackFrame{{Function: "parse(unsigned char*)", File: "/source/parser.cc", Line: 16}}}
	right := left
	right.Frames = []domain.StackFrame{{Function: "parse(unsigned char*)", File: "/alternate/parser.cc", Line: 42}}
	if !equivalentRootCause(left, right) {
		t.Fatal("line-only branch drift split the same root cause")
	}
	right.Frames[0].Function = "decode(unsigned char*)"
	if equivalentRootCause(left, right) {
		t.Fatal("different top-level operation was treated as the same root cause")
	}
	right.Frames[0].Function = ""
	if equivalentRootCause(left, right) {
		t.Fatal("a frame without a function identity was treated as the same root cause")
	}
}

func pipelineStore(t *testing.T) (*store.Store, domain.Campaign) {
	t.Helper()
	ctx := context.Background()
	repository, err := store.Open(ctx, store.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	scope := domain.AuthorizationScope{ID: "scope", OperatorID: "operator", Purpose: "test", TargetRepository: "repo", AllowedRevisions: []string{"abc"},
		WorkspaceRoots: []string{repository.Root()}, AllowedOperations: []domain.Operation{domain.OperationBuild, domain.OperationFuzz},
		Budget: domain.ResourceBudget{MaxWallSeconds: 10}, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := repository.CreateScope(ctx, scope); err != nil {
		repository.Close()
		t.Fatal(err)
	}
	campaign, err := repository.CreateCampaign(ctx, domain.Campaign{SchemaVersion: 1, ID: "campaign", ScopeID: scope.ID, Name: "test", State: domain.CampaignDraft, Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	return repository, campaign
}
