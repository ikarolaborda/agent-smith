package sourcefetch

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"
)

const (
	MaxManifestLifetime = 90 * 24 * time.Hour
	manifestClockSkew   = 5 * time.Minute
	manifestDomain      = "agent-smith/source-bundle-manifest/v1"
)

// Manifest is the signed source policy payload.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Sources       []Source  `json:"sources"`
}

// SignedManifest is an Ed25519-authenticated source policy envelope.
type SignedManifest struct {
	SchemaVersion int      `json:"schema_version"`
	KeyID         string   `json:"key_id"`
	Manifest      Manifest `json:"manifest"`
	Signature     string   `json:"signature"`
}

// VerifiedManifest cannot be populated from decoded caller data. Server
// admission consumes only values returned by VerifyManifest.
type VerifiedManifest struct {
	keyID     string
	expiresAt time.Time
	sources   []Source
}

func (manifest *VerifiedManifest) ExpiresAt() time.Time {
	if manifest == nil {
		return time.Time{}
	}
	return manifest.expiresAt
}

func (manifest *VerifiedManifest) KeyID() string {
	if manifest == nil {
		return ""
	}
	return manifest.keyID
}

func (manifest *VerifiedManifest) Sources() []Source {
	if manifest == nil {
		return nil
	}
	return cloneSources(manifest.sources)
}

// KeyID derives the stable full SHA-256 identity of one Ed25519 public key.
func KeyID(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("sourcefetch: invalid Ed25519 public key")
	}
	digest := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// SignManifest produces the exact canonical envelope accepted by VerifyManifest.
func SignManifest(manifest Manifest, privateKey ed25519.PrivateKey) (SignedManifest, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return SignedManifest{}, errors.New("sourcefetch: invalid Ed25519 private key")
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return SignedManifest{}, errors.New("sourcefetch: private key has no Ed25519 public identity")
	}
	keyID, err := KeyID(publicKey)
	if err != nil {
		return SignedManifest{}, err
	}
	if err := validateManifest(manifest, manifest.IssuedAt); err != nil {
		return SignedManifest{}, err
	}
	envelope := SignedManifest{SchemaVersion: 1, KeyID: keyID, Manifest: manifest}
	payload, err := manifestSigningPayload(envelope)
	if err != nil {
		return SignedManifest{}, err
	}
	envelope.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return envelope, nil
}

// VerifyManifest authenticates identity, schema, validity, and all fixed source
// descriptors before returning an admission-only value.
func VerifyManifest(envelope SignedManifest, publicKey ed25519.PublicKey, now time.Time) (*VerifiedManifest, error) {
	keyID, err := KeyID(publicKey)
	if err != nil {
		return nil, err
	}
	if envelope.SchemaVersion != 1 || envelope.KeyID != keyID {
		return nil, errors.New("sourcefetch: manifest envelope identity mismatch")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := validateManifest(envelope.Manifest, now); err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature {
		return nil, errors.New("sourcefetch: invalid manifest signature encoding")
	}
	payload, err := manifestSigningPayload(envelope)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return nil, errors.New("sourcefetch: manifest signature mismatch")
	}
	return &VerifiedManifest{keyID: keyID, expiresAt: envelope.Manifest.ExpiresAt, sources: cloneSources(envelope.Manifest.Sources)}, nil
}

// ParsePublicKeyPEM accepts a single standard PKIX Ed25519 public-key block.
func ParsePublicKeyPEM(data []byte) (ed25519.PublicKey, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || strings.TrimSpace(string(trailing)) != "" {
		return nil, errors.New("sourcefetch: one PEM PUBLIC KEY block required")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("sourcefetch: invalid PKIX public key")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("sourcefetch: Ed25519 public key required")
	}
	return append(ed25519.PublicKey(nil), key...), nil
}

// ParsePrivateKeyPEM accepts a single standard PKCS#8 Ed25519 private-key block.
func ParsePrivateKeyPEM(data []byte) (ed25519.PrivateKey, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || strings.TrimSpace(string(trailing)) != "" {
		return nil, errors.New("sourcefetch: one PEM PRIVATE KEY block required")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("sourcefetch: invalid PKCS#8 private key")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("sourcefetch: Ed25519 private key required")
	}
	return append(ed25519.PrivateKey(nil), key...), nil
}

func validateManifest(manifest Manifest, now time.Time) error {
	if manifest.SchemaVersion != 1 || manifest.IssuedAt.IsZero() || manifest.ExpiresAt.IsZero() || len(manifest.Sources) == 0 || len(manifest.Sources) > 64 {
		return errors.New("sourcefetch: invalid manifest identity or source count")
	}
	lifetime := manifest.ExpiresAt.Sub(manifest.IssuedAt)
	if lifetime <= 0 || lifetime > MaxManifestLifetime || now.Before(manifest.IssuedAt.Add(-manifestClockSkew)) || !now.Before(manifest.ExpiresAt) {
		return errors.New("sourcefetch: manifest expired or outside time policy")
	}
	_, err := validateSources(manifest.Sources, "", time.Time{})
	return err
}

func manifestSigningPayload(envelope SignedManifest) ([]byte, error) {
	return json.Marshal(struct {
		Domain        string   `json:"domain"`
		SchemaVersion int      `json:"schema_version"`
		KeyID         string   `json:"key_id"`
		Manifest      Manifest `json:"manifest"`
	}{Domain: manifestDomain, SchemaVersion: envelope.SchemaVersion, KeyID: envelope.KeyID, Manifest: envelope.Manifest})
}

func cloneSources(sources []Source) []Source {
	result := make([]Source, len(sources))
	for index, source := range sources {
		result[index] = source
		result[index].Bundles = append([]Bundle(nil), source.Bundles...)
	}
	return result
}
