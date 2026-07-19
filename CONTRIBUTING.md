# Contributing to agent-smith

Thanks for your interest. Small contributions are welcome — bug reports, doc fixes, new RAG corpora, and provider implementations are all on the table.

By contributing you agree that your work will be released under [GPL-3.0-or-later](./LICENSE) — the same licence as the rest of the project.

## Quick start for contributors

```sh
git clone git@github.com:ikarolaborda/agent-smith.git
cd agent-smith
make web-install        # one-time: install Node deps for the SPA
make build              # builds web/dist + bin/agent
make test               # go test -race -count=1 ./...
```

Requirements: Go 1.25+, Node 20+, and (optionally) a local Ollama install for the offline-first path.

## How to propose a change

1. Open an issue first if the change is non-trivial. This avoids two people doing the same work.
2. Fork the repo and create a topic branch — `ikaro/web-grounding-cache-ttl` is a fine shape.
3. Make the change. Keep commits focused — one concern per commit.
4. Run `make test` and `make lint`. CI runs the same checks.
5. Open a PR against `main`. Fill in the PR template. Link the issue.
6. A maintainer will review. Expect questions; expect to iterate.

## Commit style

[Conventional Commits](https://www.conventionalcommits.org/) prefixes are preferred but not enforced:

- `feat:` new user-visible capability
- `fix:` bug fix
- `refactor:` no behaviour change
- `docs:` documentation only
- `test:` test-only changes
- `chore:` build / tooling / housekeeping

Subject lines should be ≤ 72 chars, imperative mood (“add caching”, not “added caching”).

## Code style

### Go

- Targets the Go toolchain version pinned in `go.mod` (currently Go 1.25). The CI runs `go vet`, `go test -race -count=1 ./...`, and `golangci-lint run` (config in `.golangci.yml`).
- Block comments `/* ... */` are used throughout this codebase; please match the local convention (the only `//` lines you should add are the `//go:embed` directives, which Go itself requires).
- Add or update tests for any behaviour change. Race tests are mandatory.

### TypeScript / React

- Vite + React + TypeScript under `web/`. Run `cd web && npm run build` after editing.
- Keep markdown rendering safe — sanitiser pipelines exist for a reason; do not loosen them.

## Adding a RAG corpus

1. Drop your markdown under `docs/rag/<collection-name>/`.
2. Restart the agent — ingest is idempotent and content-hashed.
3. Test that retrieval surfaces the new content by asking a representative question.

## Adding an LLM provider

1. Create `internal/llm/<name>/` with a type that satisfies `internal/llm.Provider`.
2. Register the constructor in `cmd/agent/main.go::buildProvider`.
3. Add streaming tests modelled on the existing `internal/agent/stream_test.go`. Use a `blockingProvider` stub for cancellation tests — buffered channels are racy under `select`.

## Security disclosures

Please **do not** open a public issue for security problems. See [SECURITY.md](./SECURITY.md) for the responsible-disclosure path.

## Code of Conduct

Everyone interacting with the project is expected to follow the [Code of Conduct](./CODE_OF_CONDUCT.md).
