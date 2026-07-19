package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestSignedJobRejectsTamperingAndExpiry(t *testing.T) {
	signer, err := GenerateSigner("key-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	signer.Now = func() time.Time { return now }
	job := domain.WorkerJob{ID: "job", RunID: "run", CampaignID: "campaign", Operation: domain.OperationFuzz}
	envelope, err := signer.Sign(job, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyJob(envelope, signer.KeyID, signer.PublicKey, now); err != nil {
		t.Fatal(err)
	}
	tampered := envelope
	tampered.Job.Operation = domain.OperationDisclose
	if err := VerifyJob(tampered, signer.KeyID, signer.PublicKey, now); err == nil {
		t.Fatal("accepted tampered operation")
	}
	if err := VerifyJob(envelope, signer.KeyID, signer.PublicKey, envelope.ExpiresAt); err == nil {
		t.Fatal("accepted expired envelope")
	}
}

func TestLeaseIsShortLivedJobBoundAndOneTime(t *testing.T) {
	authority := NewLeaseAuthority()
	now := time.Now().UTC()
	authority.now = func() time.Time { return now }
	lease, err := authority.Issue("job", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Consume("other", "worker", lease.Token); err == nil {
		t.Fatal("lease crossed job boundary")
	}
	if err := authority.Consume("job", "worker", lease.Token); err != nil {
		t.Fatal(err)
	}
	if err := authority.Consume("job", "worker", lease.Token); err == nil {
		t.Fatal("lease replay succeeded")
	}
}

func TestRemoteArtifactHashAndSizeVerification(t *testing.T) {
	content := "evidence"
	digest := sha256.Sum256([]byte(content))
	contentID := "sha256:" + hex.EncodeToString(digest[:])
	if err := VerifyArtifact(context.Background(), strings.NewReader(content), contentID, int64(len(content)), 100); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArtifact(context.Background(), strings.NewReader(content+"x"), contentID, int64(len(content)), 100); err == nil {
		t.Fatal("accepted size/hash mismatch")
	}
}
