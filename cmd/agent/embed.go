package main

import (
	"errors"
	"fmt"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/*
buildEmbedders constructs every embedder for which the relevant provider is
configured (and credentialed). An empty map still permits the required embedded
lexical corpus; dense retrieval and writable memory remain unavailable.
*/
func buildEmbedders(cfg *config.Config, f flags) (map[string]llm.Embedder, error) {
	out := map[string]llm.Embedder{}
	for name := range cfg.Providers {
		f2 := f
		f2.embedder = name
		e, err := buildSingleEmbedder(cfg, f2)
		if err != nil {
			continue
		}
		out[e.Identity()] = e
	}
	if len(out) == 0 {
		return out, errors.New("no embedders could be built from configured providers")
	}
	return out, nil
}

/* buildSingleEmbedder constructs one embedder by name (openai or ollama). */
func buildSingleEmbedder(cfg *config.Config, f flags) (llm.Embedder, error) {
	name := f.embedder
	if name == "" {
		name = "ollama"
	}
	pcfg, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q has no config block", name)
	}
	switch name {
	case "openai":
		if pcfg.APIKey == "" {
			return nil, fmt.Errorf("openai embed: missing api_key")
		}
		c, err := openai.New(openai.Config{APIKey: pcfg.APIKey, BaseURL: pcfg.BaseURL, Model: pcfg.Model})
		if err != nil {
			return nil, err
		}
		return openai.NewEmbedder(c, openai.EmbedConfig{Model: f.embedModel})
	case "ollama":
		c, err := ollama.New(ollama.Config{BaseURL: pcfg.BaseURL, Model: pcfg.Model})
		if err != nil {
			return nil, err
		}
		return ollama.NewEmbedder(c, ollama.EmbedConfig{Model: f.embedModel})
	default:
		return nil, fmt.Errorf("unknown embedder provider %q", name)
	}
}

/* requestedMemoryEmbedderID resolves --embedder/--embed-model without contacting a backend. */
func requestedMemoryEmbedderID(f flags) (string, error) {
	name := f.embedder
	if name == "" {
		name = "ollama"
	}
	model := f.embedModel
	switch name {
	case "openai":
		if model == "" {
			model = "text-embedding-3-small"
		}
	case "ollama":
		if model == "" {
			model = "nomic-embed-text"
		}
	default:
		return "", fmt.Errorf("unknown memory embedder provider %q", name)
	}
	return name + ":" + model, nil
}
