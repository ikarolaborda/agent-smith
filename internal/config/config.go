/*
Package config defines the application configuration schema and the Load
entrypoint that reads a YAML file, performs environment-variable expansion,
and returns a validated Config struct.

Load also pulls a project-root .env file into the process environment via
godotenv before expansion, so YAML files can reference variables that live
only in .env without leaking them onto the developer's shell.
*/
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

/*
ProviderConfig is the per-provider configuration block. APIKey is optional
for providers that do not require authentication (e.g. local Ollama).
*/
type ProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

/* AgentConfig controls the agent loop's high-level behavior. */
type AgentConfig struct {
	SystemPrompt  string  `yaml:"system_prompt"`
	MaxIterations int     `yaml:"max_iterations"`
	Temperature   float64 `yaml:"temperature"`
}

/* LoggingConfig controls the slog handler chosen at startup. */
type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

/*
Config is the top-level configuration shape. Providers is keyed by provider
name ("openai", "anthropic", "ollama"); DefaultProvider names which entry to
use when the CLI does not override it.
*/
type Config struct {
	DefaultProvider string                    `yaml:"default_provider"`
	Providers       map[string]ProviderConfig `yaml:"providers"`
	Agent           AgentConfig               `yaml:"agent"`
	Logging         LoggingConfig             `yaml:"logging"`
}

/*
Load reads the YAML config at path, after first sourcing a .env file from
the same directory (best-effort: missing .env is not an error). All
${VAR} / $VAR placeholders inside the YAML are expanded against the process
environment before unmarshalling, so .env values are visible to the
expander. Real environment variables take precedence over .env values.
*/
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config: path is empty")
	}

	/*
		Load .env from the config file's directory. godotenv.Load does not
		overwrite variables that are already set, which preserves the rule
		"real env > .env".
	*/
	_ = godotenv.Load(filepath.Join(filepath.Dir(path), ".env"))

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

/*
ApplyEnvOverrides re-runs ${VAR} expansion against an in-memory Config. It
exists for the case where callers construct a Config programmatically and
want to resolve placeholders without round-tripping through YAML.
*/
func ApplyEnvOverrides(cfg *Config) error {
	if cfg == nil {
		return errors.New("config: nil config")
	}
	for name, p := range cfg.Providers {
		p.APIKey = os.ExpandEnv(p.APIKey)
		p.BaseURL = os.ExpandEnv(p.BaseURL)
		p.Model = os.ExpandEnv(p.Model)
		cfg.Providers[name] = p
	}
	return nil
}

/*
validate enforces the small set of invariants the rest of the codebase
relies on: a default provider must exist and be one of the configured
providers, and max_iterations must be non-negative.
*/
func validate(cfg *Config) error {
	if cfg.DefaultProvider == "" {
		return errors.New("config: default_provider is required")
	}
	if _, ok := cfg.Providers[cfg.DefaultProvider]; !ok {
		return fmt.Errorf("config: default_provider %q not present in providers map", cfg.DefaultProvider)
	}
	if cfg.Agent.MaxIterations < 0 {
		return fmt.Errorf("config: agent.max_iterations must be non-negative, got %d", cfg.Agent.MaxIterations)
	}
	return nil
}
