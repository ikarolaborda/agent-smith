package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/novelty"
	"github.com/ikarolaborda/agent-smith/internal/research/remediation"
	"github.com/ikarolaborda/agent-smith/internal/research/report"
	"github.com/ikarolaborda/agent-smith/internal/research/repository"
)

const workflowQueryLimit = 10000

// SourceReviewRequest is an interpretation of already-captured lookup bytes.
// Callers cannot create source evidence through this path.
type SourceReviewRequest struct {
	FindingID        string `json:"finding_id"`
	SourceEvidenceID string `json:"source_evidence_id"`
	Status           string `json:"status"`
	Summary          string `json:"summary"`
}

// AuthorizeNoveltyLookup gates one fixed-source egress action before the
// server-owned lookup broker performs it.
func (s *Service) AuthorizeNoveltyLookup(ctx context.Context, actor domain.Principal, campaignID, findingID, sourceName, destinationHost, approvalID string) (string, error) {
	campaign, scope, _, err := s.workflowFinding(ctx, actor, campaignID, findingID, "novelty.lookup", domain.RoleOperator, domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return "", err
	}
	if campaign.State != domain.CampaignBranchChecked && campaign.State != domain.CampaignNoveltyReviewed {
		return "", s.denied(ctx, actor, "novelty.lookup", "campaign", campaignID, "branch review must complete before external novelty lookups")
	}
	target, err := s.store.GetTarget(ctx, campaign.TargetID)
	if err != nil {
		return "", err
	}
	sourceName, destinationHost = strings.TrimSpace(sourceName), strings.TrimSpace(destinationHost)
	if sourceName == "" || destinationHost == "" {
		return "", s.denied(ctx, actor, "novelty.lookup", "finding", findingID, "fixed source identity and destination required")
	}
	correlationID := "lookup:" + findingID + ":" + sourceName
	decision, err := s.AuthorizeAction(ctx, campaignID, domain.Action{
		Principal: actor, Operation: domain.OperationNoveltyLookup, Repository: scope.TargetRepository, Revision: target.Commit,
		DestinationHost: destinationHost, ApprovalID: approvalID,
	}, correlationID)
	if err != nil {
		return "", err
	}
	if !decision.Allowed {
		return "", s.denied(ctx, actor, "novelty.lookup", "finding", findingID, decision.Reason)
	}
	if err := s.allowedWithDetails(ctx, actor, "novelty.lookup", "finding", findingID, correlationID, map[string]string{"campaign_id": campaignID, "source_name": sourceName, "destination_host": destinationHost}); err != nil {
		return "", err
	}
	return correlationID, nil
}

// RemediationRequest names durable build/run evidence. Outcome booleans are
// deliberately absent: the service derives them from retained runner records.
type RemediationRequest struct {
	FindingID            string `json:"finding_id"`
	PatchArtifactID      string `json:"patch_artifact_id"`
	ApprovalID           string `json:"approval_id"`
	FixBuildID           string `json:"fix_build_id"`
	ReproducerRunID      string `json:"reproducer_run_id"`
	RegressionRunID      string `json:"regression_run_id"`
	NegativeControlRunID string `json:"negative_control_run_id"`
}

// CandidatePatchRequest retains a bounded unified diff without applying it on
// the control-plane host. A later isolated build must bind to its artifact ID.
type CandidatePatchRequest struct {
	FindingID  string `json:"finding_id"`
	ApprovalID string `json:"approval_id"`
	Diff       string `json:"diff"`
}

// ReportDraftRequest creates a private local artifact; it never transmits it.
type ReportDraftRequest struct {
	FindingID  string `json:"finding_id"`
	ApprovalID string `json:"approval_id"`
}

// DisclosureRecordRequest records a human action that already happened. The
// control plane has no outbound disclosure transport.
type DisclosureRecordRequest struct {
	FindingID  string `json:"finding_id"`
	ApprovalID string `json:"approval_id"`
	Reference  string `json:"reference"`
}

func (s *Service) workflowStore() (repository.WorkflowEvidence, error) {
	workflow, ok := s.store.(repository.WorkflowEvidence)
	if !ok {
		return nil, errors.New("research service: workflow evidence repository unavailable")
	}
	return workflow, nil
}

