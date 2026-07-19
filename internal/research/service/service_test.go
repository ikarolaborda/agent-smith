package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

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
		Budget:             domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 16, MaxConcurrent: 1},
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
		Budget: domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 16}, ExpiresAt: now.Add(time.Hour),
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
		AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: domain.ResourceBudget{MaxWallSeconds: 1}, ExpiresAt: time.Now().UTC().Add(time.Hour),
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
		AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: domain.ResourceBudget{MaxWallSeconds: 1}, ExpiresAt: base.Add(time.Minute),
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
		Limits: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 8}}
	if err := repository.SaveApparatus(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	scope, err := svc.CreateScope(ctx, operator, domain.AuthorizationScope{
		Purpose: "authorized runner test", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{root},
		MemberIDs:         []string{reviewer.ID},
		AllowedOperations: []domain.Operation{domain.OperationFuzz}, ApprovalOperations: []domain.Operation{domain.OperationFuzz},
		AllowedApparatusIDs: []string{manifest.ID},
		Budget:              domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 8}, ExpiresAt: now.Add(time.Hour),
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
