// Package service is the deterministic research control plane. Models may
// propose calls into this package, but policy and evidence gates decide them.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/acquisition"
	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/repository"
	"github.com/ikarolaborda/agent-smith/internal/research/sourcefetch"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

var (
	ErrForbidden         = errors.New("research service: forbidden")
	ErrApprovalMissing   = errors.New("research service: approved decision not found")
	ErrSourceUnavailable = errors.New("research service: source acquisition unavailable")
)

const artifactPurgeApprovalLifetime = 24 * time.Hour

// Service coordinates policy, evidence transitions, durable objects, and audit.
type Service struct {
	store              repository.ControlPlane
	machine            domain.StateMachine
	now                func() time.Time
	broker             JobBroker
	sourceBroker       *sourcefetch.Broker
	apparatusAdmission *apparatus.VerifiedAdmissionCatalog
	workspaceRoots     []string
	internalRoot       string
}

// TargetRequest describes either an operator-authorized local Git acquisition
// or a configured pinned-bundle acquisition. The revision must be an exact
// commit. The control plane computes all source and provenance evidence.
type TargetRequest struct {
	Repository    string `json:"repository"`
	Revision      string `json:"revision"`
	SourceDir     string `json:"source_dir"`
	SourceName    string `json:"source_name,omitempty"`
	Language      string `json:"language"`
	Architecture  string `json:"architecture"`
	ApprovalID    string `json:"approval_id,omitempty"`
	CorrelationID string `json:"correlation_id"`
}

// ConfigureInternalRoot installs the private server-owned mount root used for
// materialized builds and campaign corpora. It is distinct from operator source
// roots and cannot be selected through an authorization scope.
func (s *Service) ConfigureInternalRoot(root string) error {
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return fmt.Errorf("research service: invalid internal worker root: %w", err)
	}
	s.internalRoot = canonical
	return nil
}

// ConfigureWorkspaceRoots installs the immutable host-root ceiling supplied by
// the server operator. Authorization scopes may narrow these roots but cannot
// expand them through the API.
func (s *Service) ConfigureWorkspaceRoots(roots []string) error {
	if len(roots) == 0 {
		return errors.New("research service: at least one fixed workspace root required")
	}
	canonical := make([]string, 0, len(roots))
	for _, root := range roots {
		resolved, err := canonicalDirectory(root)
		if err != nil {
			return fmt.Errorf("research service: invalid fixed workspace root: %w", err)
		}
		canonical = append(canonical, resolved)
	}
	s.workspaceRoots = canonical
	return nil
}

// JobBroker is the typed runner queue boundary used after policy authorization.
type JobBroker interface {
	Submit(context.Context, domain.WorkerJob) (domain.ExperimentRun, error)
}

// New constructs a deterministic control-plane service.
func New(storage repository.ControlPlane, minimumReproductions int) (*Service, error) {
	if storage == nil {
		return nil, errors.New("research service: store required")
	}
	return &Service{
		store: storage, machine: domain.StateMachine{MinimumReproductions: minimumReproductions},
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Store exposes read-only/query-oriented persistence operations to API adapters.
func (s *Service) Store() repository.ControlPlane { return s.store }

// AttachBroker installs the verified runner after its isolation preflight.
func (s *Service) AttachBroker(broker JobBroker) error {
	if broker == nil {
		return errors.New("research service: job broker required")
	}
	s.broker = broker
	return nil
}

// AttachSourceBroker enables fixed-origin, digest-pinned HTTPS acquisition.
func (s *Service) AttachSourceBroker(broker *sourcefetch.Broker) error {
	if broker == nil {
		return errors.New("research service: source acquisition broker required")
	}
	s.sourceBroker = broker
	return nil
}

// AttachApparatusAdmission installs the separately verified, signed SBOM and
// provenance catalog used by apparatus registration.
func (s *Service) AttachApparatusAdmission(catalog *apparatus.VerifiedAdmissionCatalog) error {
	if catalog == nil {
		return errors.New("research service: apparatus admission catalog required")
	}
	s.apparatusAdmission = catalog
	return nil
}

// CreateScope validates and persists a scope owned by the authenticated actor.
func (s *Service) CreateScope(ctx context.Context, actor domain.Principal, scope domain.AuthorizationScope) (domain.AuthorizationScope, error) {
	if !hasRole(actor, domain.RoleAdmin) && !hasRole(actor, domain.RoleOperator) {
		return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, "operator or admin role required")
	}
	if scope.OperatorID == "" {
		scope.OperatorID = actor.ID
	}
	if scope.OperatorID != actor.ID && !hasRole(actor, domain.RoleAdmin) {
		return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, "only an admin may create another operator's scope")
	}
	if scope.ID == "" {
		scope.ID = newID("scope")
	}
	if scope.SchemaVersion == 0 {
		scope.SchemaVersion = 1
	}
	if scope.CreatedAt.IsZero() {
		scope.CreatedAt = s.now()
	}
	for index, root := range scope.WorkspaceRoots {
		canonical, err := canonicalDirectory(root)
		if err != nil {
			return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, "workspace root must be an existing directory")
		}
		if len(s.workspaceRoots) > 0 && !insideAnyRoot(canonical, s.workspaceRoots) {
			return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, "workspace root exceeds the server operator allowlist")
		}
		scope.WorkspaceRoots[index] = canonical
	}
	if err := scope.Validate(s.now()); err != nil {
		return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, err.Error())
	}
	for _, manifestID := range scope.AllowedApparatusIDs {
		if _, err := s.store.GetApparatus(ctx, manifestID); err != nil {
			return scope, s.denied(ctx, actor, "scope.create", "authorization_scope", scope.ID, "allowed apparatus is not registered: "+manifestID)
		}
	}
	if err := s.store.CreateScope(ctx, scope); err != nil {
		return scope, err
	}
	return scope, s.allowed(ctx, actor, "scope.create", "authorization_scope", scope.ID, "")
}