func (s *Service) workflowFinding(ctx context.Context, actor domain.Principal, campaignID, findingID, action string, roles ...domain.Role) (domain.Campaign, domain.AuthorizationScope, domain.Finding, error) {
	if !hasAnyRole(actor, roles...) {
		return domain.Campaign{}, domain.AuthorizationScope{}, domain.Finding{}, s.denied(ctx, actor, action, "finding", findingID, "required research role missing")
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return campaign, domain.AuthorizationScope{}, domain.Finding{}, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return campaign, scope, domain.Finding{}, err
	}
	if err := scope.Validate(s.now()); err != nil {
		return campaign, scope, domain.Finding{}, s.denied(ctx, actor, action, "campaign", campaignID, err.Error())
	}
	if !scopeMember(scope, actor) {
		return campaign, scope, domain.Finding{}, s.denied(ctx, actor, action, "campaign", campaignID, "principal is not a campaign member")
	}
	finding, err := s.store.GetFinding(ctx, findingID)
	if err != nil {
		return campaign, scope, finding, err
	}
	if finding.CampaignID != campaignID {
		return campaign, scope, finding, s.denied(ctx, actor, action, "finding", findingID, "finding belongs to another campaign")
	}
	return campaign, scope, finding, nil
}

// RecordUntestedRevision records a reviewer-owned exception. It cannot assert
// affected/unaffected status and therefore cannot impersonate worker evidence.
func (s *Service) RecordUntestedRevision(ctx context.Context, actor domain.Principal, campaignID, findingID, revision, reason string) (domain.RevisionCheck, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, findingID, "revision.untested", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return domain.RevisionCheck{}, err
	}
	revision, reason = strings.TrimSpace(revision), strings.TrimSpace(reason)
	if campaign.State != domain.CampaignPrimitiveAssessed && campaign.State != domain.CampaignBranchChecked {
		return domain.RevisionCheck{}, s.denied(ctx, actor, "revision.untested", "campaign", campaignID, "campaign is not awaiting branch review")
	}
	if finding.Label != domain.FindingPrimitiveConfirmed {
		return domain.RevisionCheck{}, s.denied(ctx, actor, "revision.untested", "finding", findingID, "primitive-confirmed finding required")
	}
	if !containsString(scope.AllowedRevisions, revision) || reason == "" || len(reason) > 2048 {
		return domain.RevisionCheck{}, s.denied(ctx, actor, "revision.untested", "finding", findingID, "authorized revision and bounded explicit reason required")
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return domain.RevisionCheck{}, err
	}
	check := domain.RevisionCheck{
		SchemaVersion: 1,
		ID:            deterministicID("revision", campaignID, findingID, revision, reason),
		CampaignID:    campaignID,
		FindingID:     findingID,
		Revision:      revision,
		Status:        "untested",
		Reason:        reason,
		CheckedAt:     s.now(),
	}
	if err := workflow.SaveRevisionCheck(ctx, check); err != nil {
		return check, err
	}
	return check, s.allowedWithDetails(ctx, actor, "revision.untested", "revision_check", check.ID, "", map[string]string{"campaign_id": campaignID, "finding_id": findingID, "revision": revision})
}

// CompleteBranchReview advances only after every authorized supported revision
// has machine evidence or a reviewer-authored explicit untested reason.
func (s *Service) CompleteBranchReview(ctx context.Context, actor domain.Principal, campaignID, findingID string) (domain.Finding, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, findingID, "branch.complete", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return finding, err
	}
	if campaign.State != domain.CampaignPrimitiveAssessed && campaign.State != domain.CampaignBranchChecked {
		return finding, s.denied(ctx, actor, "branch.complete", "campaign", campaignID, "campaign is not awaiting branch review")
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return finding, err
	}
	checks, err := workflow.ListRevisionChecks(ctx, campaignID, workflowQueryLimit)
	if err != nil {
		return finding, err
	}
	checks = filterRevisionChecks(checks, findingID)
	facts, gates := novelty.BranchFacts(scope.AllowedRevisions, checks)
	if !facts.BranchChecksRecorded {
		return finding, s.denied(ctx, actor, "branch.complete", "finding", findingID, "every supported revision needs evidence or an explicit untested reason")
	}
	updatedCampaign, err := s.machine.Advance(campaign, domain.CampaignBranchChecked, facts, s.now())
	if err != nil {
		return finding, s.denied(ctx, actor, "branch.complete", "campaign", campaignID, err.Error())
	}
	finding.BranchChecks = gates
	finding.AffectedRevisions = affectedRevisions(gates)
	finding.UpdatedAt = s.now()
	if err := s.store.SaveFinding(ctx, finding); err != nil {
		return finding, err
	}
	if updatedCampaign.Version != campaign.Version {
		if err := s.store.UpdateCampaign(ctx, updatedCampaign, campaign.Version); err != nil {
			return finding, err
		}
	}
	return finding, s.allowedWithDetails(ctx, actor, "branch.complete", "finding", findingID, "", map[string]string{"campaign_id": campaignID, "checks": fmt.Sprint(len(gates))})
}

