# langgraph-spike — proving agent-smith → langchaingo → langgraphgo

A throwaway prototype answering one question from [ADR 0002](../../docs/adr/0002-langgraph-langsmith-abliteration-assessment.md):
**can agent-smith's own `llm.Provider` drive a `smallnest/langgraphgo` graph through a `tmc/langchaingo` `llms.Model` adapter?** Yes.

## Why this is a separate Go module

This directory has its **own `go.mod`** on purpose. Go excludes nested modules
from the parent's `./...`, so `langchaingo` + `langgraphgo` and their dependency
trees **never enter agent-smith's single-binary build or CI**. That keeps the
product's offline, single-binary thesis intact while we evaluate the library.
(`go.mod` carries no comment because `go.mod` only supports `//` comments and
this project forbids them in code; the rationale lives here instead.)

## What it shows

- `adapter.go` — `ProviderModel` implements `llms.Model` by mapping
  `[]llms.MessageContent` ⇄ agent-smith messages and calling `Provider.Chat`.
  This is the portable artifact: in-tree it wraps the real `internal/llm.Provider`
  unchanged (the types in `provider.go` mirror it field-for-field).
- `ollama_provider.go` — a minimal real `Provider` over Ollama `/api/chat`, so
  the adapter is driven by an actual local model, not a stub.
- `graph.go` — a two-node `langgraphgo` StateGraph (`answer → ground_check → END`)
  that drafts an answer then appends an explicit confidence/caveat line: the
  smallest version of the self-correction loop the security-research workflow wants.

## Run it

```sh
cd experiments/langgraph-spike
go test ./...                 # hermetic: fake provider, no network/model
SPIKE_MODEL=qwen3-coder:latest go run .   # live: against a local Ollama model
```

The hermetic test is the durable proof (compiles + runs with no external state);
`go run .` is the live end-to-end demo against Ollama.

## Status

Prototype only. Per ADR 0002 this stays an **optional/reference** path — adopting
it in the core would pull `langchaingo` in as a dependency. Not wired into the
agent-smith binary.