// RegisterApparatus persists a validated immutable worker adapter for later scopes.
func (s *Service) RegisterApparatus(ctx context.Context, actor domain.Principal, manifest domain.ApparatusManifest) (domain.ApparatusManifest, error) {
	if !hasRole(actor, domain.RoleAdmin) {
		return manifest, s.denied(ctx, actor, "apparatus.register", "apparatus_manifest", manifest.ID, "admin role required")
	}
	if s.apparatusAdmission == nil {
		return manifest, s.denied(ctx, actor, "apparatus.register", "apparatus_manifest", manifest.ID, "verified apparatus supply-chain admission required")
	}
	admitted, err := s.apparatusAdmission.Admit(manifest, s.now())
	if err != nil {
		return manifest, s.denied(ctx, actor, "apparatus.register", "apparatus_manifest", manifest.ID, err.Error())
	}
	if err := apparatus.ValidateManifest(admitted); err != nil {
		return manifest, s.denied(ctx, actor, "apparatus.register", "apparatus_manifest", manifest.ID, err.Error())
	}
	if err := s.store.SaveApparatus(ctx, admitted); err != nil {
		return admitted, err
	}
	return admitted, s.allowedWithDetails(ctx, actor, "apparatus.register", "apparatus_manifest", admitted.ID, "", map[string]string{
		"image_digest": admitted.ImageDigest, "sbom_sha256": admitted.SupplyChain.SBOMSHA256, "provenance_sha256": admitted.SupplyChain.ProvenanceSHA256,
		"admission_key_id": admitted.SupplyChain.AdmissionKeyID, "admission_expires_at": admitted.SupplyChain.AdmissionExpiresAt.Format(time.RFC3339Nano),
	})
}

// RevokeScope permanently removes a scope from future authorization decisions.
func (s *Service) RevokeScope(ctx context.Context, actor domain.Principal, scopeID, reason string) error {
	scope, err := s.store.GetScope(ctx, scopeID)
	if err != nil {
		return err
	}
	if actor.ID != scope.OperatorID && !hasRole(actor, domain.RoleAdmin) {
		return s.denied(ctx, actor, "scope.revoke", "authorization_scope", scopeID, "scope owner or admin required")
	}
	if err := s.store.RevokeScope(ctx, scopeID, s.now()); err != nil {
		return err
	}
	return s.allowedWithDetails(ctx, actor, "scope.revoke", "authorization_scope", scopeID, "", map[string]string{"reason": reason})
}

// CreateCampaign persists a version-one draft under a currently valid scope.
func (s *Service) CreateCampaign(ctx context.Context, actor domain.Principal, campaign domain.Campaign) (domain.Campaign, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleAdmin) {
		return campaign, s.denied(ctx, actor, "campaign.create", "campaign", campaign.ID, "operator or admin role required")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return campaign, err
	}
	if err := scope.Validate(s.now()); err != nil {
		return campaign, s.denied(ctx, actor, "campaign.create", "campaign", campaign.ID, err.Error())
	}
	if actor.ID != scope.OperatorID && !hasRole(actor, domain.RoleAdmin) {
		return campaign, s.denied(ctx, actor, "campaign.create", "campaign", campaign.ID, "scope owner or admin required")
	}
	if budgetExceeds(campaign.Budget, scope.Budget) {
		return campaign, s.denied(ctx, actor, "campaign.create", "campaign", campaign.ID, "campaign budget exceeds authorization scope")
	}
	if campaign.ID == "" {
		campaign.ID = newID("campaign")
	}
	if campaign.SchemaVersion == 0 {
		campaign.SchemaVersion = 1
	}
	if campaign.Budget == (domain.ResourceBudget{}) {
		campaign.Budget = scope.Budget
	}
	if err := campaign.Budget.Validate(); err != nil {
		return campaign, s.denied(ctx, actor, "campaign.create", "campaign", campaign.ID, err.Error())
	}
	campaign.State = domain.CampaignDraft
	campaign.Version = 1
	campaign.CreatedAt = s.now()
	campaign.UpdatedAt = campaign.CreatedAt
	campaign, err = s.store.CreateCampaign(ctx, campaign)
	if err != nil {
		return campaign, err
	}
	return campaign, s.allowed(ctx, actor, "campaign.create", "campaign", campaign.ID, "")
}

