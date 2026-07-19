package sourcefetch

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestSignedManifestAuthenticatesIdentityPolicyAndDefensiveCopies(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	manifest := testManifest(now)
	envelope, err := SignManifest(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyManifest(envelope, publicKey, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expectedKeyID, _ := KeyID(publicKey)
	if verified.KeyID() != expectedKeyID || len(verified.Sources()) != 1 || verified.Sources()[0].Bundles[0].Commit != testCommit {
		t.Fatalf("verified key=%q sources=%#v", verified.KeyID(), verified.Sources())
	}
	copy := verified.Sources()
	copy[0].Repository = "tampered"
	copy[0].Bundles[0].URL = "https://evil.example/source.tar"
	if verified.Sources()[0].Repository == "tampered" || verified.Sources()[0].Bundles[0].URL == copy[0].Bundles[0].URL {
		t.Fatal("verified manifest returned mutable internal state")
	}
}

func TestSignedManifestRejectsTamperingWrongKeysAndTimeReplay(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	otherPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	envelope, err := SignManifest(testManifest(now), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("tamper", func(t *testing.T) {
		candidate := envelope
		candidate.Manifest.Sources = cloneSources(envelope.Manifest.Sources)
		candidate.Manifest.Sources[0].Repository = "https://evil.example/project.git"
		if _, err := VerifyManifest(candidate, publicKey, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
			t.Fatalf("tampered manifest accepted: %v", err)
		}
	})
	t.Run("wrong key", func(t *testing.T) {
		if _, err := VerifyManifest(envelope, otherPublic, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("wrong key accepted: %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		if _, err := VerifyManifest(envelope, publicKey, envelope.Manifest.ExpiresAt); err == nil || !strings.Contains(err.Error(), "time policy") {
			t.Fatalf("expired manifest accepted: %v", err)
		}
	})
	t.Run("future", func(t *testing.T) {
		if _, err := VerifyManifest(envelope, publicKey, envelope.Manifest.IssuedAt.Add(-manifestClockSkew-time.Second)); err == nil || !strings.Contains(err.Error(), "time policy") {
			t.Fatalf("future manifest accepted: %v", err)
		}
	})
	t.Run("version", func(t *testing.T) {
		candidate := envelope
		candidate.SchemaVersion = 2
		if _, err := VerifyManifest(candidate, publicKey, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("unknown envelope version accepted: %v", err)
		}
	})
	t.Run("noncanonical signature", func(t *testing.T) {
		candidate := envelope
		candidate.Signature += "="
		if _, err := VerifyManifest(candidate, publicKey, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "signature encoding") {
			t.Fatalf("noncanonical signature accepted: %v", err)
		}
	})
}

func TestManifestLifetimeAndSourceValidationFailClosed(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC()
	manifest := testManifest(now)
	manifest.ExpiresAt = manifest.IssuedAt.Add(MaxManifestLifetime + time.Second)
	if _, err := SignManifest(manifest, privateKey); err == nil || !strings.Contains(err.Error(), "time policy") {
		t.Fatalf("overlong manifest accepted: %v", err)
	}
	manifest = testManifest(now)
	manifest.Sources[0].Bundles[0].URL = "http://private.example/source.tar"
	if _, err := SignManifest(manifest, privateKey); err == nil || !strings.Contains(err.Error(), "invalid pinned bundle") {
		t.Fatalf("unsafe signed source accepted: %v", err)
	}
}

func TestManifestPEMKeyParsingIsStrictAndTyped(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	parsedPublic, err := ParsePublicKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	if err != nil || !publicKey.Equal(parsedPublic) {
		t.Fatalf("public parse err=%v", err)
	}
	parsedPrivate, err := ParsePrivateKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))
	if err != nil || !privateKey.Equal(parsedPrivate) {
		t.Fatalf("private parse err=%v", err)
	}
	if _, err := ParsePublicKeyPEM(append(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), []byte("trailing")...)); err == nil {
		t.Fatal("trailing public-key data accepted")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaDER, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if _, err := ParsePublicKeyPEM(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: rsaDER})); err == nil || !strings.Contains(err.Error(), "Ed25519") {
		t.Fatalf("RSA public key accepted: %v", err)
	}
}

func testManifest(now time.Time) Manifest {
	return Manifest{SchemaVersion: 1, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour), Sources: []Source{{Name: "upstream", Repository: "https://example.test/project.git", Bundles: []Bundle{{Commit: testCommit, URL: "https://sources.example.test/project/source.tar", SHA256: "sha256:" + strings.Repeat("a", 64)}}}}}
}
