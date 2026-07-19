package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestStorePersistsScopeCampaignAndTypedRecords(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "research")
	s := openTestStore(t, root, 0)
	scope, campaign := seedCampaign(t, s)

	now := time.Now().UTC().Truncate(time.Microsecond)
	target := domain.TargetRevision{
		SchemaVersion: 1, ID: "target-1", CampaignID: campaign.ID, Repository: scope.TargetRepository,
		RequestedRef: "main", Commit: "0123456789abcdef", SourceSHA256: strings.Repeat("a", 64),
		Language: "c++", Architecture: "amd64", AcquiredAt: now,
	}
	if err := s.SaveTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTarget(ctx, target); err == nil {
		t.Fatal("immutable target accepted a duplicate write")
	}
	run := domain.ExperimentRun{
		SchemaVersion: 1, ID: "run-1", CampaignID: campaign.ID, ScopeID: scope.ID,
		Operation: domain.OperationFuzz, Status: domain.RunQueued, CreatedAt: now,
	}
	if err := s.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTestStore(t, root, 0)
	defer s.Close()
	gotScope, err := s.GetScope(ctx, scope.ID)
	if err != nil || gotScope.TargetRepository != scope.TargetRepository {
		t.Fatalf("scope after reopen: %#v, %v", gotScope, err)
	}
	gotTarget, err := s.GetTarget(ctx, target.ID)
	if err != nil || gotTarget.SourceSHA256 != target.SourceSHA256 {
		t.Fatalf("target after reopen: %#v, %v", gotTarget, err)
	}
	gotRun, err := s.GetRun(ctx, run.ID)
	if err != nil || gotRun.Operation != domain.OperationFuzz {
		t.Fatalf("run after reopen: %#v, %v", gotRun, err)
	}
	var migrations int
	if err := s.Database().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil || migrations != 2 {
		t.Fatalf("migrations=%d err=%v", migrations, err)
	}
}