// AcquireTarget captures and hashes an existing authorized source tree, then
// advances a scoped campaign with system-computed evidence.
func (s *Service) AcquireTarget(ctx context.Context, actor domain.Principal, campaignID string, request TargetRequest) (domain.TargetRevision, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleAdmin) {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire", "campaign", campaignID, "operator or admin role required")
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if campaign.State != domain.CampaignScoped {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire", "campaign", campaignID, "campaign must be scoped before acquisition")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	destinationHost, err := s.targetAcquisitionPolicy(request)
	if err != nil {
		if errors.Is(err, ErrSourceUnavailable) {
			return domain.TargetRevision{}, err
		}
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire", "campaign", campaignID, err.Error())
	}
	decision, err := s.AuthorizeAction(ctx, campaignID, domain.Action{Principal: actor, Operation: domain.OperationAcquire, Repository: request.Repository,
		Revision: request.Revision, WorkspacePath: request.SourceDir, DestinationHost: destinationHost, WallSeconds: scope.Budget.MaxWallSeconds,
		DiskBytes: scope.Budget.MaxDiskBytes, Inodes: scope.Budget.MaxInodes, ApprovalID: request.ApprovalID}, request.CorrelationID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if !decision.Allowed {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire", "campaign", campaignID, decision.Reason)
	}
	if request.CorrelationID == "" || request.Language == "" || request.Architecture == "" || s.internalRoot == "" {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire", "campaign", campaignID, "correlation, language, architecture, and internal storage are required")
	}
	limits := acquisition.Limits{MaxFiles: scope.Budget.MaxInodes, MaxBytes: scope.Budget.MaxDiskBytes}
	targetID := deterministicID("target", campaignID, request.Repository, request.Revision)
	destination, err := acquisition.CaptureDirectory(s.internalRoot, campaignID, targetID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	acquireCtx, cancelAcquire := acquisitionDeadline(ctx, scope.Budget.MaxWallSeconds)
	defer cancelAcquire()
	requestedRef, commit, snapshot, provenance, err := s.captureTarget(acquireCtx, request, destination, limits)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	now := s.now()
	target := domain.TargetRevision{SchemaVersion: 1, ID: targetID, CampaignID: campaignID, Repository: request.Repository, RequestedRef: requestedRef,
		Commit: commit, SourceSHA256: snapshot.SourceSHA256, Language: request.Language, Architecture: request.Architecture, Acquisition: provenance, AcquiredAt: now}
	if existing, getErr := s.store.GetTarget(ctx, target.ID); getErr == nil {
		if !sameTargetIdentity(existing, target) {
			return domain.TargetRevision{}, errors.New("research service: target identity collision")
		}
		target = existing
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return domain.TargetRevision{}, getErr
	} else if err := s.store.SaveTarget(ctx, target); err != nil {
		return domain.TargetRevision{}, err
	}
	updated, err := s.machine.Advance(campaign, domain.CampaignAcquired, domain.EvidenceFacts{TargetID: target.ID, SourceSHA256: target.SourceSHA256}, now)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if err := s.store.UpdateCampaign(ctx, updated, campaign.Version); err != nil {
		return domain.TargetRevision{}, err
	}
	details := targetAcquisitionDetails(campaignID, "", target)
	return target, s.allowedWithDetails(ctx, actor, "target.acquire", "target_revision", target.ID, request.CorrelationID, details)
}

// AcquireComparisonTarget captures another authorized supported revision after
// primitive assessment without replacing the immutable primary campaign target.
func (s *Service) AcquireComparisonTarget(ctx context.Context, actor domain.Principal, campaignID, findingID string, request TargetRequest) (domain.TargetRevision, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleAdmin) {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "campaign", campaignID, "operator or admin role required")
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if campaign.State != domain.CampaignPrimitiveAssessed && campaign.State != domain.CampaignBranchChecked {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "campaign", campaignID, "primitive assessment must complete before comparison acquisition")
	}
	finding, err := s.store.GetFinding(ctx, findingID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if finding.CampaignID != campaignID || finding.Label != domain.FindingPrimitiveConfirmed {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "finding", findingID, "campaign-owned primitive-confirmed finding required")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	expectedCorrelation := "target:" + findingID + ":" + strings.TrimSpace(request.Revision)
	if request.CorrelationID != expectedCorrelation {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "finding", findingID, "comparison target correlation does not match finding and revision")
	}
	destinationHost, err := s.targetAcquisitionPolicy(request)
	if err != nil {
		if errors.Is(err, ErrSourceUnavailable) {
			return domain.TargetRevision{}, err
		}
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "campaign", campaignID, err.Error())
	}
	decision, err := s.AuthorizeAction(ctx, campaignID, domain.Action{Principal: actor, Operation: domain.OperationAcquire, Repository: request.Repository,
		Revision: request.Revision, WorkspacePath: request.SourceDir, DestinationHost: destinationHost, WallSeconds: scope.Budget.MaxWallSeconds,
		DiskBytes: scope.Budget.MaxDiskBytes, Inodes: scope.Budget.MaxInodes, ApprovalID: request.ApprovalID}, request.CorrelationID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	if !decision.Allowed {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "campaign", campaignID, decision.Reason)
	}
	if request.Language == "" || request.Architecture == "" || s.internalRoot == "" {
		return domain.TargetRevision{}, s.denied(ctx, actor, "target.acquire_comparison", "campaign", campaignID, "language, architecture, and internal storage are required")
	}
	limits := acquisition.Limits{MaxFiles: scope.Budget.MaxInodes, MaxBytes: scope.Budget.MaxDiskBytes}
	targetID := deterministicID("target", campaignID, request.Repository, request.Revision)
	destination, err := acquisition.CaptureDirectory(s.internalRoot, campaignID, targetID)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	acquireCtx, cancelAcquire := acquisitionDeadline(ctx, scope.Budget.MaxWallSeconds)
	defer cancelAcquire()
	requestedRef, commit, snapshot, provenance, err := s.captureTarget(acquireCtx, request, destination, limits)
	if err != nil {
		return domain.TargetRevision{}, err
	}
	target := domain.TargetRevision{SchemaVersion: 1, ID: targetID, CampaignID: campaignID, Repository: request.Repository, RequestedRef: requestedRef,
		Commit: commit, SourceSHA256: snapshot.SourceSHA256, Language: request.Language, Architecture: request.Architecture, Acquisition: provenance, AcquiredAt: s.now()}
	if existing, getErr := s.store.GetTarget(ctx, target.ID); getErr == nil {
		if !sameTargetIdentity(existing, target) {
			return domain.TargetRevision{}, errors.New("research service: target identity collision")
		}
		target = existing
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return domain.TargetRevision{}, getErr
	} else if err := s.store.SaveTarget(ctx, target); err != nil {
		return domain.TargetRevision{}, err
	}
	return target, s.allowedWithDetails(ctx, actor, "target.acquire_comparison", "target_revision", target.ID, request.CorrelationID, targetAcquisitionDetails(campaignID, findingID, target))
}

// Transition advances exactly one evidence-backed state and rejects stale writes.
func (s *Service) Transition(ctx context.Context, actor domain.Principal, campaignID string, to domain.CampaignState, facts domain.EvidenceFacts) (domain.Campaign, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleAdmin) {
		return domain.Campaign{}, s.denied(ctx, actor, "campaign.transition", "campaign", campaignID, "operator or admin role required")
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return campaign, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return campaign, err
	}
	if err := scope.Validate(s.now()); err != nil {
		return campaign, s.denied(ctx, actor, "campaign.transition", "campaign", campaignID, err.Error())
	}
	if actor.ID != scope.OperatorID && !hasRole(actor, domain.RoleAdmin) {
		return campaign, s.denied(ctx, actor, "campaign.transition", "campaign", campaignID, "scope owner or admin required")
	}
	updated, err := s.machine.Advance(campaign, to, facts, s.now())
	if err != nil {
		return campaign, s.denied(ctx, actor, "campaign.transition", "campaign", campaignID, err.Error())
	}
	if updated.Version == campaign.Version {
		return campaign, nil
	}
	if err := s.store.UpdateCampaign(ctx, updated, campaign.Version); err != nil {
		return campaign, err
	}
	return updated, s.allowedWithDetails(ctx, actor, "campaign.transition", "campaign", campaignID, "", map[string]string{"from": string(campaign.State), "to": string(to)})
}

// PromoteFinding applies the monotonic evidence gate and persists the result.
func (s *Service) PromoteFinding(ctx context.Context, actor domain.Principal, findingID string, to domain.FindingLabel, facts domain.EvidenceFacts) (domain.Finding, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleReviewer, domain.RoleAdmin) {
		return domain.Finding{}, s.denied(ctx, actor, "finding.promote", "finding", findingID, "operator, reviewer, or admin role required")
	}
	finding, err := s.store.GetFinding(ctx, findingID)
	if err != nil {
		return finding, err
	}
	campaign, err := s.store.GetCampaign(ctx, finding.CampaignID)
	if err != nil {
		return finding, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return finding, err
	}
	if !scopeMember(scope, actor) {
		return finding, s.denied(ctx, actor, "finding.promote", "finding", findingID, "principal is not a campaign member")
	}
	promoted, err := s.machine.PromoteFinding(finding, to, facts, s.now())
	if err != nil {
		return finding, s.denied(ctx, actor, "finding.promote", "finding", findingID, err.Error())
	}
	if err := s.store.SaveFinding(ctx, promoted); err != nil {
		return finding, err
	}
	return promoted, s.allowedWithDetails(ctx, actor, "finding.promote", "finding", findingID, "", map[string]string{"from": string(finding.Label), "to": string(to)})
}

