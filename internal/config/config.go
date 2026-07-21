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
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	/*
		LlamaCpp is the optional configuration for the self-managed llama.cpp
		provider, which downloads GGUF weights and supervises a local
		llama-server subprocess. It is only consulted for a provider entry named
		"llamacpp" and is ignored (and may be nil) for every other provider.
	*/
	LlamaCpp *LlamaCppConfig `yaml:"llama_cpp"`
}

/*
LlamaCppConfig configures the self-managed llama.cpp provider. Exactly one model
source is required: ModelPath for an existing local .gguf, or Repo for a Hugging
Face reference that agent-smith downloads on its own. All other fields are
optional and fall back to safe defaults (loopback host, auto-selected port,
llama-server on PATH).
*/
type LlamaCppConfig struct {
	Binary                string   `yaml:"binary"`
	ModelsDir             string   `yaml:"models_dir"`
	Repo                  string   `yaml:"repo"`
	File                  string   `yaml:"file"`
	Quant                 string   `yaml:"quant"`
	Revision              string   `yaml:"revision"`
	MMProjFile            string   `yaml:"mmproj_file"`
	ModelPath             string   `yaml:"model_path"`
	MMProjPath            string   `yaml:"mmproj_path"`
	HFToken               string   `yaml:"hf_token"`
	Host                  string   `yaml:"host"`
	Port                  int      `yaml:"port"`
	CtxSize               int      `yaml:"ctx_size"`
	Parallel              int      `yaml:"parallel"`
	GPULayers             int      `yaml:"gpu_layers"`
	Jinja                 bool     `yaml:"jinja"`
	ExtraArgs             []string `yaml:"extra_args"`
	StartupTimeoutSeconds int      `yaml:"startup_timeout_seconds"`
	/*
		AutoPickModel substitutes the largest abliterated catalog model
		(code-optimized preferred) that fits this host when the configured Repo
		fails the memory fit gate, instead of refusing to start. Only applies to
		Repo-sourced models: an explicit ModelPath always fails strictly, because
		silently serving different weights than the file the operator pinned
		would violate least surprise.
	*/
	AutoPickModel bool `yaml:"auto_pick_model"`
}

/* AgentConfig controls the agent loop's high-level behavior. */
type AgentConfig struct {
	SystemPrompt  string  `yaml:"system_prompt"`
	MaxIterations int     `yaml:"max_iterations"`
	Temperature   float64 `yaml:"temperature"`
	/*
		Agentic enables agentic-RAG by default: the model plans and runs its own
		retrieval via the rag_search tool and self-evaluates, rather than the
		one-shot Augment. Best paired with a tool-capable reasoning provider
		(OpenAI/Anthropic). Per-request `agentic` overrides it; off by default so
		the offline-friendly classic path is unchanged.
	*/
	Agentic bool `yaml:"agentic"`
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
		Load .env from the config file's directory, then from the current
		working directory (project root). godotenv.Load does not overwrite
		variables that are already set, so the precedence is
		"real env > config-dir .env > project-root .env", and loading the same
		file twice (when the config sits at the root) is a harmless no-op. The
		root fallback is what lets a project-root .env — the conventional
		location — supply keys like CONTEXT7_API_KEY when --config points at
		configs/.
	*/
	_ = godotenv.Load(filepath.Join(filepath.Dir(path), ".env"))
	_ = godotenv.Load(".env")

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	expanded := expandEnvRefs(string(raw))

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
		if p.LlamaCpp != nil {
			p.LlamaCpp.Binary = os.ExpandEnv(p.LlamaCpp.Binary)
			p.LlamaCpp.HFToken = os.ExpandEnv(p.LlamaCpp.HFToken)
			p.LlamaCpp.File = os.ExpandEnv(p.LlamaCpp.File)
			p.LlamaCpp.MMProjFile = os.ExpandEnv(p.LlamaCpp.MMProjFile)
			p.LlamaCpp.ModelPath = os.ExpandEnv(p.LlamaCpp.ModelPath)
			p.LlamaCpp.MMProjPath = os.ExpandEnv(p.LlamaCpp.MMProjPath)
			p.LlamaCpp.ModelsDir = os.ExpandEnv(p.LlamaCpp.ModelsDir)
			p.LlamaCpp.Repo = os.ExpandEnv(p.LlamaCpp.Repo)
			p.LlamaCpp.Revision = os.ExpandEnv(p.LlamaCpp.Revision)
			for i := range p.LlamaCpp.ExtraArgs {
				p.LlamaCpp.ExtraArgs[i] = os.ExpandEnv(p.LlamaCpp.ExtraArgs[i])
			}
		}
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
	for name, provider := range cfg.Providers {
		if provider.LlamaCpp == nil {
			continue
		}
		if name != "llamacpp" {
			return fmt.Errorf("config: provider %q has llama_cpp settings; use the reserved provider name %q", name, "llamacpp")
		}
		if err := validateLlamaCpp(provider.LlamaCpp); err != nil {
			return err
		}
	}
	return nil
}

func validateLlamaCpp(cfg *LlamaCppConfig) error {
	hasRepo := strings.TrimSpace(cfg.Repo) != ""
	hasLocal := strings.TrimSpace(cfg.ModelPath) != ""
	if hasRepo == hasLocal {
		return errors.New("config: providers.llamacpp.llama_cpp requires exactly one of repo or model_path")
	}
	if hasRepo && cfg.MMProjPath != "" {
		return errors.New("config: providers.llamacpp.llama_cpp.mmproj_path is only valid with model_path; use mmproj_file for a repository")
	}
	if hasLocal && (cfg.File != "" || cfg.Quant != "" || cfg.Revision != "" || cfg.MMProjFile != "") {
		return errors.New("config: repository selectors file, quant, revision, and mmproj_file cannot be combined with model_path")
	}
	if cfg.Host != "" && !isLoopbackHost(cfg.Host) {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.host %q is not loopback; the supervised endpoint is intentionally local-only", cfg.Host)
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.port must be between 0 and 65535, got %d", cfg.Port)
	}
	if cfg.CtxSize < 0 {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.ctx_size must be non-negative, got %d", cfg.CtxSize)
	}
	if cfg.Parallel < 0 {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.parallel must be non-negative, got %d", cfg.Parallel)
	}
	if cfg.GPULayers < 0 {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.gpu_layers must be non-negative, got %d", cfg.GPULayers)
	}
	if cfg.StartupTimeoutSeconds < 0 {
		return fmt.Errorf("config: providers.llamacpp.llama_cpp.startup_timeout_seconds must be non-negative, got %d", cfg.StartupTimeoutSeconds)
	}
	return nil
}

/*
envRefRe matches a $$ escape or a shell-style variable reference whose name
starts with a letter or underscore. Requiring that start is what makes
expandEnvRefs leave sequences like "$100" (a price in a system prompt) intact —
os.ExpandEnv would have parsed "$1" as the positional variable and dropped it.
*/
var envRefRe = regexp.MustCompile(`\$\$|\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*`)

/*
expandEnvRefs expands ${VAR}/$VAR references (undefined ones resolve to empty,
matching the prior os.ExpandEnv behavior so optional keys like ${HF_TOKEN} still
blank cleanly) but leaves non-variable dollar text untouched and supports $$ as
a literal-dollar escape.
*/
func expandEnvRefs(s string) string {
	return envRefRe.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$$" {
			return "$"
		}
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(m, "$"), "{"), "}")
		return os.Getenv(name)
	})
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