// ReviewSourceEvidence saves an immutable interpretation of captured lookup
// evidence. Raw clients cannot submit their own response body or URL here.
func (s *Service) ReviewSourceEvidence(ctx context.Context, actor domain.Principal, campaignID string, request SourceReviewRequest) (domain.SourceReview, error) {
	_, _, _, err := s.workflowFinding(ctx, actor, campaignID, request.FindingID, "source.review", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return domain.SourceReview{}, err
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return domain.SourceReview{}, err
	}
	evidence, err := workflow.GetSourceEvidence(ctx, strings.TrimSpace(request.SourceEvidenceID))
	if err != nil {
		return domain.SourceReview{}, err
	}
	status, summary := strings.TrimSpace(request.Status), strings.TrimSpace(request.Summary)
	if evidence.CampaignID != campaignID || evidence.FindingID != request.FindingID || !containsString(novelty.RequiredKinds, evidence.Kind) {
		return domain.SourceReview{}, s.denied(ctx, actor, "source.review", "source_evidence", evidence.ID, "lookup evidence does not belong to this required finding check")
	}
	if !validReviewStatus(status) || summary == "" || len(summary) > 4096 {
		return domain.SourceReview{}, s.denied(ctx, actor, "source.review", "source_evidence", evidence.ID, "valid status and bounded review summary required")
	}
	if (status == "match" || status == "no_match") && (evidence.ArtifactID == "" || evidence.ResponseHash == "") {
		return domain.SourceReview{}, s.denied(ctx, actor, "source.review", "source_evidence", evidence.ID, "match decisions require captured response bytes")
	}
	review := domain.SourceReview{
		SchemaVersion:    1,
		ID:               newID("review"),
		CampaignID:       campaignID,
		FindingID:        request.FindingID,
		SourceEvidenceID: evidence.ID,
		Kind:             evidence.Kind,
		Status:           status,
		Summary:          summary,
		ReviewedBy:       actor.ID,
		ReviewedAt:       s.now(),
	}
	if err := workflow.SaveSourceReview(ctx, review); err != nil {
		return review, err
	}
	return review, s.allowedWithDetails(ctx, actor, "source.review", "source_review", review.ID, "", map[string]string{"campaign_id": campaignID, "finding_id": request.FindingID, "status": status})
}

// CompleteNoveltyReview records a conservative decision. Exhaustive no-match
// results remain novelty_unverified; this service never returns "novel".
func (s *Service) CompleteNoveltyReview(ctx context.Context, actor domain.Principal, campaignID, findingID string) (domain.Finding, error) {
	campaign, _, finding, err := s.workflowFinding(ctx, actor, campaignID, findingID, "novelty.complete", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return finding, err
	}
	if campaign.State != domain.CampaignBranchChecked && campaign.State != domain.CampaignNoveltyReviewed {
		return finding, s.denied(ctx, actor, "novelty.complete", "campaign", campaignID, "branch review must complete first")
	}
	if len(finding.BranchChecks) == 0 {
		return finding, s.denied(ctx, actor, "novelty.complete", "finding", findingID, "durable branch checks required")
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return finding, err
	}
	evidence, err := workflow.ListSourceEvidence(ctx, campaignID, workflowQueryLimit)
	if err != nil {
		return finding, err
	}
	reviews, err := workflow.ListSourceReviews(ctx, campaignID, workflowQueryLimit)
	if err != nil {
		return finding, err
	}
	evidence = filterSourceEvidence(evidence, findingID)
	reviews = filterSourceReviews(reviews, findingID)
	decision := novelty.Evaluate(evidence, reviews)
	if !decision.Complete {
		return finding, s.denied(ctx, actor, "novelty.complete", "finding", findingID, "all required source kinds need retained, reviewed lookup records")
	}
	facts := novelty.NoveltyFacts(decision)
	updatedCampaign, err := s.machine.Advance(campaign, domain.CampaignNoveltyReviewed, facts, s.now())
	if err != nil {
		return finding, s.denied(ctx, actor, "novelty.complete", "campaign", campaignID, err.Error())
	}
	finding.NoveltyChecks = decision.Checks
	finding.NoveltyStatus = decision.Status
	finding.UpdatedAt = s.now()
	if err := s.store.SaveFinding(ctx, finding); err != nil {
		return finding, err
	}
	if updatedCampaign.Version != campaign.Version {
		if err := s.store.UpdateCampaign(ctx, updatedCampaign, campaign.Version); err != nil {
			return finding, err
		}
	}
	return finding, s.allowedWithDetails(ctx, actor, "novelty.complete", "finding", findingID, "", map[string]string{"campaign_id": campaignID, "novelty_status": decision.Status})
}