// RequestApproval creates a pending decision for a concrete operation.
func (s *Service) RequestApproval(ctx context.Context, actor domain.Principal, approval domain.Approval) (domain.Approval, error) {
	if !hasAnyRole(actor, domain.RoleOperator, domain.RoleAdmin) {
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "operator or admin role required")
	}
	campaign, err := s.store.GetCampaign(ctx, approval.CampaignID)
	if err != nil {
		return approval, err
	}
	if campaign.ScopeID != approval.ScopeID {
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "campaign does not belong to scope")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return approval, err
	}
	if !scopeMember(scope, actor) {
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "principal is not a scope member")
	}
	if !domain.IsKnownOperation(approval.Operation) {
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "approval operation is unknown")
	}
	if approval.Operation == domain.OperationPurgeArtifact {
		if !containsOperation(scope.AllowedOperations, domain.OperationPurgeArtifact) || !containsOperation(scope.ApprovalOperations, domain.OperationPurgeArtifact) {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "artifact purge must be an approval-gated scope operation")
		}
		artifactID, ok := artifactIDFromPurgeCorrelation(approval.CorrelationID)
		if !ok {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "artifact purge correlation must bind one artifact")
		}
		artifact, err := s.store.GetArtifact(ctx, artifactID)
		if err != nil {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "artifact purge requires the matching terminal campaign")
		}
		if artifact.CampaignID != campaign.ID || !domain.IsTerminalCampaignState(campaign.State) {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "artifact purge requires the matching terminal campaign")
		}
		if artifact.PurgedAt != nil {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "artifact already has an immutable purge tombstone")
		}
		if artifact.RetainUntil.IsZero() || s.now().Before(artifact.RetainUntil) {
			return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, store.ErrRetentionActive.Error())
		}
	}
	if approval.ID == "" {
		approval.ID = newID("approval")
	} else if _, err := s.store.GetApproval(ctx, approval.ID); !errors.Is(err, store.ErrNotFound) {
		if err != nil {
			return approval, err
		}
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "approval id already exists")
	}
	approval.Reason = strings.TrimSpace(approval.Reason)
	if approval.CorrelationID == "" || approval.Operation == "" || approval.Reason == "" || len(approval.Reason) > 4096 {
		return approval, s.denied(ctx, actor, "approval.request", "approval", approval.ID, "correlation, operation, and reason required")
	}
	approval.SchemaVersion = 1
	approval.Status = "pending"
	approval.RequestedBy = actor.ID
	approval.CreatedAt = s.now()
	approval.DecidedBy = ""
	approval.DecidedAt = nil
	if err := s.store.SaveApproval(ctx, approval); err != nil {
		return approval, err
	}
	return approval, s.allowed(ctx, actor, "approval.request", "approval", approval.ID, approval.CorrelationID)
}

// ArtifactPurgeCorrelation is the stable approval binding for one artifact.
func ArtifactPurgeCorrelation(artifactID string) string { return "artifact-purge:" + artifactID }

func artifactIDFromPurgeCorrelation(correlationID string) (string, bool) {
	const prefix = "artifact-purge:"
	artifactID := strings.TrimPrefix(correlationID, prefix)
	return artifactID, strings.HasPrefix(correlationID, prefix) && artifactID != "" && ArtifactPurgeCorrelation(artifactID) == correlationID
}

// PurgeArtifact applies the destructive custody gate: an admin, a terminal
// campaign, an elapsed retention deadline, and a fresh independent approval
// bound to exactly one artifact. The store retains immutable tombstone metadata.
func (s *Service) PurgeArtifact(ctx context.Context, actor domain.Principal, artifactID, approvalID, reason string) (domain.Artifact, error) {
	if !hasRole(actor, domain.RoleAdmin) {
		return domain.Artifact{}, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, "admin role required")
	}
	artifact, err := s.store.GetArtifact(ctx, artifactID)
	if err != nil {
		return artifact, err
	}
	campaign, err := s.store.GetCampaign(ctx, artifact.CampaignID)
	if err != nil {
		return artifact, err
	}
	if !domain.IsTerminalCampaignState(campaign.State) {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, "campaign must be terminal before evidence purge")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return artifact, err
	}
	if !containsOperation(scope.AllowedOperations, domain.OperationPurgeArtifact) || !containsOperation(scope.ApprovalOperations, domain.OperationPurgeArtifact) {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, "artifact purge is outside the approval-gated scope")
	}
	now := s.now()
	if artifact.RetainUntil.IsZero() || now.Before(artifact.RetainUntil) {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, store.ErrRetentionActive.Error())
	}
	approval, err := s.store.GetApproval(ctx, approvalID)
	if err != nil {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, ErrApprovalMissing.Error())
	}
	correlationID := ArtifactPurgeCorrelation(artifactID)
	if approval.Status != "approved" || approval.ScopeID != campaign.ScopeID || approval.CampaignID != campaign.ID ||
		approval.Operation != domain.OperationPurgeArtifact || approval.CorrelationID != correlationID || approval.DecidedAt == nil ||
		approval.DecidedBy == "" || approval.RequestedBy == approval.DecidedBy || now.Before(*approval.DecidedAt) || now.Sub(*approval.DecidedAt) > artifactPurgeApprovalLifetime {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, ErrApprovalMissing.Error())
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1024 {
		return artifact, s.denied(ctx, actor, "artifact.purge", "artifact", artifactID, "bounded purge reason required")
	}
	details := map[string]string{
		"campaign_id": campaign.ID, "content_id": artifact.ContentID, "approval_id": approval.ID,
		"retain_until": artifact.RetainUntil.UTC().Format(time.RFC3339Nano), "reason": reason,
	}
	if err := s.allowedWithDetails(ctx, actor, "artifact.purge.authorize", "artifact", artifactID, correlationID, details); err != nil {
		return artifact, err
	}
	purged, err := s.store.PurgeArtifact(ctx, artifactID, approval.ID, reason, now)
	if err != nil {
		_, _ = s.store.AppendAudit(ctx, domain.AuditEvent{ActorID: actor.ID, Action: "artifact.purge.failed", ResourceType: "artifact", ResourceID: artifactID, CorrelationID: correlationID, Decision: "failed", Details: map[string]string{"reason": err.Error()}})
		return purged, err
	}
	details["blob_deleted"] = fmt.Sprintf("%t", purged.BlobDeletedAt != nil)
	if err := s.allowedWithDetails(ctx, actor, "artifact.purge.complete", "artifact", artifactID, correlationID, details); err != nil {
		return purged, err
	}
	return purged, nil
}

