package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

func TestConfiguredLlamaDownloadUsesSafeOperationalDefaults(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{}}
	ref, downloader, err := configuredLlamaDownload(cfg, "owner/model-GGUF:Q5_K_M", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("configuredLlamaDownload: %v", err)
	}
	if ref.Repo != "owner/model-GGUF" || ref.Quant != "Q5_K_M" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
	if downloader.ContextTokens != 4096 || downloader.Parallel != 1 {
		t.Fatalf("unsafe defaults: context=%d parallel=%d", downloader.ContextTokens, downloader.Parallel)
	}
}

func TestConfiguredLlamaDownloadScopesSelectorsToRepository(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"llamacpp": {
			LlamaCpp: &config.LlamaCppConfig{
				Repo:       "trusted/model-GGUF",
				File:       "trusted-q4.gguf",
				MMProjFile: "mmproj-f16.gguf",
				Revision:   "release",
				CtxSize:    8192,
				Parallel:   2,
			},
		},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	matching, dl, err := configuredLlamaDownload(cfg, "trusted/model-GGUF", logger)
	if err != nil {
		t.Fatal(err)
	}
	if matching.File != "trusted-q4.gguf" || matching.MMProjFile != "mmproj-f16.gguf" || matching.Revision != "release" {
		t.Fatalf("configured selectors not applied: %#v", matching)
	}
	if dl.ContextTokens != 8192 || dl.Parallel != 2 {
		t.Fatalf("operational settings not applied: context=%d parallel=%d", dl.ContextTokens, dl.Parallel)
	}

	other, _, err := configuredLlamaDownload(cfg, "someone/other-GGUF", logger)
	if err != nil {
		t.Fatal(err)
	}
	if other.File != "" || other.MMProjFile != "" || other.Revision != "main" {
		t.Fatalf("selectors leaked to another repository: %#v", other)
	}

	explicit, _, err := configuredLlamaDownload(cfg, "trusted/model-GGUF:Q5_K_M", logger)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Quant != "Q5_K_M" || explicit.File != "" {
		t.Fatalf("explicit CLI quant was overridden by configured file: %#v", explicit)
	}
}