// CreateCandidatePatch stores a reviewer-approved unified diff as private,
// immutable evidence. It does not modify a checkout or execute patch tooling.
func (s *Service) CreateCandidatePatch(ctx context.Context, actor domain.Principal, campaignID string, request CandidatePatchRequest) (domain.Artifact, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, request.FindingID, "patch.create", domain.RoleOperator, domain.RoleAdmin)
	if err != nil {
		return domain.Artifact{}, err
	}
	if campaign.State != domain.CampaignNoveltyReviewed {
		return domain.Artifact{}, s.denied(ctx, actor, "patch.create", "campaign", campaignID, "novelty review must complete before patch creation")
	}
	if !containsOperation(scope.AllowedOperations, domain.OperationRegressionTest) {
		return domain.Artifact{}, s.denied(ctx, actor, "patch.create", "campaign", campaignID, "regression validation is outside authorization scope")
	}
	approval, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return domain.Artifact{}, err
	}
	if err := matchingApproval(approval, campaignID, domain.OperationRegressionTest, "patch:"+finding.ID); err != nil {
		return domain.Artifact{}, s.denied(ctx, actor, "patch.create", "approval", approval.ID, err.Error())
	}
	diff := strings.ReplaceAll(request.Diff, "\r\n", "\n")
	if err := validateUnifiedDiff(diff); err != nil {
		return domain.Artifact{}, s.denied(ctx, actor, "patch.create", "finding", finding.ID, err.Error())
	}
	artifact, err := s.store.PutArtifact(ctx, domain.Artifact{
		SchemaVersion: 1,
		CampaignID:    campaignID,
		ParentIDs:     []string{finding.ID, approval.ID},
		Role:          "candidate_patch",
		MediaType:     "text/x-diff; charset=utf-8",
		Sensitivity:   "private_disclosure",
		CreatedAt:     s.now(),
	}, strings.NewReader(diff))
	if err != nil {
		return domain.Artifact{}, err
	}
	return artifact, s.allowedWithDetails(ctx, actor, "patch.create", "artifact", artifact.ID, approval.CorrelationID, map[string]string{"campaign_id": campaignID, "finding_id": finding.ID})
}