// DecideApproval records an independent human review decision.
func (s *Service) DecideApproval(ctx context.Context, actor domain.Principal, approvalID string, approved bool, reason string) (domain.Approval, error) {
	if !hasAnyRole(actor, domain.RoleReviewer, domain.RoleAdmin) {
		return domain.Approval{}, s.denied(ctx, actor, "approval.decide", "approval", approvalID, "reviewer or admin role required")
	}
	approval, err := s.store.GetApproval(ctx, approvalID)
	if err != nil {
		return approval, err
	}
	scope, err := s.store.GetScope(ctx, approval.ScopeID)
	if err != nil {
		return approval, err
	}
	if !scopeMember(scope, actor) {
		return approval, s.denied(ctx, actor, "approval.decide", "approval", approvalID, "reviewer is not a scope member")
	}
	if approval.Status != "pending" {
		return approval, s.denied(ctx, actor, "approval.decide", "approval", approvalID, "approval is no longer pending")
	}
	if approval.RequestedBy == actor.ID && (approval.Operation == domain.OperationPurgeArtifact || !hasRole(actor, domain.RoleAdmin)) {
		return approval, s.denied(ctx, actor, "approval.decide", "approval", approvalID, "requester cannot review their own approval")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4096 {
		return approval, s.denied(ctx, actor, "approval.decide", "approval", approvalID, "decision reason required")
	}
	if approved {
		approval.Status = "approved"
	} else {
		approval.Status = "denied"
	}
	now := s.now()
	approval.DecidedBy = actor.ID
	approval.DecidedAt = &now
	approval.Reason = reason
	if err := s.store.SaveApproval(ctx, approval); err != nil {
		return approval, err
	}
	return approval, s.allowedWithDetails(ctx, actor, "approval.decide", "approval", approvalID, approval.CorrelationID, map[string]string{"status": approval.Status, "reason": reason})
}

// AuthorizeAction verifies policy and, when needed, the exact durable approval.
func (s *Service) AuthorizeAction(ctx context.Context, campaignID string, action domain.Action, correlationID string) (domain.PolicyDecision, error) {
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return domain.PolicyDecision{}, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return domain.PolicyDecision{}, err
	}
	if !scopeMember(scope, action.Principal) {
		decision := domain.PolicyDecision{Reason: "research: principal is not a scope member"}
		_ = s.denied(ctx, action.Principal, "action.authorize", "campaign", campaignID, decision.Reason)
		return decision, nil
	}
	decision := scope.Authorize(action, s.now())
	if decision.ApprovalRequired {
		return decision, nil
	}
	if !decision.Allowed {
		_ = s.denied(ctx, action.Principal, "action.authorize", "campaign", campaignID, decision.Reason)
		return decision, nil
	}
	if action.ApprovalID != "" {
		approval, err := s.store.GetApproval(ctx, action.ApprovalID)
		if err != nil {
			return domain.PolicyDecision{Reason: ErrApprovalMissing.Error()}, nil
		}
		if approval.Status != "approved" || approval.ScopeID != scope.ID || approval.CampaignID != campaignID || approval.Operation != action.Operation || approval.CorrelationID != correlationID {
			return domain.PolicyDecision{Reason: ErrApprovalMissing.Error()}, nil
		}
	}
	_ = s.allowed(ctx, action.Principal, "action.authorize", "campaign", campaignID, correlationID)
	return decision, nil
}

// CanReadCampaign applies campaign membership to query/API adapters.
func (s *Service) CanReadCampaign(ctx context.Context, actor domain.Principal, campaignID string) (bool, error) {
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return false, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return false, err
	}
	return scopeMember(scope, actor), nil
}

// PreauthorizeEnqueue checks state, actor, target, approval, and resource policy
// before an API adapter creates any campaign-owned corpus/evidence views.
// Enqueue repeats the checks against the final manifest-built job envelope.
func (s *Service) PreauthorizeEnqueue(ctx context.Context, actor domain.Principal, campaignID string, operation domain.Operation, revision string, budget domain.ResourceBudget, approvalID, correlationID string) error {
	if correlationID == "" {
		return s.denied(ctx, actor, "job.preauthorize", "campaign", campaignID, "audit correlation id is required")
	}
	if err := budget.Validate(); err != nil {
		return s.denied(ctx, actor, "job.preauthorize", "campaign", campaignID, err.Error())
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return err
	}
	if !campaignAllowsOperation(campaign.State, operation) {
		return s.denied(ctx, actor, "job.preauthorize", "campaign", campaignID, "operation is not permitted in the current evidence state")
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return err
	}
	decision, err := s.AuthorizeAction(ctx, campaignID, domain.Action{
		Principal: actor, Operation: operation, Repository: scope.TargetRepository, Revision: revision,
		WallSeconds: budget.MaxWallSeconds, MemoryBytes: budget.MaxMemoryBytes, CPUSeconds: budget.MaxCPUSeconds, DiskBytes: budget.MaxDiskBytes, Inodes: budget.MaxInodes, PIDs: budget.MaxPIDs, ApprovalID: approvalID,
	}, correlationID)
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return s.denied(ctx, actor, "job.preauthorize", "campaign", campaignID, decision.Reason)
	}
	return nil
}

