package store

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestArtifactEncryptionStreamsAuthenticatesAndHidesPlaintext(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "encrypted")
	key := bytes.Repeat([]byte{0x42}, 32)
	store, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20, ArtifactEncryptionKeys: [][]byte{key}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, campaign := seedCampaign(t, store)
	plaintext := bytes.Repeat([]byte("sensitive-evidence-"), 9000)
	artifact, err := store.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "crashing_input", MediaType: "application/octet-stream", Sensitivity: "embargoed"}, bytes.NewReader(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Encryption != artifactEncryptionScheme || artifact.EncryptionKeyID == "" || artifact.Size != int64(len(plaintext)) || !store.ArtifactEncryptionEnabled() {
		t.Fatalf("artifact=%#v", artifact)
	}
	rawPath := filepath.Join(root, "artifacts", filepath.FromSlash(artifact.StoragePath))
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, plaintext) || bytes.Contains(raw, []byte("sensitive-evidence")) || len(raw) <= len(plaintext) || !bytes.Equal(raw[:len(artifactMagic)], artifactMagic[:]) {
		t.Fatal("artifact was not stored in the authenticated encrypted format")
	}
	openedMetadata, reader, err := store.OpenArtifact(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(opened, plaintext) || openedMetadata.EncryptionKeyID != artifact.EncryptionKeyID {
		t.Fatalf("opened bytes=%d read=%v close=%v metadata=%#v", len(opened), readErr, closeErr, openedMetadata)
	}
	storedMetadata, err := store.GetArtifact(ctx, artifact.ID)
	if err != nil || storedMetadata.Encryption != artifactEncryptionScheme || storedMetadata.EncryptionKeyID != artifact.EncryptionKeyID {
		t.Fatalf("stored metadata=%#v err=%v", storedMetadata, err)
	}

	raw[artifactHeaderSize+artifactRecordHeaderSize+7] ^= 0xff
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, reader, err := store.OpenArtifact(ctx, artifact.ID); err == nil {
		reader.Close()
		t.Fatal("tampered encrypted artifact accepted")
	}
}

func TestArtifactEncryptionMigratesLegacyAndRotatesKeys(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "rotation")
	legacy, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, campaign := seedCampaign(t, legacy)
	artifact, err := legacy.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "log", MediaType: "text/plain", Sensitivity: "research"}, strings.NewReader("legacy evidence"))
	if err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(root, "artifacts", filepath.FromSlash(artifact.StoragePath))
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	oldKey := bytes.Repeat([]byte{0x11}, 32)
	migrated, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20, ArtifactEncryptionKeys: [][]byte{oldKey}})
	if err != nil {
		t.Fatal(err)
	}
	migratedRaw, _ := os.ReadFile(rawPath)
	if !bytes.Equal(migratedRaw[:len(artifactMagic)], artifactMagic[:]) || bytes.Contains(migratedRaw, []byte("legacy evidence")) {
		t.Fatal("legacy artifact was not encrypted during startup migration")
	}
	migratedMetadata, err := migrated.GetArtifact(ctx, artifact.ID)
	if err != nil || migratedMetadata.EncryptionKeyID == "" {
		t.Fatalf("migrated metadata=%#v err=%v", migratedMetadata, err)
	}
	oldKeyID := migratedMetadata.EncryptionKeyID
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "keys required") {
		t.Fatalf("encrypted custody opened without keys: %v", err)
	}
	wrongKey := bytes.Repeat([]byte{0x22}, 32)
	if _, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20, ArtifactEncryptionKeys: [][]byte{wrongKey}}); err == nil || !strings.Contains(err.Error(), "key unavailable") {
		t.Fatalf("encrypted custody opened with wrong key: %v", err)
	}

	newKey := bytes.Repeat([]byte{0x33}, 32)
	rotated, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20, ArtifactEncryptionKeys: [][]byte{newKey, oldKey}})
	if err != nil {
		t.Fatal(err)
	}
	rotatedMetadata, err := rotated.GetArtifact(ctx, artifact.ID)
	if err != nil || rotatedMetadata.EncryptionKeyID == oldKeyID || rotatedMetadata.EncryptionKeyID == "" {
		t.Fatalf("rotated metadata=%#v err=%v", rotatedMetadata, err)
	}
	_, reader, err := rotated.OpenArtifact(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(content) != "legacy evidence" {
		t.Fatalf("rotated content=%q err=%v", content, err)
	}
	if err := rotated.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, Config{Root: root, MaxArtifactBytes: 1 << 20, ArtifactEncryptionKeys: [][]byte{newKey}}); err != nil {
		t.Fatalf("rotated custody did not reopen with only active key: %v", err)
	} else {
		reopened.Close()
	}
}

func TestArtifactEncryptionHandlesEmptyObjectsAndKeyValidation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := Open(ctx, Config{Root: root, ArtifactEncryptionKeys: [][]byte{[]byte("short")}}); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("short key accepted: %v", err)
	}
	key := bytes.Repeat([]byte{0x77}, 32)
	store, err := Open(ctx, Config{Root: filepath.Join(root, "valid"), ArtifactEncryptionKeys: [][]byte{key}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, campaign := seedCampaign(t, store)
	artifact, err := store.PutArtifact(ctx, domain.Artifact{CampaignID: campaign.ID, Role: "empty", MediaType: "application/octet-stream"}, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := store.OpenArtifact(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || len(content) != 0 {
		t.Fatalf("empty content=%q err=%v", content, err)
	}
	if _, _, err := prepareArtifactKeys([][]byte{key, append([]byte(nil), key...)}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key accepted: %v", err)
	}
}