// ValidateRemediation derives all outcomes from durable artifacts, builds,
// runs, and parsed observations, then advances the campaign on a clean result.
func (s *Service) ValidateRemediation(ctx context.Context, actor domain.Principal, campaignID string, request RemediationRequest) (domain.RemediationValidation, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, request.FindingID, "remediation.validate", domain.RoleOperator, domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if campaign.State != domain.CampaignNoveltyReviewed && campaign.State != domain.CampaignRemediated {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "campaign", campaignID, "novelty review must complete first")
	}
	if !containsOperation(scope.AllowedOperations, domain.OperationRegressionTest) {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "campaign", campaignID, "regression validation is outside authorization scope")
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	patch, err := s.store.GetArtifact(ctx, request.PatchArtifactID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if patch.CampaignID != campaignID || patch.Role != "candidate_patch" {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "artifact", patch.ID, "campaign-owned candidate patch artifact required")
	}
	approval, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if err := matchingApproval(approval, campaignID, domain.OperationRegressionTest, "patch:"+finding.ID); err != nil {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "approval", approval.ID, err.Error())
	}
	fixBuild, err := s.store.GetBuild(ctx, request.FixBuildID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if fixBuild.TargetID != campaign.TargetID || fixBuild.Provenance["patch_artifact_id"] != patch.ID {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "build", fixBuild.ID, "fix build is not linked to the primary target and candidate patch")
	}
	reproducer, err := workflow.GetRun(ctx, request.ReproducerRunID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	regressionRun, err := workflow.GetRun(ctx, request.RegressionRunID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	negative, err := workflow.GetRun(ctx, request.NegativeControlRunID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if !distinctStrings(reproducer.ID, regressionRun.ID, negative.ID) ||
		!validFixRun(reproducer, campaignID, fixBuild.ID, "reproducer") || !validFixRun(regressionRun, campaignID, fixBuild.ID, "regression") || !validFixRun(negative, campaignID, fixBuild.ID, "negative_control") ||
		reproducer.InputArtifactID == "" || regressionRun.InputArtifactID != "" || negative.InputArtifactID == "" || reproducer.InputArtifactID == negative.InputArtifactID {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "finding", finding.ID, "three distinct fix-build regression runs and a distinct negative-control input required")
	}
	for _, item := range []struct {
		run  domain.ExperimentRun
		kind string
	}{{reproducer, "reproducer"}, {regressionRun, "regression"}, {negative, "negative_control"}} {
		if err := s.validateRegressionResult(ctx, item.run, item.kind); err != nil {
			return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "run", item.run.ID, err.Error())
		}
	}
	group, err := workflow.GetCrashGroup(ctx, finding.CrashGroupID)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	if reproducer.InputArtifactID != group.MinimizedArtifactID && reproducer.InputArtifactID != group.CanonicalInputID {
		return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "run", reproducer.ID, "reproducer does not use the canonical or minimized crashing input")
	}
	observations, err := workflow.ListCrashes(ctx, campaignID, workflowQueryLimit)
	if err != nil {
		return domain.RemediationValidation{}, err
	}
	originalSignatureSeen := false
	for _, observation := range observations {
		if observation.RunID == reproducer.ID && observation.Signature == group.Signature {
			originalSignatureSeen = true
			break
		}
	}
	evidenceIDs := uniqueStrings(append(append(append(append([]string{patch.ID}, fixBuild.OutputArtifacts...), fixBuild.LogArtifacts...), reproducer.ArtifactIDs...), append(regressionRun.ArtifactIDs, negative.ArtifactIDs...)...))
	for _, artifactID := range evidenceIDs {
		artifact, artifactErr := s.store.GetArtifact(ctx, artifactID)
		if artifactErr != nil || artifact.CampaignID != campaignID {
			return domain.RemediationValidation{}, s.denied(ctx, actor, "remediation.validate", "artifact", artifactID, "all remediation evidence must be retained in this campaign")
		}
	}
	validation, facts, err := remediation.Validate(remediation.Inputs{
		ID:         deterministicID("remediation", campaignID, finding.ID, patch.ID, fixBuild.ID, reproducer.ID, regressionRun.ID, negative.ID),
		CampaignID: campaignID, FindingID: finding.ID, PatchArtifactID: patch.ID, Approval: approval, FixBuild: fixBuild,
		ReproducerRun: reproducer, RegressionRun: regressionRun, NegativeControlRun: negative,
		OriginalSignatureSeen: originalSignatureSeen, EvidenceIDs: evidenceIDs, Now: s.now(),
	})
	if err != nil {
		return validation, s.denied(ctx, actor, "remediation.validate", "finding", finding.ID, err.Error())
	}
	updatedCampaign, err := s.machine.Advance(campaign, domain.CampaignRemediated, facts, s.now())
	if err != nil {
		return validation, s.denied(ctx, actor, "remediation.validate", "campaign", campaignID, err.Error())
	}
	if err := workflow.SaveRemediation(ctx, validation); err != nil {
		return validation, err
	}
	finding.FixArtifactID = patch.ID
	finding.RegressionRunID = regressionRun.ID
	finding.UpdatedAt = s.now()
	if err := s.store.SaveFinding(ctx, finding); err != nil {
		return validation, err
	}
	if updatedCampaign.Version != campaign.Version {
		if err := s.store.UpdateCampaign(ctx, updatedCampaign, campaign.Version); err != nil {
			return validation, err
		}
	}
	return validation, s.allowedWithDetails(ctx, actor, "remediation.validate", "remediation_validation", validation.ID, approval.CorrelationID, map[string]string{"campaign_id": campaignID, "finding_id": finding.ID})
}