// Enqueue authorizes every mount and exact approval before touching the queue.
func (s *Service) Enqueue(ctx context.Context, actor domain.Principal, campaignID string, job domain.WorkerJob, approvalID string) (domain.ExperimentRun, error) {
	if s.broker == nil {
		return domain.ExperimentRun{}, errors.New("research service: verified runner unavailable")
	}
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return domain.ExperimentRun{}, err
	}
	if !campaignAllowsOperation(campaign.State, job.Operation) {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "operation is not permitted in the current evidence state")
	}
	if job.CampaignID != campaign.ID || job.ScopeID != campaign.ScopeID || job.AuditCorrelationID == "" {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "job identity does not match campaign")
	}
	if err := s.PreauthorizeEnqueue(ctx, actor, campaignID, job.Operation, job.Arguments["revision"], job.Budget, approvalID, job.AuditCorrelationID); err != nil {
		return domain.ExperimentRun{}, err
	}
	scope, err := s.store.GetScope(ctx, campaign.ScopeID)
	if err != nil {
		return domain.ExperimentRun{}, err
	}
	manifestID := job.Arguments["manifest"]
	if manifestID == "" || !containsString(scope.AllowedApparatusIDs, manifestID) {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "apparatus is outside authorization scope")
	}
	manifest, err := s.store.GetApparatus(ctx, manifestID)
	if err != nil {
		return domain.ExperimentRun{}, err
	}
	if s.apparatusAdmission == nil || s.apparatusAdmission.ValidateAdmitted(manifest, s.now()) != nil {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "apparatus supply-chain admission is missing, expired, or no longer trusted")
	}
	if manifest.ImageDigest != job.ImageDigest || !containsOperation(manifest.Operations, job.Operation) || !manifestHasHarness(manifest, job.Arguments["harness"]) {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "job does not match registered apparatus manifest")
	}
	if !containsString(manifest.Sanitizers, job.Arguments["sanitizer"]) || !reflect.DeepEqual(job.Environment, manifest.Environment) ||
		!reflect.DeepEqual(job.ArtifactRules, apparatus.ArtifactRules(job.Operation)) || budgetExceeds(job.Budget, manifest.Limits) ||
		!validApparatusArguments(job) || !validApparatusMounts(job) {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "job envelope differs from the registered apparatus schema")
	}
	repository, revision := scope.TargetRepository, ""
	selectedTargetID := job.TargetID
	if selectedTargetID == "" {
		selectedTargetID = campaign.TargetID
	}
	var selectedTarget domain.TargetRevision
	if selectedTargetID != "" {
		target, targetErr := s.store.GetTarget(ctx, selectedTargetID)
		if targetErr != nil {
			return domain.ExperimentRun{}, targetErr
		}
		if target.CampaignID != campaignID || target.Repository != scope.TargetRepository {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "target_revision", target.ID, "target does not belong to this campaign and scope")
		}
		selectedTarget = target
		repository, revision = target.Repository, target.Commit
		verifiedSource, verifyErr := acquisition.VerifiedCapture(s.internalRoot, campaign.ID, target.ID, target.SourceSHA256, acquisition.Limits{MaxFiles: scope.Budget.MaxInodes, MaxBytes: scope.Budget.MaxDiskBytes})
		if verifyErr != nil {
			return domain.ExperimentRun{}, verifyErr
		}
		for _, mount := range job.Mounts {
			if mount.Name == "source" {
				canonical, canonicalErr := canonicalDirectory(mount.HostPath)
				if canonicalErr != nil || canonical != verifiedSource {
					return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "source mount does not match the captured target")
				}
			}
		}
	}
	if revision == "" && len(scope.AllowedRevisions) == 1 {
		revision = scope.AllowedRevisions[0]
	}
	if job.Arguments["revision"] != revision {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "job revision does not match its immutable captured target")
	}
	if jobNeedsEvidence(job) {
		if job.InputArtifactID == "" {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "operation requires an evidence artifact")
		}
		artifact, artifactErr := s.store.GetArtifact(ctx, job.InputArtifactID)
		if artifactErr != nil {
			return domain.ExperimentRun{}, artifactErr
		}
		if artifact.CampaignID != campaignID || !evidenceRoleAllowed(job, artifact.Role) {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "evidence artifact does not match the campaign operation")
		}
		expected := filepath.Join(s.internalRoot, campaignID, "evidence", string(job.Operation)+"-"+job.InputArtifactID)
		for _, mount := range job.Mounts {
			if mount.Name == "evidence" {
				canonical, canonicalErr := canonicalDirectory(mount.HostPath)
				if canonicalErr != nil || canonical != expected {
					return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "evidence mount does not match the verified artifact")
				}
			}
		}
	} else if job.InputArtifactID != "" {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "operation does not accept an evidence artifact")
	}
	if job.PatchArtifactID != "" {
		if job.Operation != domain.OperationBuild || campaign.State != domain.CampaignNoveltyReviewed || selectedTarget.ID != campaign.TargetID {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "patch evidence is accepted only for a novelty-reviewed fix build")
		}
		patch, patchErr := s.store.GetArtifact(ctx, job.PatchArtifactID)
		if patchErr != nil {
			return domain.ExperimentRun{}, patchErr
		}
		if patch.CampaignID != campaignID || patch.Role != "candidate_patch" {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "artifact", patch.ID, "campaign-owned candidate patch required")
		}
		expected := filepath.Join(s.internalRoot, campaignID, "evidence", string(domain.OperationBuild)+"-"+patch.ID)
		matched := false
		for _, mount := range job.Mounts {
			if mount.Name == "patch" {
				canonical, canonicalErr := canonicalDirectory(mount.HostPath)
				matched = canonicalErr == nil && canonical == expected
			}
		}
		if !matched {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "artifact", patch.ID, "patch mount does not match verified evidence")
		}
	} else if job.Operation == domain.OperationBuild && campaign.State == domain.CampaignNoveltyReviewed {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "novelty-reviewed build requires a candidate patch")
	}
	if requiresBuild(job.Operation) {
		if job.BuildID == "" {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "operation requires an evidence-backed build")
		}
		build, buildErr := s.store.GetBuild(ctx, job.BuildID)
		if buildErr != nil {
			return domain.ExperimentRun{}, buildErr
		}
		if build.CampaignID != campaignID || build.Status != string(domain.RunCompleted) || build.ManifestID != manifest.ID ||
			build.TargetID != selectedTarget.ID || build.ImageDigest != manifest.ImageDigest || build.Sanitizer != job.Arguments["sanitizer"] || build.Provenance["harness"] != job.Arguments["harness"] {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "build provenance does not match the requested experiment")
		}
	} else if job.BuildID != "" {
		return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "operation does not accept a build id")
	}
	paths := []string{""}
	if len(job.Mounts) > 0 {
		paths = paths[:0]
		for _, mount := range job.Mounts {
			paths = append(paths, mount.HostPath)
		}
	}
	for index, path := range paths {
		policyPath := path
		if len(job.Mounts) > 0 && (job.Mounts[index].Name == "build" || job.Mounts[index].Name == "corpus" || job.Mounts[index].Name == "evidence" || job.Mounts[index].Name == "patch" || job.Mounts[index].Name == "source" && campaign.TargetID != "") {
			campaignInternal := filepath.Join(s.internalRoot, campaignID)
			if s.internalRoot == "" || !insideAnyRoot(path, []string{campaignInternal}) {
				return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "internal mount is outside the campaign-owned worker root")
			}
			policyPath = ""
		} else if path != "" && len(s.workspaceRoots) > 0 && !insideAnyRoot(path, s.workspaceRoots) {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, "mount exceeds the server operator workspace allowlist")
		}
		action := domain.Action{
			Principal: actor, Operation: job.Operation, Repository: repository, Revision: revision, WorkspacePath: policyPath,
			WallSeconds: job.Budget.MaxWallSeconds, MemoryBytes: job.Budget.MaxMemoryBytes, CPUSeconds: job.Budget.MaxCPUSeconds, DiskBytes: job.Budget.MaxDiskBytes,
			Inodes: job.Budget.MaxInodes, PIDs: job.Budget.MaxPIDs, ApprovalID: approvalID,
		}
		decision, authErr := s.AuthorizeAction(ctx, campaignID, action, job.AuditCorrelationID)
		if authErr != nil {
			return domain.ExperimentRun{}, authErr
		}
		if !decision.Allowed {
			return domain.ExperimentRun{}, s.denied(ctx, actor, "job.enqueue", "campaign", campaignID, decision.Reason)
		}
	}
	run, err := s.broker.Submit(ctx, job)
	if err != nil {
		return run, err
	}
	if err := s.allowedWithDetails(ctx, actor, "job.enqueue", "experiment_run", run.ID, job.AuditCorrelationID, map[string]string{"operation": string(job.Operation), "campaign_id": campaignID}); err != nil {
		return run, err
	}
	return run, nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return filepath.Clean(real), nil
}

