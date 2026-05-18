---
name: Bug report
about: Something is broken or behaving unexpectedly
title: "[bug] "
labels: ["bug"]
assignees: []
---

## What happened
<!-- One paragraph. What did you do, what did you see? -->

## What you expected
<!-- What should have happened instead? -->

## Reproduction
<!-- Minimum command, config snippet, or chat transcript that reproduces the issue. -->

```sh
# example
./bin/agent --serve
curl -N -X POST http://127.0.0.1:9090/v1/chat/completions ...
```

## Environment
- agent-smith version (or commit SHA):
- `go version`:
- Provider: openai / anthropic / ollama (model: ...)
- OS:
- Browser (if the bug is in the SPA):

## Logs / output
<!-- Paste relevant log lines or SSE frames. Use a code block. -->

## Additional context
<!-- Anything else useful — screenshots, config, related issues. -->