func TestResourceMigrationBackfillsFiniteBudgets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := openTestStore(t, root, 0)
	scope, campaign := seedCampaign(t, s)
	var scopeData, campaignData []byte
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM authorization_scopes WHERE id = ?`, scope.ID).Scan(&scopeData); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM campaigns WHERE id = ?`, campaign.ID).Scan(&campaignData); err != nil {
		t.Fatal(err)
	}
	var oldScope domain.AuthorizationScope
	var oldCampaign domain.Campaign
	if err := json.Unmarshal(scopeData, &oldScope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(campaignData, &oldCampaign); err != nil {
		t.Fatal(err)
	}
	oldScope.Budget.MaxCPUSeconds, oldScope.Budget.MaxInodes, oldScope.Budget.MaxConcurrent = 0, 0, 0
	oldCampaign.Budget.MaxCPUSeconds, oldCampaign.Budget.MaxInodes, oldCampaign.Budget.MaxConcurrent = 0, 0, 0
	scopeData, _ = json.Marshal(oldScope)
	campaignData, _ = json.Marshal(oldCampaign)
	if _, err := s.db.ExecContext(ctx, `UPDATE authorization_scopes SET data = ? WHERE id = ?`, scopeData, scope.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE campaigns SET data = ? WHERE id = ?`, campaignData, campaign.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, root, 0)
	defer s.Close()
	migratedScope, err := s.GetScope(ctx, scope.ID)
	if err != nil || migratedScope.Budget.Validate() != nil || migratedScope.Budget.MaxInodes == 0 {
		t.Fatalf("scope=%#v err=%v", migratedScope, err)
	}
	migratedCampaign, err := s.GetCampaign(ctx, campaign.ID)
	if err != nil || migratedCampaign.Budget.Validate() != nil || migratedCampaign.Budget.MaxInodes != migratedScope.Budget.MaxInodes {
		t.Fatalf("campaign=%#v err=%v", migratedCampaign, err)
	}
}

func TestCampaignOptimisticUpdateRejectsStaleWriter(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir(), 0)
	defer s.Close()
	_, campaign := seedCampaign(t, s)

	first := campaign
	first.State = domain.CampaignScoped
	first.Version++
	first.UpdatedAt = time.Now().UTC()
	if err := s.UpdateCampaign(ctx, first, campaign.Version); err != nil {
		t.Fatal(err)
	}
	stale := campaign
	stale.State = domain.CampaignFailed
	stale.Version++
	if err := s.UpdateCampaign(ctx, stale, campaign.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	got, err := s.GetCampaign(ctx, campaign.ID)
	if err != nil || got.State != domain.CampaignScoped || got.Version != 2 {
		t.Fatalf("campaign=%#v err=%v", got, err)
	}
}

func TestArtifactCASDedupeBoundsPermissionsAndIntegrity(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private")
	s := openTestStore(t, root, 5)
	defer s.Close()
	_, campaign := seedCampaign(t, s)

	meta := domain.Artifact{SchemaVersion: 1, ID: "artifact-1", CampaignID: campaign.ID, Role: "crashing_input", MediaType: "application/octet-stream", Sensitivity: "embargoed"}
	first, err := s.PutArtifact(ctx, meta, strings.NewReader("crash"))
	if err != nil {
		t.Fatal(err)
	}
	meta.ID = "artifact-2"
	meta.ParentIDs = []string{first.ID}
	second, err := s.PutArtifact(ctx, meta, strings.NewReader("crash"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentID != second.ContentID || first.StoragePath != second.StoragePath {
		t.Fatalf("identical artifacts not deduplicated: %#v %#v", first, second)
	}
	if _, err := s.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("123456")); !errors.Is(err, ErrArtifactTooLarge) {
		t.Fatalf("oversized artifact error=%v", err)
	}

	for _, path := range []string{root, filepath.Join(root, "artifacts")} {
		stat, err := os.Stat(path)
		if err != nil || stat.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode=%v err=%v", path, stat.Mode().Perm(), err)
		}
	}
	dbStat, err := os.Stat(filepath.Join(root, "research.sqlite"))
	if err != nil || dbStat.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%v err=%v", dbStat.Mode().Perm(), err)
	}
	objectPath := filepath.Join(root, "artifacts", filepath.FromSlash(first.StoragePath))
	objectStat, err := os.Stat(objectPath)
	if err != nil || objectStat.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%v err=%v", objectStat.Mode().Perm(), err)
	}
	_, file, err := s.OpenArtifact(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(contents, []byte("crash")) {
		t.Fatalf("artifact bytes=%q err=%v", contents, err)
	}
	if err := os.WriteFile(objectPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, file, err := s.OpenArtifact(ctx, first.ID); err == nil {
		file.Close()
		t.Fatal("tampered artifact was accepted")
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir(), 0)
	defer s.Close()
	for _, action := range []string{"campaign.created", "campaign.scoped", "run.queued"} {
		if _, err := s.AppendAudit(ctx, domain.AuditEvent{
			ActorID: "operator-1", Action: action, ResourceType: "campaign", ResourceID: "campaign-1",
			CorrelationID: "correlation-1", Decision: "allowed", Details: map[string]string{"action": action},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.VerifyAuditChain(ctx); err != nil {
		t.Fatalf("valid chain: %v", err)
	}
	events, err := s.ListAudit(ctx, 0, 10)
	if err != nil || len(events) != 3 || events[1].PreviousHash != events[0].Hash {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if _, err := s.Database().ExecContext(ctx, `UPDATE audit_events SET details_json = ? WHERE sequence = 2`, []byte(`{"action":"altered"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAuditChain(ctx); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("tampered chain error=%v", err)
	}
}

func TestAuditVerificationPaginatesEntireChain(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for index := 0; index < 105; index++ {
		if _, err := s.AppendAudit(ctx, domain.AuditEvent{ActorID: "operator", Action: "test", ResourceType: "campaign", ResourceID: "campaign"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_events SET action = 'tampered' WHERE sequence = 105`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAuditChain(ctx); err == nil {
		t.Fatal("tampering beyond the first verification page was not detected")
	}
}

func TestMissingObjectReturnsSentinel(t *testing.T) {
	s := openTestStore(t, t.TempDir(), 0)
	defer s.Close()
	if _, err := s.GetCampaign(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error=%v", err)
	}
}

func openTestStore(t *testing.T, root string, maxArtifactBytes int64) *Store {
	t.Helper()
	s, err := Open(context.Background(), Config{Root: root, MaxArtifactBytes: maxArtifactBytes})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seedCampaign(t *testing.T, s *Store) (domain.AuthorizationScope, domain.Campaign) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := domain.AuthorizationScope{
		SchemaVersion: 1, ID: "scope-1", OperatorID: "operator-1", Purpose: "authorized fixture research",
		TargetRepository: "https://example.test/owned.git", AllowedRevisions: []string{"main"},
		WorkspaceRoots: []string{s.Root()}, AllowedOperations: []domain.Operation{domain.OperationInspect, domain.OperationFuzz},
		Budget:    domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1 << 30, MaxCPUSeconds: 60, MaxDiskBytes: 1 << 30, MaxInodes: 4096, MaxPIDs: 64, MaxConcurrent: 1},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := s.CreateScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		SchemaVersion: 1, ID: "campaign-1", ScopeID: scope.ID, Name: "fixture", Goal: "find known bug",
		State: domain.CampaignDraft, Budget: scope.Budget, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scope, campaign
}
