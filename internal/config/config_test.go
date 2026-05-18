package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
)

const sampleYAML = `
default_provider: openai
providers:
  openai:
    api_key: ${OPENAI_API_KEY}
    base_url: https://api.openai.com/v1
    model: gpt-4o-mini
  ollama:
    base_url: http://localhost:11434
    model: llama3.1
agent:
  system_prompt: "you are helpful"
  max_iterations: 5
  temperature: 0.5
logging:
  level: info
  format: text
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoad_ExpandsEnvAndValidates(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	dir := t.TempDir()
	yamlPath := writeFile(t, dir, "config.yaml", sampleYAML)

	cfg, err := config.Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "openai" {
		t.Fatalf("default_provider: %q", cfg.DefaultProvider)
	}
	if cfg.Providers["openai"].APIKey != "sk-from-env" {
		t.Fatalf("openai.api_key not expanded: %q", cfg.Providers["openai"].APIKey)
	}
	if cfg.Agent.MaxIterations != 5 {
		t.Fatalf("max_iterations: %d", cfg.Agent.MaxIterations)
	}
}

func TestLoad_DotEnvFillsMissingVars(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "OPENAI_API_KEY=sk-from-dotenv\n")
	yamlPath := writeFile(t, dir, "config.yaml", sampleYAML)

	/*
		Make sure the variable is not already set so .env is the only source
		that can satisfy the placeholder.
	*/
	t.Setenv("OPENAI_API_KEY", "")
	os.Unsetenv("OPENAI_API_KEY")

	cfg, err := config.Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openai"].APIKey; got != "sk-from-dotenv" {
		t.Fatalf("expected .env value, got %q", got)
	}
}

func TestLoad_RealEnvBeatsDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".env", "OPENAI_API_KEY=from-dotenv\n")
	yamlPath := writeFile(t, dir, "config.yaml", sampleYAML)

	t.Setenv("OPENAI_API_KEY", "from-real-env")

	cfg, err := config.Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["openai"].APIKey; got != "from-real-env" {
		t.Fatalf("real env should win, got %q", got)
	}
}

func TestLoad_RejectsUnknownDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	bad := `
default_provider: not-real
providers:
  openai: {model: foo}
agent: {max_iterations: 1}
logging: {level: info, format: text}
`
	p := writeFile(t, dir, "bad.yaml", bad)
	if _, err := config.Load(p); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestApplyEnvOverrides_ExpandsProviders(t *testing.T) {
	t.Setenv("MODEL_NAME", "from-env")
	cfg := &config.Config{
		DefaultProvider: "openai",
		Providers: map[string]config.ProviderConfig{
			"openai": {Model: "${MODEL_NAME}"},
		},
	}
	if err := config.ApplyEnvOverrides(cfg); err != nil {
		t.Fatalf("ApplyEnvOverrides: %v", err)
	}
	if cfg.Providers["openai"].Model != "from-env" {
		t.Fatalf("not expanded: %q", cfg.Providers["openai"].Model)
	}
}
