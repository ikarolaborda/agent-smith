#!/usr/bin/env bash
# Local dev helper. Runs the agent binary with the example config.
set -euo pipefail

cd "$(dirname "$0")/.."

CONFIG="${CONFIG:-configs/config.example.yaml}"
PROMPT="${PROMPT:-}"

go run ./cmd/agent --config "${CONFIG}" ${PROMPT:+--prompt "${PROMPT}"} "$@"
