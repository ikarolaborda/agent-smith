// Package remote defines the cryptographic boundary for future remote research
// workers. It contains no model/provider credentials and no arbitrary commands.
package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const (
	MaxEnvelopeLifetime        = 5 * time.Minute
	DefaultArtifactLimit int64 = 256 << 20
)

// SignedJob is an Ed25519-authenticated, short-lived worker envelope.
type SignedJob struct {
	SchemaVersion int              `json:"schema_version"`
	KeyID         string           `json:"key_id"`
	Nonce         string           `json:"nonce"`
	IssuedAt      time.Time        `json:"issued_at"`
	ExpiresAt     time.Time        `json:"expires_at"`
	Job           domain.WorkerJob `json:"job"`
	Signature     string           `json:"signature"`
}

// Signer holds the control-plane signing identity; workers receive only PublicKey.
type Signer struct {
	KeyID      string
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	Now        func() time.Time
}

func GenerateSigner(keyID string) (Signer, error) {
	if keyID == "" {
		return Signer{}, errors.New("remote: key id required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Signer{}, err
	}
	return Signer{KeyID: keyID, PrivateKey: private, PublicKey: public, Now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s Signer) Sign(job domain.WorkerJob, lifetime time.Duration) (SignedJob, error) {
	if len(s.PrivateKey) != ed25519.PrivateKeySize || s.KeyID == "" || job.ID == "" || job.RunID == "" {
		return SignedJob{}, errors.New("remote: signing identity and durable job ids required")
	}
	if lifetime <= 0 || lifetime > MaxEnvelopeLifetime {
		return SignedJob{}, errors.New("remote: envelope lifetime outside policy")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	envelope := SignedJob{SchemaVersion: 1, KeyID: s.KeyID, Nonce: randomToken(16), IssuedAt: now, ExpiresAt: now.Add(lifetime), Job: job}
	payload, err := signingPayload(envelope)
	if err != nil {
		return envelope, err
	}
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.PrivateKey, payload))
	return envelope, nil
}

// VerifyJob rejects tampering, clock-invalid envelopes, wrong keys, and long leases.
func VerifyJob(envelope SignedJob, keyID string, publicKey ed25519.PublicKey, now time.Time) error {
	if envelope.SchemaVersion != 1 || envelope.KeyID != keyID || envelope.Nonce == "" || envelope.Job.ID == "" || envelope.Job.RunID == "" {
		return errors.New("remote: invalid envelope identity")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("remote: invalid public key")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if envelope.ExpiresAt.Sub(envelope.IssuedAt) <= 0 || envelope.ExpiresAt.Sub(envelope.IssuedAt) > MaxEnvelopeLifetime || now.Before(envelope.IssuedAt.Add(-30*time.Second)) || !now.Before(envelope.ExpiresAt) {
		return errors.New("remote: job envelope expired or outside clock policy")
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return errors.New("remote: invalid signature encoding")
	}
	payload, err := signingPayload(envelope)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("remote: job envelope signature mismatch")
	}
	return nil
}

func signingPayload(envelope SignedJob) ([]byte, error) {
	envelope.Signature = ""
	return json.Marshal(envelope)
}

// Lease is a one-job, one-worker bearer identity, not a control-plane secret.
type Lease struct {
	Token     string    `json:"token"`
	JobID     string    `json:"job_id"`
	WorkerID  string    `json:"worker_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type leaseRecord struct {
	digest    [sha256.Size]byte
	jobID     string
	workerID  string
	expiresAt time.Time
	consumed  bool
}

// LeaseAuthority issues and consumes in-memory short-lived workload identities.
type LeaseAuthority struct {
	mu      sync.Mutex
	records map[string]*leaseRecord
	now     func() time.Time
}

func NewLeaseAuthority() *LeaseAuthority {
	return &LeaseAuthority{records: map[string]*leaseRecord{}, now: func() time.Time { return time.Now().UTC() }}
}

func (a *LeaseAuthority) Issue(jobID, workerID string, lifetime time.Duration) (Lease, error) {
	if jobID == "" || workerID == "" || lifetime <= 0 || lifetime > MaxEnvelopeLifetime {
		return Lease{}, errors.New("remote: invalid workload lease")
	}
	token := randomToken(32)
	expires := a.now().Add(lifetime)
	digest := sha256.Sum256([]byte(token))
	a.mu.Lock()
	a.records[jobID] = &leaseRecord{digest: digest, jobID: jobID, workerID: workerID, expiresAt: expires}
	a.mu.Unlock()
	return Lease{Token: token, JobID: jobID, WorkerID: workerID, ExpiresAt: expires}, nil
}

// Consume verifies and invalidates a lease so artifact/result submission cannot replay.
func (a *LeaseAuthority) Consume(jobID, workerID, token string) error {
	candidate := sha256.Sum256([]byte(token))
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.records[jobID]
	if !ok || record.consumed || record.workerID != workerID || !a.now().Before(record.expiresAt) || subtle.ConstantTimeCompare(candidate[:], record.digest[:]) != 1 {
		return errors.New("remote: invalid, expired, or consumed workload lease")
	}
	record.consumed = true
	return nil
}

// VerifyArtifact streams a remote object through size and SHA-256 checks.
func VerifyArtifact(_ context.Context, source io.Reader, expectedContentID string, expectedSize, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultArtifactLimit
	}
	if expectedSize < 0 || expectedSize > maxBytes || len(expectedContentID) != len("sha256:")+sha256.Size*2 {
		return errors.New("remote: invalid artifact declaration")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(source, maxBytes+1))
	if err != nil {
		return err
	}
	if written != expectedSize || written > maxBytes {
		return fmt.Errorf("remote: artifact size mismatch: got %d want %d", written, expectedSize)
	}
	actual := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expectedContentID)) != 1 {
		return errors.New("remote: artifact content hash mismatch")
	}
	return nil
}

func randomToken(bytes int) string {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}
