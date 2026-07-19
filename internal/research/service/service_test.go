package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

func TestRegisterApparatusRequiresVerifiedSupplyChainAdmission(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()

	_, err := svc.RegisterApparatus(ctx, domain.Principal{ID: "admin", Roles: []domain.Role{domain.RoleAdmin}}, domain.ApparatusManifest{ID: "untrusted"})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "supply-chain admission required") {
		t.Fatalf("untrusted apparatus registration error=%v", err)
	}
	if _, err := repository.GetApparatus(ctx, "untrusted"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("untrusted apparatus was persisted: %v", err)
	}
}

func TestServiceEnforcesScopeOwnershipBudgetAndEvidenceTransitions(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	other := domain.Principal{ID: "other", Roles: []domain.Role{domain.RoleOperator}}
	now := time.Now().UTC()
	work := filepath.Join(repository.Root(), "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "authorized test", TargetRepository: "https://example.test/owned.git", AllowedRevisions: []string{"abc"},
		WorkspaceRoots:     []string{work},
		AllowedOperations:  []domain.Operation{domain.OperationInspect, domain.OperationFuzz, domain.OperationDisclose},
		ApprovalOperations: []domain.Operation{domain.OperationFuzz, domain.OperationDisclose},
		Budget:             domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxCPUSeconds: 60, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 16, MaxConcurrent: 1},
		ExpiresAt:          now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateCampaign(ctx, other, domain.Campaign{ScopeID: scope.ID, Name: "stolen"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other operator campaign error=%v", err)
	}
	if _, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "too large", Budget: domain.ResourceBudget{MaxWallSeconds: 61}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("budget error=%v", err)
	}
	campaign, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Transition(ctx, operator, campaign.ID, domain.CampaignAcquired, domain.EvidenceFacts{TargetID: "target", SourceSHA256: "hash"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("skipped state error=%v", err)
	}
	campaign, err = svc.Transition(ctx, operator, campaign.ID, domain.CampaignScoped, domain.EvidenceFacts{ScopeValid: true})
	if err != nil || campaign.State != domain.CampaignScoped || campaign.Version != 2 {
		t.Fatalf("campaign=%#v error=%v", campaign, err)
	}
	// Repeating the achieved transition is idempotent and does not add a version.
	repeated, err := svc.Transition(ctx, operator, campaign.ID, domain.CampaignScoped, domain.EvidenceFacts{})
	if err != nil || repeated.Version != campaign.Version {
		t.Fatalf("idempotent campaign=%#v error=%v", repeated, err)
	}
	if err := repository.VerifyAuditChain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalMustBeIndependentAndMatchAction(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	reviewer := domain.Principal{ID: "reviewer", Roles: []domain.Role{domain.RoleReviewer}}
	now := time.Now().UTC()
	work := filepath.Join(repository.Root(), "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "authorized test", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{work},
		MemberIDs:         []string{reviewer.ID},
		AllowedOperations: []domain.Operation{domain.OperationFuzz}, ApprovalOperations: []domain.Operation{domain.OperationFuzz},
		Budget: domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxCPUSeconds: 60, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 16, MaxConcurrent: 1}, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	action := domain.Action{Principal: operator, Operation: domain.OperationFuzz, Repository: "repo", Revision: "abc", WorkspacePath: work, WallSeconds: 10}
	decision, err := svc.AuthorizeAction(ctx, campaign.ID, action, "correlation")
	if err != nil || decision.Allowed || !decision.ApprovalRequired {
		t.Fatalf("preapproval decision=%#v err=%v", decision, err)
	}
	approval, err := svc.RequestApproval(ctx, operator, domain.Approval{ScopeID: scope.ID, CampaignID: campaign.ID, CorrelationID: "correlation", Operation: domain.OperationFuzz, Reason: "bounded fixture run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestApproval(ctx, domain.Principal{ID: "outsider", Roles: []domain.Role{domain.RoleOperator}}, domain.Approval{ScopeID: scope.ID, CampaignID: campaign.ID, CorrelationID: "outsider", Operation: domain.OperationFuzz, Reason: "not a member"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member requested approval: %v", err)
	}
	if _, err := svc.DecideApproval(ctx, operator, approval.ID, true, "self approve"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval error=%v", err)
	}
	approval, err = svc.DecideApproval(ctx, reviewer, approval.ID, true, "scope and budget reviewed")
	if err != nil || approval.Status != "approved" {
		t.Fatalf("approval=%#v error=%v", approval, err)
	}
	action.ApprovalID = approval.ID
	decision, err = svc.AuthorizeAction(ctx, campaign.ID, action, "wrong-correlation")
	if err != nil || decision.Allowed {
		t.Fatalf("mismatch decision=%#v err=%v", decision, err)
	}
	decision, err = svc.AuthorizeAction(ctx, campaign.ID, action, "correlation")
	if err != nil || !decision.Allowed {
		t.Fatalf("approved decision=%#v err=%v", decision, err)
	}
}

func TestArtifactPurgeRequiresTerminalRetentionAdminAndBoundIndependentApproval(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	reviewer := domain.Principal{ID: "reviewer", Roles: []domain.Role{domain.RoleReviewer}}
	admin := domain.Principal{ID: "admin", Roles: []domain.Role{domain.RoleAdmin}}
	work := filepath.Join(repository.Root(), "work-purge")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "authorized retention test", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{work},
		MemberIDs: []string{reviewer.ID}, AllowedOperations: []domain.Operation{domain.OperationInspect, domain.OperationPurgeArtifact},
		ApprovalOperations: []domain.Operation{domain.OperationPurgeArtifact},
		Budget:             domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxCPUSeconds: 60, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 16, MaxConcurrent: 1}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "purge fixture"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repository.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("embargoed"))
	if err != nil {
		t.Fatal(err)
	}
	correlationID := ArtifactPurgeCorrelation(artifact.ID)
	request := domain.Approval{ScopeID: scope.ID, CampaignID: campaign.ID, CorrelationID: correlationID, Operation: domain.OperationPurgeArtifact, Reason: "retention expired"}
	if _, err := svc.RequestApproval(ctx, operator, request); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("nonterminal purge approval accepted: %v", err)
	}
	if _, err := svc.Transition(ctx, operator, campaign.ID, domain.CampaignCancelled, domain.EvidenceFacts{}); err != nil {
		t.Fatal(err)
	}
	future := artifact.RetainUntil.Add(time.Minute)
	svc.now = func() time.Time { return future }
	request.CorrelationID = "artifact-purge:other"
	if _, err := svc.RequestApproval(ctx, operator, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unbound purge approval accepted: %v", err)
	}
	request.CorrelationID = correlationID
	approval, err := svc.RequestApproval(ctx, operator, request)
	if err != nil {
		t.Fatal(err)
	}
	approval, err = svc.DecideApproval(ctx, reviewer, approval.ID, true, "independently reviewed custody and backups")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return approval.DecidedAt.Add(artifactPurgeApprovalLifetime + time.Second) }
	if _, err := svc.PurgeArtifact(ctx, admin, artifact.ID, approval.ID, "stale approval attempt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale purge approval accepted: %v", err)
	}
	svc.now = func() time.Time { return future }
	if _, err := svc.PurgeArtifact(ctx, operator, artifact.ID, approval.ID, "approved lifecycle cleanup"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin purged artifact: %v", err)
	}
	purged, err := svc.PurgeArtifact(ctx, admin, artifact.ID, approval.ID, "approved lifecycle cleanup")
	if err != nil || purged.PurgedAt == nil || purged.BlobDeletedAt == nil || purged.PurgeApprovalID != approval.ID {
		t.Fatalf("purged=%#v err=%v", purged, err)
	}
	if _, reader, err := repository.OpenArtifact(ctx, artifact.ID); !errors.Is(err, store.ErrArtifactPurged) {
		if reader != nil {
			reader.Close()
		}
		t.Fatalf("purged artifact readable: %v", err)
	}
	if err := repository.VerifyAuditChain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFixedWorkspaceRootsCannotBeExpandedByScope(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	fixed := t.TempDir()
	outside := t.TempDir()
	if err := svc.ConfigureWorkspaceRoots([]string{fixed}); err != nil {
		t.Fatal(err)
	}
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	_, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "must stay fixed", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{outside},
		AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: domain.ResourceBudget{MaxWallSeconds: 1, MaxMemoryBytes: 1024, MaxCPUSeconds: 1, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("outside fixed root error=%v", err)
	}
}