// CreatePrivateReport renders and stores an evidence-linked local draft. It
// promotes the finding only after branch, novelty, and human approval gates.
func (s *Service) CreatePrivateReport(ctx context.Context, actor domain.Principal, campaignID string, request ReportDraftRequest) (domain.Artifact, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, request.FindingID, "report.create", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return domain.Artifact{}, err
	}
	if campaign.State != domain.CampaignRemediated && campaign.State != domain.CampaignReportReady {
		return domain.Artifact{}, s.denied(ctx, actor, "report.create", "campaign", campaignID, "validated remediation required before reporting")
	}
	if !containsOperation(scope.AllowedOperations, domain.OperationDraftReport) {
		return domain.Artifact{}, s.denied(ctx, actor, "report.create", "campaign", campaignID, "report drafting is outside authorization scope")
	}
	approval, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return domain.Artifact{}, err
	}
	if err := matchingApproval(approval, campaignID, domain.OperationDraftReport, "report:"+finding.ID); err != nil {
		return domain.Artifact{}, s.denied(ctx, actor, "report.create", "approval", approval.ID, err.Error())
	}
	if finding.ReportArtifactID != "" {
		return s.store.GetArtifact(ctx, finding.ReportArtifactID)
	}
	workflow, err := s.workflowStore()
	if err != nil {
		return domain.Artifact{}, err
	}
	group, err := workflow.GetCrashGroup(ctx, finding.CrashGroupID)
	if err != nil {
		return domain.Artifact{}, err
	}
	primitive, err := workflow.GetPrimitive(ctx, finding.PrimitiveID)
	if err != nil {
		return domain.Artifact{}, err
	}
	target, err := s.store.GetTarget(ctx, campaign.TargetID)
	if err != nil {
		return domain.Artifact{}, err
	}
	facts := domain.EvidenceFacts{BranchChecksRecorded: len(finding.BranchChecks) > 0, NoveltyChecksRecorded: len(finding.NoveltyChecks) > 0, HumanReviewApprovalID: approval.ID}
	promoted, err := s.machine.PromoteFinding(finding, domain.FindingCandidateVulnerability, facts, s.now())
	if err != nil {
		return domain.Artifact{}, s.denied(ctx, actor, "report.create", "finding", finding.ID, err.Error())
	}
	artifacts, err := workflow.ListArtifacts(ctx, campaignID, workflowQueryLimit)
	if err != nil {
		return domain.Artifact{}, err
	}
	artifacts = relevantArtifacts(artifacts, finding, group, primitive)
	markdown, err := report.RenderPrivateDraft(report.DraftInputs{
		Campaign: campaign, Target: target, Finding: promoted, Group: group, Primitive: primitive,
		Artifacts: artifacts, Reviewer: approval, NoveltyStatus: finding.NoveltyStatus,
	})
	if err != nil {
		return domain.Artifact{}, err
	}
	reportArtifact, err := s.store.PutArtifact(ctx, domain.Artifact{
		SchemaVersion: 1, CampaignID: campaignID, ParentIDs: uniqueStrings(append([]string{finding.ID, approval.ID}, finding.EvidenceIDs...)),
		Role: "private_report", MediaType: "text/markdown; charset=utf-8", Sensitivity: "private_disclosure", CreatedAt: s.now(),
	}, bytes.NewBufferString(markdown))
	if err != nil {
		return domain.Artifact{}, err
	}
	promoted.ReportArtifactID = reportArtifact.ID
	promoted.HumanReviewApproval = approval.ID
	promoted.UpdatedAt = s.now()
	if err := s.store.SaveFinding(ctx, promoted); err != nil {
		return domain.Artifact{}, err
	}
	updatedCampaign, err := s.machine.Advance(campaign, domain.CampaignReportReady, facts, s.now())
	if err != nil {
		return domain.Artifact{}, err
	}
	if updatedCampaign.Version != campaign.Version {
		if err := s.store.UpdateCampaign(ctx, updatedCampaign, campaign.Version); err != nil {
			return domain.Artifact{}, err
		}
	}
	return reportArtifact, s.allowedWithDetails(ctx, actor, "report.create", "artifact", reportArtifact.ID, approval.CorrelationID, map[string]string{"campaign_id": campaignID, "finding_id": finding.ID})
}

