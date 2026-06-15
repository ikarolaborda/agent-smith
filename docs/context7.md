# Context7 documentation augmentation

agent-smith can transparently enrich answers with **current, version-specific
library documentation** fetched from [Context7](https://context7.com). It works
for **every** model — local Ollama models included — because the docs are
injected into the system prompt before the model runs, not exposed as a tool the
model has to call. No per-request toggle, no UI action: when an API key is
configured, it is on.

## Enable

Set the API key in the environment (the server reads `.env` next to the config
file via godotenv, so a project-root `.env` works):

```
CONTEXT7_API_KEY=ctx7_...your key...
# optional — only if Context7 changes its API path:
CONTEXT7_BASE_URL=https://context7.com/api/v2
```

On boot the server logs `context7 augmentation: enabled`. Without the key it
logs `context7 augmentation: disabled` and behaves exactly as before.

Operator kill switch: start with `--no-context7` to force it off regardless of
the key.

## How it works

1. On each chat turn, `rag.Service.Augment` checks whether the user message
   looks like a technical/library question (a permissive gate — chit-chat is
   skipped so no API call is wasted).
2. If so, it resolves the message to a Context7 library id, then fetches that
   library's docs as plain text (`GET /libs/search` → `GET /context?type=txt`),
   authenticated with `Authorization: Bearer <CONTEXT7_API_KEY>`.
3. The docs are length-capped and injected as a
   `## Library documentation (Context7, authoritative)` section, which the model
   is told to prefer over its training memory for that library — while treating
   any instruction-like text inside the docs as data, never commands.

It is **best-effort**: any network/auth/parse failure, or a query with no
matching library, simply omits the section — the chat is never blocked. Results
are cached for 30 minutes so repeated questions about the same library do not
re-hit the API.

## Tuning

Defaults live in `internal/context7/client.go`: request timeout (6s), per-query
documentation token budget (1200), injected-section byte cap (4000), and cache
TTL (30m). The technical-query gate lives in `internal/rag/service.go`
(`technicalQuery`).