func insideAnyRoot(path string, roots []string) bool {
	candidate, err := canonicalDirectory(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, candidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func (s *Service) denied(ctx context.Context, actor domain.Principal, action, resourceType, resourceID, reason string) error {
	_, _ = s.store.AppendAudit(ctx, domain.AuditEvent{ActorID: actor.ID, Action: action, ResourceType: resourceType, ResourceID: resourceID, Decision: "denied", Details: map[string]string{"reason": reason}})
	return fmt.Errorf("%w: %s", ErrForbidden, reason)
}

func (s *Service) allowed(ctx context.Context, actor domain.Principal, action, resourceType, resourceID, correlationID string) error {
	return s.allowedWithDetails(ctx, actor, action, resourceType, resourceID, correlationID, nil)
}

func (s *Service) allowedWithDetails(ctx context.Context, actor domain.Principal, action, resourceType, resourceID, correlationID string, details map[string]string) error {
	_, err := s.store.AppendAudit(ctx, domain.AuditEvent{ActorID: actor.ID, Action: action, ResourceType: resourceType, ResourceID: resourceID, CorrelationID: correlationID, Decision: "allowed", Details: details})
	return err
}

func hasRole(principal domain.Principal, role domain.Role) bool {
	for _, candidate := range principal.Roles {
		if candidate == role {
			return principal.ID != ""
		}
	}
	return false
}

func hasAnyRole(principal domain.Principal, roles ...domain.Role) bool {
	for _, role := range roles {
		if hasRole(principal, role) {
			return true
		}
	}
	return false
}

func scopeMember(scope domain.AuthorizationScope, principal domain.Principal) bool {
	if principal.ID == "" {
		return false
	}
	if hasRole(principal, domain.RoleAdmin) || scope.OperatorID == principal.ID {
		return true
	}
	for _, memberID := range scope.MemberIDs {
		if memberID == principal.ID {
			return true
		}
	}
	return false
}

func budgetExceeds(request, limit domain.ResourceBudget) bool {
	return over(request.MaxWallSeconds, limit.MaxWallSeconds) || over(request.MaxMemoryBytes, limit.MaxMemoryBytes) ||
		over(request.MaxCPUSeconds, limit.MaxCPUSeconds) || over(request.MaxDiskBytes, limit.MaxDiskBytes) ||
		over(request.MaxInodes, limit.MaxInodes) || over(request.MaxPIDs, limit.MaxPIDs) || (limit.MaxConcurrent > 0 && request.MaxConcurrent > limit.MaxConcurrent)
}

func over(request, limit int64) bool { return request < 0 || (limit > 0 && request > limit) }

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsOperation(values []domain.Operation, wanted domain.Operation) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func manifestHasHarness(manifest domain.ApparatusManifest, wanted string) bool {
	for _, harness := range manifest.Harnesses {
		if harness.Name == wanted {
			return true
		}
	}
	return false
}

func validApparatusArguments(job domain.WorkerJob) bool {
	for key, value := range job.Arguments {
		if value == "" {
			return false
		}
		switch key {
		case "manifest", "harness", "revision", "sanitizer", "max-total-time":
			if key == "max-total-time" && job.Operation != domain.OperationFuzz {
				return false
			}
		case "validation-kind":
			if job.Operation != domain.OperationRegressionTest || value != "reproducer" && value != "regression" && value != "negative_control" {
				return false
			}
		default:
			return false
		}
	}
	if job.Operation == domain.OperationRegressionTest && job.Arguments["validation-kind"] == "" {
		return false
	}
	return job.Arguments["manifest"] != "" && job.Arguments["harness"] != "" && job.Arguments["revision"] != "" && job.Arguments["sanitizer"] != ""
}

func validApparatusMounts(job domain.WorkerJob) bool {
	seen := map[string]bool{}
	for _, mount := range job.Mounts {
		if seen[mount.Name] {
			return false
		}
		seen[mount.Name] = true
		switch mount.Name {
		case "source":
			if mount.ContainerPath != "/source" || !mount.ReadOnly {
				return false
			}
		case "build":
			if mount.ContainerPath != "/build" || !mount.ReadOnly {
				return false
			}
		case "corpus":
			if mount.ContainerPath != "/corpus" || mount.ReadOnly {
				return false
			}
		case "evidence":
			if mount.ContainerPath != "/evidence" || !mount.ReadOnly {
				return false
			}
		case "patch":
			if mount.ContainerPath != "/patch" || !mount.ReadOnly {
				return false
			}
		default:
			return false
		}
	}
	if !seen["source"] || requiresBuild(job.Operation) && !seen["build"] || jobNeedsCorpus(job) && !seen["corpus"] || jobNeedsEvidence(job) && !seen["evidence"] || job.PatchArtifactID != "" && !seen["patch"] {
		return false
	}
	if seen["patch"] && (job.Operation != domain.OperationBuild || job.PatchArtifactID == "") {
		return false
	}
	return true
}

func requiresBuild(operation domain.Operation) bool {
	switch operation {
	case domain.OperationSmokeTest, domain.OperationFuzz, domain.OperationReproduce, domain.OperationMinimize,
		domain.OperationMergeCorpus, domain.OperationCoverage, domain.OperationSymbolize,
		domain.OperationCompareBranch, domain.OperationRegressionTest:
		return true
	default:
		return false
	}
}

func requiresCorpus(operation domain.Operation) bool {
	switch operation {
	case domain.OperationSeed, domain.OperationFuzz, domain.OperationMergeCorpus, domain.OperationCoverage, domain.OperationRegressionTest:
		return true
	default:
		return false
	}
}

func jobNeedsCorpus(job domain.WorkerJob) bool {
	if job.Operation == domain.OperationRegressionTest {
		return job.Arguments["validation-kind"] == "regression"
	}
	return requiresCorpus(job.Operation)
}

func operationNeedsEvidence(operation domain.Operation) bool {
	switch operation {
	case domain.OperationReproduce, domain.OperationMinimize, domain.OperationSymbolize, domain.OperationCompareBranch:
		return true
	default:
		return false
	}
}

func jobNeedsEvidence(job domain.WorkerJob) bool {
	if job.Operation == domain.OperationRegressionTest {
		return job.Arguments["validation-kind"] == "reproducer" || job.Arguments["validation-kind"] == "negative_control"
	}
	return operationNeedsEvidence(job.Operation)
}

func evidenceRoleAllowed(job domain.WorkerJob, role string) bool {
	if job.Operation == domain.OperationSymbolize {
		return role == "stderr_log" || role == "revision_comparison_log" || role == "regression_log"
	}
	if job.Operation == domain.OperationRegressionTest && job.Arguments["validation-kind"] == "negative_control" {
		return role == "corpus_seed" || role == "corpus_entry"
	}
	return role == "crashing_input" || role == "minimized_input"
}

func newID(prefix string) string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(id[:])
}

func deterministicID(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func acquisitionDeadline(ctx context.Context, seconds int64) (context.Context, context.CancelFunc) {
	maximum := int64(time.Duration(1<<63-1) / time.Second)
	if seconds > maximum {
		seconds = maximum
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

func (s *Service) targetAcquisitionPolicy(request TargetRequest) (string, error) {
	if request.SourceName == "" {
		if strings.TrimSpace(request.SourceDir) == "" {
			return "", errors.New("local source directory or fixed source name required")
		}
		return "", nil
	}
	if request.SourceName != strings.TrimSpace(request.SourceName) || strings.TrimSpace(request.SourceDir) != "" {
		return "", errors.New("fixed source name and local source directory are mutually exclusive")
	}
	if s.sourceBroker == nil {
		return "", fmt.Errorf("%w: fixed source acquisition is not configured", ErrSourceUnavailable)
	}
	descriptor, err := s.sourceBroker.Describe(request.SourceName, request.Revision)
	if err != nil {
		return "", err
	}
	if descriptor.Repository != request.Repository {
		return "", errors.New("fixed source repository does not match request")
	}
	return descriptor.Host, nil
}

func (s *Service) captureTarget(ctx context.Context, request TargetRequest, destination string, limits acquisition.Limits) (string, string, acquisition.Snapshot, domain.AcquisitionProvenance, error) {
	if request.SourceName == "" {
		checkout, snapshot, err := acquisition.CaptureGitCheckout(ctx, request.SourceDir, request.Revision, destination, limits)
		return checkout.RequestedRef, checkout.Commit, snapshot, domain.AcquisitionProvenance{Method: "local_git_object_export"}, err
	}
	result, err := s.sourceBroker.Fetch(ctx, request.SourceName, request.Revision, destination, limits)
	if err != nil {
		return "", "", acquisition.Snapshot{}, domain.AcquisitionProvenance{}, err
	}
	provenance := domain.AcquisitionProvenance{Method: "https_pinned_tar", SourceName: result.Descriptor.SourceName, SourceURL: result.Descriptor.URL,
		BundleSHA256: result.Descriptor.SHA256, BundleBytes: result.BundleBytes, ManifestKeyID: result.Descriptor.ManifestKeyID, ManifestExpiresAt: result.Descriptor.ManifestExpiresAt, FetchedAt: result.FetchedAt}
	return result.Descriptor.Commit, result.Descriptor.Commit, result.Snapshot, provenance, nil
}

func sameTargetIdentity(left, right domain.TargetRevision) bool {
	return left.CampaignID == right.CampaignID && left.Repository == right.Repository && left.RequestedRef == right.RequestedRef && left.Commit == right.Commit &&
		left.SourceSHA256 == right.SourceSHA256 && left.Language == right.Language && left.Architecture == right.Architecture &&
		left.Acquisition.Method == right.Acquisition.Method && left.Acquisition.SourceName == right.Acquisition.SourceName && left.Acquisition.SourceURL == right.Acquisition.SourceURL &&
		left.Acquisition.BundleSHA256 == right.Acquisition.BundleSHA256 && left.Acquisition.BundleBytes == right.Acquisition.BundleBytes && left.Acquisition.ManifestKeyID == right.Acquisition.ManifestKeyID && left.Acquisition.ManifestExpiresAt.Equal(right.Acquisition.ManifestExpiresAt)
}

func targetAcquisitionDetails(campaignID, findingID string, target domain.TargetRevision) map[string]string {
	details := map[string]string{"campaign_id": campaignID, "requested_ref": target.RequestedRef, "commit": target.Commit, "source_sha256": target.SourceSHA256, "acquisition_method": target.Acquisition.Method}
	if findingID != "" {
		details["finding_id"] = findingID
	}
	if target.Acquisition.SourceName != "" {
		details["source_name"] = target.Acquisition.SourceName
		details["bundle_sha256"] = target.Acquisition.BundleSHA256
		details["manifest_key_id"] = target.Acquisition.ManifestKeyID
	}
	return details
}

func campaignAllowsOperation(state domain.CampaignState, operation domain.Operation) bool {
	switch operation {
	case domain.OperationInspect, domain.OperationListHarnesses:
		return state != domain.CampaignDraft && state != domain.CampaignScoped && state != domain.CampaignPaused && state != domain.CampaignCancelled && state != domain.CampaignFailed
	case domain.OperationBuild:
		return state == domain.CampaignAcquired || state == domain.CampaignPrimitiveAssessed || state == domain.CampaignBranchChecked || state == domain.CampaignNoveltyReviewed
	case domain.OperationSmokeTest, domain.OperationSeed:
		return state == domain.CampaignBuildReady
	case domain.OperationFuzz:
		return state == domain.CampaignBuildReady || state == domain.CampaignFuzzing
	case domain.OperationReproduce:
		return state == domain.CampaignCrashObserved || state == domain.CampaignReproduced
	case domain.OperationMinimize:
		return state == domain.CampaignReproduced
	case domain.OperationMergeCorpus, domain.OperationCoverage:
		return state == domain.CampaignFuzzing || state == domain.CampaignCrashObserved || state == domain.CampaignReproduced
	case domain.OperationSymbolize:
		return state == domain.CampaignMinimized || state == domain.CampaignRootCaused
	case domain.OperationCompareBranch:
		return state == domain.CampaignPrimitiveAssessed || state == domain.CampaignBranchChecked
	case domain.OperationRegressionTest:
		return state == domain.CampaignNoveltyReviewed || state == domain.CampaignRemediated
	default:
		return false
	}
}