// RecordDisclosure persists a reviewer-authenticated record of external human
// disclosure. It performs no network request and requires a separate approval.
func (s *Service) RecordDisclosure(ctx context.Context, actor domain.Principal, campaignID string, request DisclosureRecordRequest) (domain.Finding, error) {
	campaign, scope, finding, err := s.workflowFinding(ctx, actor, campaignID, request.FindingID, "disclosure.record", domain.RoleReviewer, domain.RoleAdmin)
	if err != nil {
		return finding, err
	}
	reference := strings.TrimSpace(request.Reference)
	if campaign.State != domain.CampaignReportReady || finding.ReportArtifactID == "" || reference == "" || len(reference) > 2048 {
		return finding, s.denied(ctx, actor, "disclosure.record", "finding", finding.ID, "report-ready finding and bounded human disclosure reference required")
	}
	approval, err := s.store.GetApproval(ctx, request.ApprovalID)
	if err != nil {
		return finding, err
	}
	if err := matchingApproval(approval, campaignID, domain.OperationDisclose, "disclosure:"+finding.ID); err != nil {
		return finding, s.denied(ctx, actor, "disclosure.record", "approval", approval.ID, err.Error())
	}
	target, err := s.store.GetTarget(ctx, campaign.TargetID)
	if err != nil {
		return finding, err
	}
	decision, err := s.AuthorizeAction(ctx, campaignID, domain.Action{Principal: actor, Operation: domain.OperationDisclose, Repository: scope.TargetRepository, Revision: target.Commit, ApprovalID: approval.ID}, approval.CorrelationID)
	if err != nil {
		return finding, err
	}
	if !decision.Allowed {
		return finding, s.denied(ctx, actor, "disclosure.record", "finding", finding.ID, decision.Reason)
	}
	facts := domain.EvidenceFacts{DisclosureApprovalID: approval.ID, HumanPerformedDisclosure: true}
	updatedCampaign, err := s.machine.Advance(campaign, domain.CampaignDisclosed, facts, s.now())
	if err != nil {
		return finding, s.denied(ctx, actor, "disclosure.record", "campaign", campaignID, err.Error())
	}
	now := s.now()
	finding.DisclosureApproval = approval.ID
	finding.DisclosureStatus = "disclosed"
	finding.DisclosureReference = reference
	finding.DisclosedAt = &now
	finding.UpdatedAt = now
	if err := s.store.SaveFinding(ctx, finding); err != nil {
		return finding, err
	}
	if err := s.store.UpdateCampaign(ctx, updatedCampaign, campaign.Version); err != nil {
		return finding, err
	}
	return finding, s.allowedWithDetails(ctx, actor, "disclosure.record", "finding", finding.ID, approval.CorrelationID, map[string]string{"campaign_id": campaignID, "reference": reference})
}

func matchingApproval(approval domain.Approval, campaignID string, operation domain.Operation, correlationID string) error {
	if approval.ID == "" || approval.Status != "approved" || approval.CampaignID != campaignID || approval.Operation != operation || approval.CorrelationID != correlationID || approval.DecidedAt == nil || approval.DecidedBy == "" {
		return errors.New("matching approved human decision required")
	}
	return nil
}

func validReviewStatus(status string) bool {
	return status == "match" || status == "no_match" || status == "unavailable" || status == "error"
}

func validFixRun(run domain.ExperimentRun, campaignID, buildID, validationKind string) bool {
	return run.CampaignID == campaignID && run.BuildID == buildID && run.Operation == domain.OperationRegressionTest && run.Arguments["validation-kind"] == validationKind
}

type regressionResult struct {
	SchemaVersion  int    `json:"schema_version"`
	TargetRevision string `json:"target_revision"`
	ValidationKind string `json:"validation_kind"`
	ExitCode       int    `json:"exit_code"`
	SignalAbsent   bool   `json:"signal_absent"`
}

