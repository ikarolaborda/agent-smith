package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/novelty"
)

func TestEvidenceOwnedWorkflowFromBranchReviewThroughDisclosure(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	reviewer := domain.Principal{ID: "reviewer", Roles: []domain.Role{domain.RoleReviewer}}
	scope := domain.AuthorizationScope{
		SchemaVersion: 1, ID: "scope", OperatorID: operator.ID, MemberIDs: []string{reviewer.ID}, Purpose: "phase five workflow",
		TargetRepository: "repo", AllowedRevisions: []string{"main", "stable"}, WorkspaceRoots: []string{repository.Root()},
		AllowedOperations:  []domain.Operation{domain.OperationRegressionTest, domain.OperationDraftReport, domain.OperationDisclose},
		ApprovalOperations: []domain.Operation{domain.OperationRegressionTest, domain.OperationDraftReport, domain.OperationDisclose},
		Budget:             domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1 << 20, MaxCPUSeconds: 60, MaxDiskBytes: 1 << 20, MaxInodes: 1024, MaxPIDs: 16, MaxConcurrent: 1}, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := repository.CreateScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	target := domain.TargetRevision{SchemaVersion: 1, ID: "target", CampaignID: "campaign", Repository: "repo", RequestedRef: "main", Commit: "main", SourceSHA256: "sha256:source", AcquiredAt: now}
	campaign, err := repository.CreateCampaign(ctx, domain.Campaign{SchemaVersion: 1, ID: "campaign", ScopeID: scope.ID, Name: "workflow", State: domain.CampaignPrimitiveAssessed, TargetID: target.ID, Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	put := func(runID, role, mediaType, body string) domain.Artifact {
		t.Helper()
		artifact, putErr := repository.PutArtifact(ctx, domain.Artifact{SchemaVersion: 1, CampaignID: campaign.ID, RunID: runID, Role: role, MediaType: mediaType, Sensitivity: "research", CreatedAt: now}, strings.NewReader(body))
		if putErr != nil {
			t.Fatal(putErr)
		}
		return artifact
	}
	original := put("original-run", "minimized_input", "application/octet-stream", "SMITA")
	negativeInput := put("seed-run", "corpus_seed", "application/octet-stream", "NOPE!")
	group := domain.CrashGroup{SchemaVersion: 1, ID: "group", CampaignID: campaign.ID, Signature: "signature", CanonicalInputID: original.ID, MinimizedArtifactID: original.ID, RootCause: "bounded fixture overflow", CreatedAt: now, UpdatedAt: now}
	if err := repository.SaveCrashGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	primitive := domain.PrimitiveAssessment{SchemaVersion: 1, ID: "primitive", CampaignID: campaign.ID, CrashGroupID: group.ID, Operation: domain.PrimitiveOOBWrite, OperationEvidence: []string{original.ID}, CreatedAt: now}
	if err := repository.SavePrimitive(ctx, primitive); err != nil {
		t.Fatal(err)
	}
	finding := domain.Finding{SchemaVersion: 1, ID: "finding", CampaignID: campaign.ID, CrashGroupID: group.ID, PrimitiveID: primitive.ID, Title: "fixture overflow", Label: domain.FindingPrimitiveConfirmed, RootCause: group.RootCause, EvidenceIDs: []string{original.ID, primitive.ID}, DisclosureStatus: "not_disclosed", CreatedAt: now, UpdatedAt: now}
	if err := repository.SaveFinding(ctx, finding); err != nil {
		t.Fatal(err)
	}
	for _, revision := range scope.AllowedRevisions {
		if _, err := svc.RecordUntestedRevision(ctx, reviewer, campaign.ID, finding.ID, revision, "comparison toolchain intentionally unavailable in unit test"); err != nil {
			t.Fatal(err)
		}
	}
	finding, err = svc.CompleteBranchReview(ctx, reviewer, campaign.ID, finding.ID)
	if err != nil || len(finding.BranchChecks) != 2 {
		t.Fatalf("branch finding=%#v err=%v", finding, err)
	}
	for index, kind := range novelty.RequiredKinds {
		response := put("", "lookup_response", "application/json", fmt.Sprintf(`{"kind":%q}`, kind))
		evidence := domain.SourceEvidence{SchemaVersion: 1, ID: fmt.Sprintf("source-%d", index), CampaignID: campaign.ID, FindingID: finding.ID, Kind: kind, SourceName: kind, Query: "fixture overflow", RequestURL: "https://example.test/search", Status: "captured", ResponseHash: response.ContentID, ArtifactID: response.ID, CheckedAt: now.Add(time.Duration(index) * time.Second)}
		if err := repository.SaveSourceEvidence(ctx, evidence); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ReviewSourceEvidence(ctx, reviewer, campaign.ID, SourceReviewRequest{FindingID: finding.ID, SourceEvidenceID: evidence.ID, Status: "no_match", Summary: "no matching root cause in retained response"}); err != nil {
			t.Fatal(err)
		}
	}
	finding, err = svc.CompleteNoveltyReview(ctx, reviewer, campaign.ID, finding.ID)
	if err != nil || finding.NoveltyStatus != "novelty_unverified" || len(finding.NoveltyChecks) != len(novelty.RequiredKinds) {
		t.Fatalf("novelty finding=%#v err=%v", finding, err)
	}
	approved := func(id string, operation domain.Operation, correlation string) domain.Approval {
		t.Helper()
		decided := now
		approval := domain.Approval{SchemaVersion: 1, ID: id, ScopeID: scope.ID, CampaignID: campaign.ID, CorrelationID: correlation, Operation: operation, Status: "approved", RequestedBy: operator.ID, DecidedBy: reviewer.ID, Reason: "reviewed", CreatedAt: now, DecidedAt: &decided}
		if err := repository.SaveApproval(ctx, approval); err != nil {
			t.Fatal(err)
		}
		return approval
	}
	patchApproval := approved("approval-patch", domain.OperationRegressionTest, "patch:"+finding.ID)
	patch, err := svc.CreateCandidatePatch(ctx, operator, campaign.ID, CandidatePatchRequest{FindingID: finding.ID, ApprovalID: patchApproval.ID, Diff: "diff --git a/file.cc b/file.cc\n--- a/file.cc\n+++ b/file.cc\n@@ -1 +1 @@\n-old\n+new\n"})
	if err != nil || patch.Role != "candidate_patch" {
		t.Fatalf("patch=%#v err=%v", patch, err)
	}
	if _, err := svc.CreateCandidatePatch(ctx, operator, campaign.ID, CandidatePatchRequest{FindingID: finding.ID, ApprovalID: patchApproval.ID, Diff: "--- ../../escape\n+++ b/file\n@@ -1 +1 @@\n-a\n+b\n"}); err == nil {
		t.Fatal("unsafe patch was accepted")
	}
	buildBinary := put("fix-build", "harness_binary", "application/x-executable", "fixed-binary")
	buildLog := put("fix-build", "build_provenance", "application/json", `{}`)
	fixBuild := domain.Build{SchemaVersion: 1, ID: "fix-build", CampaignID: campaign.ID, TargetID: target.ID, Status: string(domain.RunCompleted), OutputArtifacts: []string{buildBinary.ID}, LogArtifacts: []string{buildLog.ID}, Provenance: map[string]string{"patch_artifact_id": patch.ID}, CreatedAt: now, CompletedAt: &now}
	if err := repository.SaveBuild(ctx, fixBuild); err != nil {
		t.Fatal(err)
	}
	alternateBuild := fixBuild
	alternateBuild.ID = "alternate-fix-build"
	alternateBuild.TargetID = "alternate-target"
	if err := repository.SaveBuild(ctx, alternateBuild); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateRemediation(ctx, reviewer, campaign.ID, RemediationRequest{FindingID: finding.ID, PatchArtifactID: patch.ID, ApprovalID: patchApproval.ID, FixBuildID: alternateBuild.ID, ReproducerRunID: "missing-reproducer", RegressionRunID: "missing-regression", NegativeControlRunID: "missing-negative"}); err == nil {
		t.Fatal("alternate-revision build was accepted as primary-target remediation")
	}
	makeValidationRun := func(id, kind, inputID string) domain.ExperimentRun {
		t.Helper()
		result := put(id, "regression_result", "application/json", fmt.Sprintf(`{"schema_version":1,"target_revision":"main","validation_kind":%q,"exit_code":0,"signal_absent":true}`, kind))
		log := put(id, "regression_log", "text/plain", "clean\n")
		run := domain.ExperimentRun{SchemaVersion: 1, ID: id, CampaignID: campaign.ID, ScopeID: scope.ID, BuildID: fixBuild.ID, InputArtifactID: inputID, Operation: domain.OperationRegressionTest, Arguments: map[string]string{"revision": "main", "validation-kind": kind}, Status: domain.RunCompleted, Exit: domain.RunExit{Code: 0}, ArtifactIDs: []string{result.ID, log.ID}, CreatedAt: now, CompletedAt: &now}
		if err := repository.SaveRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}
	reproducer := makeValidationRun("fix-reproducer", "reproducer", original.ID)
	regressionRun := makeValidationRun("fix-regression", "regression", "")
	negative := makeValidationRun("fix-negative", "negative_control", negativeInput.ID)
	validation, err := svc.ValidateRemediation(ctx, reviewer, campaign.ID, RemediationRequest{FindingID: finding.ID, PatchArtifactID: patch.ID, ApprovalID: patchApproval.ID, FixBuildID: fixBuild.ID, ReproducerRunID: reproducer.ID, RegressionRunID: regressionRun.ID, NegativeControlRunID: negative.ID})
	if err != nil || !validation.OriginalSignalGone || !validation.RegressionPassed || !validation.NegativeControlClean {
		t.Fatalf("validation=%#v err=%v", validation, err)
	}
	reportApproval := approved("approval-report", domain.OperationDraftReport, "report:"+finding.ID)
	reportArtifact, err := svc.CreatePrivateReport(ctx, reviewer, campaign.ID, ReportDraftRequest{FindingID: finding.ID, ApprovalID: reportApproval.ID})
	if err != nil || reportArtifact.Role != "private_report" || reportArtifact.Sensitivity != "private_disclosure" {
		t.Fatalf("report=%#v err=%v", reportArtifact, err)
	}
	_, reportFile, err := repository.OpenArtifact(ctx, reportArtifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	reportBody, err := io.ReadAll(reportFile)
	reportFile.Close()
	if err != nil || !strings.Contains(string(reportBody), "Novelty: `novelty_unverified`") || !strings.Contains(string(reportBody), "Disclosure: not performed") {
		t.Fatalf("report body=%q err=%v", reportBody, err)
	}
	disclosureApproval := approved("approval-disclosure", domain.OperationDisclose, "disclosure:"+finding.ID)
	finding, err = svc.RecordDisclosure(ctx, reviewer, campaign.ID, DisclosureRecordRequest{FindingID: finding.ID, ApprovalID: disclosureApproval.ID, Reference: "vendor-ticket-123"})
	if err != nil || finding.DisclosureStatus != "disclosed" || finding.DisclosureReference != "vendor-ticket-123" || finding.DisclosedAt == nil {
		t.Fatalf("disclosed finding=%#v err=%v", finding, err)
	}
	campaign, err = repository.GetCampaign(ctx, campaign.ID)
	if err != nil || campaign.State != domain.CampaignDisclosed {
		t.Fatalf("final campaign=%#v err=%v", campaign, err)
	}
	if err := repository.VerifyAuditChain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireComparisonTargetRetainsAlternateRevisionWithoutReplacingPrimary(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	now := time.Now().UTC()
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	sourceRoot := t.TempDir()
	source := sourceRoot + "/stable"
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source+"/fixture.cc", []byte("int stable = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	internalRoot := repository.Root() + "/worker-inputs"
	if err := os.MkdirAll(internalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfigureInternalRoot(internalRoot); err != nil {
		t.Fatal(err)
	}
	scope := domain.AuthorizationScope{SchemaVersion: 1, ID: "scope-alt", OperatorID: operator.ID, Purpose: "supported revision", TargetRepository: "repo", AllowedRevisions: []string{"main", "stable"}, WorkspaceRoots: []string{sourceRoot}, AllowedOperations: []domain.Operation{domain.OperationAcquire}, Budget: domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1 << 20, MaxCPUSeconds: 60, MaxDiskBytes: 1 << 20, MaxInodes: 1024, MaxPIDs: 16, MaxConcurrent: 1}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := repository.CreateScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	campaign, err := repository.CreateCampaign(ctx, domain.Campaign{SchemaVersion: 1, ID: "campaign-alt", ScopeID: scope.ID, Name: "alternate", State: domain.CampaignPrimitiveAssessed, TargetID: "primary-target", Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveFinding(ctx, domain.Finding{SchemaVersion: 1, ID: "finding-alt", CampaignID: campaign.ID, CrashGroupID: "group", Title: "finding", Label: domain.FindingPrimitiveConfirmed, DisclosureStatus: "not_disclosed", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := TargetRequest{Repository: "repo", Revision: "stable", SourceDir: source, Language: "c++", Architecture: "amd64", CorrelationID: "target:finding-alt:stable"}
	target, err := svc.AcquireComparisonTarget(ctx, operator, campaign.ID, "finding-alt", request)
	if err != nil || target.Commit != "stable" || target.ID == campaign.TargetID {
		t.Fatalf("alternate target=%#v err=%v", target, err)
	}
	current, err := repository.GetCampaign(ctx, campaign.ID)
	if err != nil || current.TargetID != "primary-target" || current.State != domain.CampaignPrimitiveAssessed || current.Version != campaign.Version {
		t.Fatalf("campaign changed=%#v err=%v", current, err)
	}
	targets, err := repository.ListTargets(ctx, campaign.ID, 10)
	if err != nil || len(targets) != 1 || targets[0].ID != target.ID {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
}