func TestExpiredScopeCannotAuthorizeOrTransition(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	base := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "short test", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{repository.Root()},
		AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: domain.ResourceBudget{MaxWallSeconds: 1, MaxMemoryBytes: 1024, MaxCPUSeconds: 1, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1}, ExpiresAt: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := svc.Transition(ctx, operator, campaign.ID, domain.CampaignScoped, domain.EvidenceFacts{ScopeValid: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired transition error=%v", err)
	}
}

func TestUnapprovedOrOutOfScopeJobNeverReachesBroker(t *testing.T) {
	ctx := context.Background()
	svc, repository := newTestService(t)
	defer repository.Close()
	broker := &recordingBroker{}
	if err := svc.AttachBroker(broker); err != nil {
		t.Fatal(err)
	}
	operator := domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}
	reviewer := domain.Principal{ID: "reviewer", Roles: []domain.Role{domain.RoleReviewer}}
	root := filepath.Join(repository.Root(), "work")
	internalRoot := filepath.Join(repository.Root(), "internal")
	if err := os.MkdirAll(internalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfigureInternalRoot(internalRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := domain.ApparatusManifest{SchemaVersion: 1, ID: "apparatus", Name: "test", Version: "1", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Engine: "libfuzzer",
		Sanitizers: []string{"address"}, Architectures: []string{"amd64"}, Harnesses: []domain.HarnessManifest{{Name: "parser", Binary: "/build/fuzz_target"}}, Operations: []domain.Operation{domain.OperationFuzz},
		Limits: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxCPUSeconds: 30, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1}}
	manifest = attachTestApparatusAdmission(t, svc, manifest, now)
	if err := repository.SaveApparatus(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "authorized runner test", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{root},
		MemberIDs:         []string{reviewer.ID},
		AllowedOperations: []domain.Operation{domain.OperationFuzz}, ApprovalOperations: []domain.Operation{domain.OperationFuzz},
		AllowedApparatusIDs: []string{manifest.ID},
		Budget:              domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxCPUSeconds: 30, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1}, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := svc.CreateCampaign(ctx, operator, domain.Campaign{ScopeID: scope.ID, Name: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	previousVersion := campaign.Version
	campaign.State = domain.CampaignFuzzing
	campaign.Version++
	campaign.UpdatedAt = now
	if err := repository.UpdateCampaign(ctx, campaign, previousVersion); err != nil {
		t.Fatal(err)
	}
	buildDir := filepath.Join(internalRoot, campaign.ID, "builds", "build")
	corpusDir := filepath.Join(internalRoot, campaign.ID, "corpora", "parser")
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(corpusDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveBuild(ctx, domain.Build{ID: "build", CampaignID: campaign.ID, ManifestID: manifest.ID, ImageDigest: manifest.ImageDigest,
		Sanitizer: "address", Status: string(domain.RunCompleted), Provenance: map[string]string{"harness": "parser"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	job := domain.WorkerJob{CampaignID: campaign.ID, ScopeID: scope.ID, Operation: domain.OperationFuzz, AuditCorrelationID: "correlation", Budget: scope.Budget,
		BuildID:     "build",
		ImageDigest: manifest.ImageDigest, Arguments: map[string]string{"manifest": manifest.ID, "harness": "parser", "revision": "abc", "sanitizer": "address"},
		ArtifactRules: apparatus.ArtifactRules(domain.OperationFuzz),
		Mounts: []domain.JobMount{
			{Name: "source", HostPath: root, ContainerPath: "/source", ReadOnly: true},
			{Name: "build", HostPath: buildDir, ContainerPath: "/build", ReadOnly: true},
			{Name: "corpus", HostPath: corpusDir, ContainerPath: "/corpus", ReadOnly: false},
		}}
	if _, err := svc.Enqueue(ctx, operator, campaign.ID, job, ""); !errors.Is(err, ErrForbidden) || broker.calls != 0 {
		t.Fatalf("unapproved err=%v calls=%d", err, broker.calls)
	}
	approval, err := svc.RequestApproval(ctx, operator, domain.Approval{ScopeID: scope.ID, CampaignID: campaign.ID, CorrelationID: "correlation", Operation: domain.OperationFuzz, Reason: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	approval, err = svc.DecideApproval(ctx, reviewer, approval.ID, true, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	job.Mounts[0].HostPath = filepath.Join(repository.Root(), "outside")
	if err := os.MkdirAll(job.Mounts[0].HostPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enqueue(ctx, operator, campaign.ID, job, approval.ID); !errors.Is(err, ErrForbidden) || broker.calls != 0 {
		t.Fatalf("out-of-scope err=%v calls=%d", err, broker.calls)
	}
	job.Mounts[0].HostPath = root
	if _, err := svc.Enqueue(ctx, operator, campaign.ID, job, approval.ID); err != nil || broker.calls != 1 {
		t.Fatalf("approved err=%v calls=%d", err, broker.calls)
	}
}

type recordingBroker struct{ calls int }

func (b *recordingBroker) Submit(_ context.Context, job domain.WorkerJob) (domain.ExperimentRun, error) {
	b.calls++
	return domain.ExperimentRun{ID: "run", CampaignID: job.CampaignID, ScopeID: job.ScopeID, Operation: job.Operation, Status: domain.RunQueued}, nil
}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	repository, err := store.Open(context.Background(), store.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(repository, 3)
	if err != nil {
		repository.Close()
		t.Fatal(err)
	}
	return svc, repository
}

func attachTestApparatusAdmission(t *testing.T, svc *Service, manifest domain.ApparatusManifest, now time.Time) domain.ApparatusManifest {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sbom := json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/service-sbom","packages":[{"name":"fixture","SPDXID":"SPDXRef-Package","versionInfo":"1.0.0","checksums":[{"algorithm":"SHA256","checksumValue":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}]}`)
	provenance := json.RawMessage(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://example.test/build/v1","resolvedDependencies":[{"uri":"pkg:docker/base@1","digest":{"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}]},"runDetails":{"builder":{"id":"https://builder.example.test/research"}}}}`)
	envelope, err := apparatus.SignAdmissionCatalog(apparatus.AdmissionCatalog{SchemaVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), Entries: []apparatus.AdmissionEntry{{Manifest: manifest, SBOM: sbom, Provenance: provenance}}}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := apparatus.VerifyAdmissionCatalog(envelope, publicKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachApparatusAdmission(verified); err != nil {
		t.Fatal(err)
	}
	admitted, err := verified.Admit(manifest, now)
	if err != nil {
		t.Fatal(err)
	}
	return admitted
}
