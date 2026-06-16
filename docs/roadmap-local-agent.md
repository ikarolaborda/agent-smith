# Roadmap — Ultimate local ChatGPT/Claude agentic experience

Goal: a fully-local agent that you can use to **work on a project from the web UI**
*and* from a **terminal interface that shares the same brain** (Claude-Code-like),
with the same models, tools, memory, and grounding everywhere.

This is a multi-phase program. Each phase is independently shippable. Tech picks
below were validated against current docs (Context7) and an architecture review
(buddy, task `01KV87GJBVYAJSRAPFMGHDHS3H`, accepted, high confidence).

## Where we are today
- One Go binary: multi-provider chat (OpenAI/Anthropic/Ollama), OpenAI-compatible
  SSE server, embedded React/Vite chat SPA, RAG over curated corpora, per-profile
  long-term memory, web grounding, Context7 augmentation, **cluster inference**.
- Agent loop (`agent.Run`): plan → tool → observe, provider-agnostic; RAG/memory/
  augmentation wrap any provider (web + CLI inherit it).
- Tools shared via `tools.Registry`: `shell`, `http`, `file_read`, **and now
  `file_write` / `file_edit`** (Phase 1, this change).

## Phase 1 — Agentic mutation (DONE)
The blocking gap: the agent could read but not **modify** a project. Added
workspace-sandboxed mutation tools, registered for **both** web and CLI:
- `file_write` — create/overwrite a UTF-8 file (atomic temp+rename, mode-preserving).
- `file_edit` — exact single-match `old_string`→`new_string` (Claude Code Edit semantics).
- Security: opt-in `--workspace <dir>` (default OFF = read-only); sandbox root;
  traversal + symlinked-ancestor + write-over-symlink rejection; non-regular-file
  rejection; size caps; **no auto-exec, no arbitrary parent creation**.
- Interfaces are shaped so a permission/approval layer can wrap mutating calls
  later without changing tool semantics.

Run it: `./bin/agent --serve --workspace /path/to/project` (web) or
`./bin/agent --workspace /path/to/project` (CLI).

## Phase 2 — Unify the terminal as an API client (NEXT, highest value)
Today the CLI runs the agent **in-process**; the web runs over HTTP+SSE. They are
two execution paths. Make the terminal a **thin client of the same server**:
- Terminal creates/attaches a session over HTTP and consumes the **same**
  `/v1/chat/completions` SSE stream the web UI uses (tokens + `tool_result` events).
- Result: terminal and web share one brain, one session protocol, one tool set,
  one approval model — *identical by construction*, not by duplication.
- Likely server additions: resumable/streamed sessions, an interrupt endpoint,
  and slash-command actions recognized server-side.

## Phase 3 — Claude-Code-like TUI
Replace the line REPL with a real terminal UI. **Recommended stack (Context7-confirmed):**
- `charmbracelet/bubbletea` (Model-View-Update) + `charmbracelet/bubbles`
  (`viewport` for the transcript, `textarea` for input, `spinner`, `help`) +
  `charmbracelet/lipgloss` for styling; `WithAltScreen()` for full-screen.
- Stream tokens via a `tea.Cmd` that reads the Phase-2 SSE channel and emits
  msgs into the MVU loop. The TUI is a **pure client** over the Phase-2 API — not
  a second execution path.

## Phase 4 — Persistence & projects
Sessions are in-memory only today. Add a small embedded store. **Recommended: SQLite**
(robust, queryable, easy migrations) modeling `projects`, `sessions`, `messages`,
`tool_calls`, `artifacts`; each session optionally bound to a workspace root.
Large artifacts on disk with metadata rows. Persist enough event data to replay
both web and terminal transcripts consistently. Add the **project workspace UI**
(file tree + diff view of agent edits) to the web SPA.

## Cross-cutting — Permissions & approvals
Before/around Phase 2: a permission model wrapping mutating tools (allow/deny/ask,
per-tool and per-path), surfaced identically in web and terminal. This is the
safety backbone for autonomous project work; shape it as a thin wrapper over the
existing `tools.Tool.Execute` so no tool needs to change.

## Sequencing rationale
1 (mutation) unblocks everything — without it "work on a project" is impossible.
2 (terminal-as-client) is the real "identical terminal" win and must precede 3,
since a TUI should be a client, not a third brain. 4 (persistence) is foundational
but premature before there is real multi-surface state worth persisting.