func (s *Service) validateRegressionResult(ctx context.Context, run domain.ExperimentRun, kind string) error {
	var resultArtifact domain.Artifact
	for _, artifactID := range run.ArtifactIDs {
		artifact, err := s.store.GetArtifact(ctx, artifactID)
		if err != nil {
			return err
		}
		if artifact.CampaignID != run.CampaignID || artifact.RunID != run.ID {
			return errors.New("regression artifact identity mismatch")
		}
		if artifact.Role == "regression_result" {
			resultArtifact = artifact
		}
	}
	if resultArtifact.ID == "" {
		return errors.New("structured regression result required")
	}
	if resultArtifact.Size < 0 || resultArtifact.Size > 1<<20 {
		return errors.New("structured regression result exceeds limit")
	}
	_, file, err := s.store.OpenArtifact(ctx, resultArtifact.ID)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20+1))
	decoder.DisallowUnknownFields()
	var result regressionResult
	if err := decoder.Decode(&result); err != nil {
		return errors.New("invalid structured regression result")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing structured regression result data")
	}
	if result.SchemaVersion != 1 || result.TargetRevision != run.Arguments["revision"] || result.ValidationKind != kind || result.ExitCode != 0 || !result.SignalAbsent {
		return errors.New("regression result did not demonstrate a clean expected outcome")
	}
	return nil
}

func filterRevisionChecks(values []domain.RevisionCheck, findingID string) []domain.RevisionCheck {
	result := values[:0]
	for _, value := range values {
		if value.FindingID == findingID {
			result = append(result, value)
		}
	}
	return result
}

func filterSourceEvidence(values []domain.SourceEvidence, findingID string) []domain.SourceEvidence {
	result := values[:0]
	for _, value := range values {
		if value.FindingID == findingID {
			result = append(result, value)
		}
	}
	return result
}

func filterSourceReviews(values []domain.SourceReview, findingID string) []domain.SourceReview {
	result := values[:0]
	for _, value := range values {
		if value.FindingID == findingID {
			result = append(result, value)
		}
	}
	return result
}

func affectedRevisions(gates []domain.GateCheck) []string {
	var result []string
	for _, gate := range gates {
		if gate.Status == "affected" {
			result = append(result, gate.Name)
		}
	}
	sort.Strings(result)
	return result
}

func distinctStrings(values ...string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func relevantArtifacts(artifacts []domain.Artifact, finding domain.Finding, group domain.CrashGroup, primitive domain.PrimitiveAssessment) []domain.Artifact {
	wanted := map[string]bool{group.CanonicalInputID: true, group.MinimizedArtifactID: true, finding.FixArtifactID: true}
	for _, id := range append(append([]string{}, finding.EvidenceIDs...), primitive.OperationEvidence...) {
		wanted[id] = true
	}
	for _, check := range append(append([]domain.GateCheck{}, finding.BranchChecks...), finding.NoveltyChecks...) {
		for _, id := range check.EvidenceIDs {
			wanted[id] = true
		}
	}
	result := artifacts[:0]
	for _, artifact := range artifacts {
		if wanted[artifact.ID] {
			artifact.StoragePath = ""
			result = append(result, artifact)
		}
	}
	return result
}

func validateUnifiedDiff(diff string) error {
	if diff == "" || len(diff) > 1<<20 || strings.ContainsRune(diff, '\x00') || strings.Contains(diff, "GIT binary patch") || strings.Contains(diff, "Binary files ") {
		return errors.New("bounded textual unified diff required")
	}
	lines := strings.Split(diff, "\n")
	hasOld, hasNew, hasHunk := false, false, false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(line)
			if len(fields) != 4 || !safeDiffPath(fields[2], "a/") || !safeDiffPath(fields[3], "b/") {
				return errors.New("diff contains an unsafe git path")
			}
		case strings.HasPrefix(line, "--- "):
			hasOld = true
			value := strings.Fields(strings.TrimPrefix(line, "--- "))
			if len(value) == 0 || value[0] != "/dev/null" && !safeDiffPath(value[0], "a/") {
				return errors.New("diff contains an unsafe old path")
			}
		case strings.HasPrefix(line, "+++ "):
			hasNew = true
			value := strings.Fields(strings.TrimPrefix(line, "+++ "))
			if len(value) == 0 || value[0] != "/dev/null" && !safeDiffPath(value[0], "b/") {
				return errors.New("diff contains an unsafe new path")
			}
		case strings.HasPrefix(line, "@@ "):
			hasHunk = true
		}
	}
	if !hasOld || !hasNew || !hasHunk {
		return errors.New("unified diff headers and at least one hunk required")
	}
	return nil
}

func safeDiffPath(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	value = strings.TrimPrefix(value, prefix)
	return value != "" && value != "." && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}