func TestConfigForServeAppliesProviderAndModelOverride(t *testing.T) {
	original := &config.Config{
		DefaultProvider: "ollama",
		Providers: map[string]config.ProviderConfig{
			"ollama":   {Model: "small"},
			"llamacpp": {Model: "local"},
		},
	}
	got, err := configForServe(original, flags{provider: "llamacpp", model: "local-override"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProvider != "llamacpp" || got.Providers["llamacpp"].Model != "local-override" {
		t.Fatalf("override not applied: %+v", got)
	}
	if original.DefaultProvider != "ollama" || original.Providers["llamacpp"].Model != "local" {
		t.Fatal("serve config mutated the loaded config")
	}
}

func TestBuildRAGFailsClosedWhenKnowledgeLayerCannotInitialize(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := buildRAG(&config.Config{Providers: map[string]config.ProviderConfig{}}, &flags{ragDir: filepath.Join(blocker, "rag")}, logger)
	if err == nil {
		t.Fatal("required knowledge layer failure must stop startup")
	}
}

func TestBuildRAGSelectsConfiguredMemoryEmbedder(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		"ollama": {BaseURL: "http://127.0.0.1:11434", Model: "chat"},
		"openai": {APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "chat"},
	}}
	f := flags{
		ragDir:     t.TempDir(),
		embedder:   "ollama",
		disableWeb: true,
		disableC7:  true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := buildRAG(cfg, &f, logger)
	if err != nil {
		t.Fatal(err)
	}
	if svc.MemoryEmbedderID != "ollama:nomic-embed-text" {
		t.Fatalf("memory embedder = %q", svc.MemoryEmbedderID)
	}
}

func TestBuildRAGDoesNotFallBackToRemoteMemoryEmbedder(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{
		/* If fallback occurs this unreachable endpoint produces an HTTP error, not the required pre-I/O selection error. */
		"openai": {APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "chat"},
	}}
	f := flags{
		ragDir:     t.TempDir(),
		embedder:   "ollama",
		disableWeb: true,
		disableC7:  true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := buildRAG(cfg, &f, logger)
	if err != nil {
		t.Fatal(err)
	}
	if svc.MemoryEmbedderID != "ollama:nomic-embed-text" {
		t.Fatalf("memory embedder = %q", svc.MemoryEmbedderID)
	}
	_, err = svc.Remember(context.Background(), rag.MemoryWrite{ProfileID: "p1", Text: "private project fact"})
	if err == nil || !strings.Contains(err.Error(), "configured memory embedder") {
		t.Fatalf("expected unavailable preferred embedder error, got %v", err)
	}
}

func TestIsLocalProviderIncludesAbliterationGroundingDefault(t *testing.T) {
	if !isLocalProvider("abliteration") {
		t.Fatal("CLI abliteration provider must inherit the server's default web-grounding posture")
	}
}

func TestLoadNoveltySourcesRejectsOversizedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.json")
	valid := `[{"name":"nvd","kind":"nvd","base_url":"https://example.test/search","query_param":"q"}]`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := loadNoveltySources(path)
	if err != nil || len(sources) != 1 || sources[0].Name != "nvd" {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	if err := os.WriteFile(path, []byte(valid+strings.Repeat(" ", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNoveltySources(path); err == nil {
		t.Fatal("oversized novelty source configuration was accepted")
	}
}

func TestLoadSourceBundleSourcesIsBoundedAndStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundles.json")
	valid := `[{"name":"mirror","repository":"repo","bundles":[{"commit":"0123456789abcdef0123456789abcdef01234567","url":"https://sources.example.test/source.tar","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	sources, err := loadSourceBundleSources(path)
	if err != nil || len(sources) != 1 || sources[0].Name != "mirror" || len(sources[0].Bundles) != 1 {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	unknown := strings.Replace(valid, `"repository":"repo"`, `"repository":"repo","credential":"secret"`, 1)
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourceBundleSources(path); err == nil {
		t.Fatal("unknown source-bundle configuration field accepted")
	}
	if err := os.WriteFile(path, []byte(valid+strings.Repeat(" ", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSourceBundleSources(path); err == nil {
		t.Fatal("oversized source-bundle configuration accepted")
	}
}

func TestSignAndLoadVerifiedSourceManifest(t *testing.T) {
	directory := t.TempDir()
	sourcesPath := filepath.Join(directory, "sources.json")
	privatePath := filepath.Join(directory, "private.pem")
	publicPath := filepath.Join(directory, "public.pem")
	signedPath := filepath.Join(directory, "signed.json")
	sources := `[{"name":"mirror","repository":"repo","bundles":[{"commit":"0123456789abcdef0123456789abcdef01234567","url":"https://sources.example.test/source.tar","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]`
	if err := os.WriteFile(sourcesPath, []byte(sources), 0o600); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, _ := x509.MarshalPKCS8PrivateKey(privateKey)
	publicDER, _ := x509.MarshalPKIXPublicKey(publicKey)
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runSignSourceBundles(flags{signResearchSourceBundles: sourcesPath, researchSourcePrivateKey: privatePath, researchSourceManifestLifetime: time.Hour}, &output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signedPath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := loadVerifiedSourceManifest(signedPath, publicPath, time.Now().UTC())
	if err != nil || verified.KeyID() == "" || len(verified.Sources()) != 1 {
		t.Fatalf("verified=%#v err=%v", verified, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	manifest := envelope["manifest"].(map[string]any)
	manifest["sources"].([]any)[0].(map[string]any)["repository"] = "tampered"
	tampered, _ := json.Marshal(envelope)
	if err := os.WriteFile(signedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedSourceManifest(signedPath, publicPath, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
}

func TestLoadArtifactEncryptionKeysIsOrderedStrictAndPrivate(t *testing.T) {
	directory := t.TempDir()
	activePath := filepath.Join(directory, "active.key")
	previousPath := filepath.Join(directory, "previous.key")
	if err := os.WriteFile(activePath, []byte(strings.Repeat("11", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousPath, []byte(strings.Repeat("22", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := loadArtifactEncryptionKeys(activePath + "," + previousPath)
	if err != nil || len(keys) != 2 || !bytes.Equal(keys[0], bytes.Repeat([]byte{0x11}, 32)) || !bytes.Equal(keys[1], bytes.Repeat([]byte{0x22}, 32)) {
		t.Fatalf("keys=%x err=%v", keys, err)
	}
	if err := os.WriteFile(activePath, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadArtifactEncryptionKeys(activePath); err == nil || !strings.Contains(err.Error(), "exactly 32") {
		t.Fatalf("invalid key accepted: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(activePath, []byte(strings.Repeat("33", 32)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(activePath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadArtifactEncryptionKeys(activePath); err == nil || !strings.Contains(err.Error(), "group or others") {
			t.Fatalf("broad key permissions accepted: %v", err)
		}
	}
}

func TestSignAndLoadVerifiedApparatusAdmissionCatalog(t *testing.T) {
	directory := t.TempDir()
	entriesPath := filepath.Join(directory, "entries.json")
	privatePath := filepath.Join(directory, "apparatus-private.pem")
	publicPath := filepath.Join(directory, "apparatus-public.pem")
	signedPath := filepath.Join(directory, "apparatus-signed.json")
	manifest := domain.ApparatusManifest{
		SchemaVersion: 1, ID: "apparatus", Name: "fixture", Version: "1", ImageDigest: "sha256:" + strings.Repeat("a", 64), Engine: "libfuzzer",
		Sanitizers: []string{"address"}, Architectures: []string{"amd64"}, Harnesses: []domain.HarnessManifest{{Name: "parser", Binary: "/build/fuzz_target"}}, Operations: []domain.Operation{domain.OperationFuzz},
		Limits: domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxCPUSeconds: 60, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1},
	}
	entries := []apparatus.AdmissionEntry{{
		Manifest:   manifest,
		SBOM:       json.RawMessage(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/cli-sbom","packages":[{"name":"fixture","SPDXID":"SPDXRef-Package","versionInfo":"1.0.0","checksums":[{"algorithm":"SHA256","checksumValue":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}]}`),
		Provenance: json.RawMessage(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"digest":{"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"buildType":"https://example.test/build/v1","resolvedDependencies":[{"uri":"pkg:docker/base@1","digest":{"sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}]},"runDetails":{"builder":{"id":"https://builder.example.test/research"}}}}`),
	}}
	entryData, _ := json.Marshal(entries)
	if err := os.WriteFile(entriesPath, entryData, 0o600); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	privateDER, _ := x509.MarshalPKCS8PrivateKey(privateKey)
	publicDER, _ := x509.MarshalPKIXPublicKey(publicKey)
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runSignApparatusCatalog(flags{signResearchApparatusCatalog: entriesPath, researchApparatusPrivateKey: privatePath, researchApparatusLifetime: time.Hour}, &output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signedPath, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := loadVerifiedApparatusCatalog(signedPath, publicPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := verified.Admit(manifest, time.Now().UTC())
	if err != nil || admitted.SupplyChain == nil || admitted.SupplyChain.DependencyCount != 1 {
		t.Fatalf("admitted=%#v err=%v", admitted, err)
	}
	var envelope map[string]any
	_ = json.Unmarshal(output.Bytes(), &envelope)
	envelope["catalog"].(map[string]any)["entries"].([]any)[0].(map[string]any)["sbom"].(map[string]any)["name"] = "tampered"
	tampered, _ := json.Marshal(envelope)
	if err := os.WriteFile(signedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedApparatusCatalog(signedPath, publicPath, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("tampered apparatus catalog accepted: %v", err)
	}
}

func TestVerifyResearchStoreCommandEmitsIntegrityEvidence(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	root := filepath.Join(directory, "restored-research")
	key := bytes.Repeat([]byte{0x6a}, 32)
	repository, err := store.Open(ctx, store.Config{Root: root, ArtifactEncryptionKeys: [][]byte{key}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendAudit(ctx, domain.AuditEvent{ActorID: "restore-test", Action: "backup.restored", ResourceType: "custody_store", ResourceID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(directory, "artifact.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runVerifyResearchStore(ctx, flags{verifyResearchStore: true, researchDir: root, researchArtifactKeys: keyPath, researchArtifactRetention: store.DefaultArtifactRetention}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var report store.IntegrityReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Database != "ok" || report.ForeignKeys != "ok" || report.AuditEvents != 1 || report.OrphanBlobs != 0 {
		t.Fatalf("integrity report=%#v", report)
	}
}
