# Security policy

## Supported versions

Until a `v1.0.0` release, only the latest tagged minor receives security fixes. After `v1.0.0`, the latest two minors will be supported.

| Version  | Supported          |
| -------- | ------------------ |
| `0.1.x`  | :white_check_mark: |

## Reporting a vulnerability

Please report security issues **privately**, not through public issues.

- Preferred: open a [GitHub security advisory](https://github.com/ikarolaborda/agent-smith/security/advisories/new) in this repository.
- Alternative: email **iclaborda@msn.com** with the subject `agent-smith security:` followed by a short title.

Expect an acknowledgement within 72 hours. A fix and coordinated disclosure timeline will follow as quickly as the issue allows.

### What to include

- A short description of the issue and the version / commit you observed it on.
- The smallest reproducer you have (a curl call, a config snippet, a chat transcript).
- Your assessment of the impact — local-only, network-exposed, data-exfiltration, etc.

### What is in scope

- Memory leaks across profile boundaries (e.g. a write under `profile A` becoming retrievable under `profile B`).
- Prompt-injection paths that escape the quoted "third-party, untrusted" Augment sections (RAG / memory / web grounding).
- The `file_read` tool reading paths outside its configured roots.
- The web-grounding sanitiser failing to strip URL substrings or HTML tags.
- SSE framing bugs that allow injection of fake `tool_result` events.
- Credentials leaking via logs, error messages, or shell echo.

### What is out of scope

- Anything that requires the operator to deliberately mis-configure the agent (e.g. setting `--config` to a malicious YAML).
- Vulnerabilities in upstream providers (OpenAI / Anthropic / Ollama); report those upstream.
