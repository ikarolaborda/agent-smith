package store

import (
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

func TestArtifactRetentionPurgePreservesTombstonesAndSharedCAS(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, Config{Root: root, ArtifactRetention: MinimumArtifactRetention})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope, firstCampaign := seedCampaign(t, s)
	secondCampaign, err := s.CreateCampaign(ctx, domain.Campaign{
		SchemaVersion: 1, ID: "campaign-2", ScopeID: scope.ID, Name: "second", Goal: "shared evidence",
		State: domain.CampaignCancelled, Budget: scope.Budget, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.PutArtifact(ctx, domain.Artifact{ID: "artifact-retain-1", CampaignID: firstCampaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("shared evidence"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutArtifact(ctx, domain.Artifact{ID: "artifact-retain-2", CampaignID: secondCampaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("shared evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RetainUntil.IsZero() || first.ContentID != second.ContentID {
		t.Fatalf("retention/CAS metadata first=%#v second=%#v", first, second)
	}
	if _, err := s.PurgeArtifact(ctx, first.ID, "approval-1", "retention test", first.RetainUntil.Add(-time.Nanosecond)); !errors.Is(err, ErrRetentionActive) {
		t.Fatalf("active retention bypassed: %v", err)
	}

	purgeTime := latestTime(first.RetainUntil, second.RetainUntil).Add(time.Second)
	first, err = s.PurgeArtifact(ctx, first.ID, "approval-1", "retention test", purgeTime)
	if err != nil || first.PurgedAt == nil || first.BlobDeletedAt != nil {
		t.Fatalf("first tombstone=%#v err=%v", first, err)
	}
	if _, reader, err := s.OpenArtifact(ctx, first.ID); !errors.Is(err, ErrArtifactPurged) {
		if reader != nil {
			reader.Close()
		}
		t.Fatalf("purged logical artifact remained readable: %v", err)
	}
	_, reader, err := s.OpenArtifact(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	if readErr != nil || string(body) != "shared evidence" {
		t.Fatalf("active shared reference body=%q err=%v", body, readErr)
	}
	type purgeResult struct {
		artifact domain.Artifact
		err      error
	}
	started := make(chan struct{})
	result := make(chan purgeResult, 1)
	go func() {
		close(started)
		purged, purgeErr := s.PurgeArtifact(ctx, second.ID, "approval-2", "final shared purge", purgeTime)
		result <- purgeResult{artifact: purged, err: purgeErr}
	}()
	<-started
	select {
	case early := <-result:
		t.Fatalf("purge did not wait for active reader: %#v", early)
	case <-time.After(20 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	completed := <-result
	second, err = completed.artifact, completed.err
	if err != nil || second.BlobDeletedAt == nil {
		t.Fatalf("second tombstone=%#v err=%v", second, err)
	}
	first, err = s.GetArtifact(ctx, first.ID)
	if err != nil || first.BlobDeletedAt == nil || first.PurgeApprovalID != "approval-1" {
		t.Fatalf("updated first tombstone=%#v err=%v", first, err)
	}
	objectPath := filepath.Join(root, "artifacts", filepath.FromSlash(first.StoragePath))
	if _, err := os.Lstat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced blob still exists: %v", err)
	}
	if _, err := s.PurgeArtifact(ctx, first.ID, "different", "retention test", purgeTime); err == nil || !strings.Contains(err.Error(), "different immutable") {
		t.Fatalf("purge tombstone was mutable: %v", err)
	}
}

func TestArtifactBlobPathsRequireHexSHA256(t *testing.T) {
	s := &Store{artifactRoot: t.TempDir()}
	if _, err := s.validatedArtifactBlobPath(storedArtifactBlob{
		contentID: "sha256:" + strings.Repeat(".", 64), storagePath: filepath.Join("blobs", "..", strings.Repeat(".", 64)), size: 1,
	}); err == nil || !strings.Contains(err.Error(), "invalid stored artifact identity") {
		t.Fatalf("non-hex content identity accepted: %v", err)
	}
}

func TestArtifactRetentionConfigurationHasSafeBounds(t *testing.T) {
	for _, retention := range []time.Duration{time.Hour, MaximumArtifactRetention + time.Hour} {
		if opened, err := Open(context.Background(), Config{Root: filepath.Join(t.TempDir(), "store"), ArtifactRetention: retention}); err == nil {
			opened.Close()
			t.Fatalf("unsafe retention %s accepted", retention)
		}
	}
}

func TestArtifactPurgeRefusesCorruptEvidenceBeforeTombstone(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, Config{Root: root, ArtifactRetention: MinimumArtifactRetention})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, campaign := seedCampaign(t, s)
	artifact, err := s.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("original"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "artifacts", filepath.FromSlash(artifact.StoragePath))
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeArtifact(ctx, artifact.ID, "approval", "must not hide corruption", artifact.RetainUntil.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "verify artifact before tombstone") {
		t.Fatalf("corrupt artifact tombstoned: %v", err)
	}
	unchanged, err := s.GetArtifact(ctx, artifact.ID)
	if err != nil || unchanged.PurgedAt != nil {
		t.Fatalf("corrupt artifact lifecycle changed: %#v err=%v", unchanged, err)
	}
}

func TestApprovedPurgeRecoveryDeletesOrphanAndRetentionMigrationBackfills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := openTestStore(t, root, 0)
	_, campaign := seedCampaign(t, s)
	artifact, err := s.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "log", MediaType: "text/plain"}, strings.NewReader("recover me"))
	if err != nil {
		t.Fatal(err)
	}
	purgedAt := time.Now().UTC()
	artifact.RetainUntil = time.Time{}
	artifact.PurgedAt = &purgedAt
	artifact.PurgeApprovalID = "approval-recovery"
	artifact.PurgeReason = "approved before simulated crash"
	data, _ := json.Marshal(artifact)
	if _, err := s.db.ExecContext(ctx, `UPDATE artifacts SET data = ? WHERE id = ?`, data, artifact.ID); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, "artifacts", filepath.FromSlash(artifact.StoragePath))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recovered, err := s.GetArtifact(ctx, artifact.ID)
	if err != nil || recovered.RetainUntil.IsZero() || recovered.BlobDeletedAt == nil || recovered.PurgeApprovalID != "approval-recovery" {
		t.Fatalf("recovered tombstone=%#v err=%v", recovered, err)
	}
	if _, err := os.Lstat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved orphan blob survived restart: %v", err)
	}
}

func latestTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
