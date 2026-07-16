package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/rag"
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
