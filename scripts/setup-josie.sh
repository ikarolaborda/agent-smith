#!/usr/bin/env bash
# One-time setup for the Josiefied-Qwen3.5-0.8B-gabliterated-v1 model.
#
# It builds the local Ollama model from configs/josie/Modelfile (pulling the
# community GGUF from Hugging Face), pulls the embedding model, builds the
# agent binary, and ingests every shipped RAG corpus so the model is grounded
# with the existing training data.
#
# Re-runnable: `ollama create` and the content-hashed ingest are idempotent.
#
# Overridable via env:
#   MODEL_TAG    flat Ollama tag to create        (default: josie-qwen3.5)
#   EMBED_MODEL  Ollama embedding model            (default: nomic-embed-text)
#   CONFIG       agent config path                 (default: configs/josie.yaml)
set -euo pipefail

cd "$(dirname "$0")/.."

MODEL_TAG="${MODEL_TAG:-josie-qwen3.5}"
EMBED_MODEL="${EMBED_MODEL:-nomic-embed-text}"
CONFIG="${CONFIG:-configs/josie.yaml}"
MODELFILE="configs/josie/Modelfile"
BIN="bin/agent"

# Collections shipped under docs/<name>; mirrors the Makefile `ingest` target.
COLLECTIONS=(laravel php nestjs tailwind-css architectural-patterns software-engineering native-php cs-fundamentals computer-networks go-lang cybersecurity)

log() { printf '\033[32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- Preflight ---------------------------------------------------------------
command -v ollama >/dev/null 2>&1 || die "ollama not found. Install it: https://ollama.com/download"
command -v go >/dev/null 2>&1 || die "go not found. Install Go 1.26+."
ollama list >/dev/null 2>&1 || die "ollama daemon not reachable. Start it with: ollama serve"
[ -f "$MODELFILE" ] || die "missing $MODELFILE (run from the repo, not a copy)"

case "$MODEL_TAG" in
  */*) die "MODEL_TAG must be a flat tag without '/': got '$MODEL_TAG' (the HTTP layer splits model ids on '/')" ;;
esac

# --- Build the model ---------------------------------------------------------
log "Creating Ollama model '$MODEL_TAG' from $MODELFILE (downloads GGUF on first run)..."
ollama create "$MODEL_TAG" -f "$MODELFILE"

log "Pulling embedding model '$EMBED_MODEL'..."
ollama pull "$EMBED_MODEL"

# --- Build the agent ---------------------------------------------------------
log "Building the agent binary..."
go build -o "$BIN" ./cmd/agent

# --- Ingest the RAG corpora --------------------------------------------------
for c in "${COLLECTIONS[@]}"; do
  log "Ingesting corpus '$c'..."
  "$BIN" --config "$CONFIG" --ingest \
    --collection "$c" --source "docs/$c" \
    --embedder ollama --embed-model "$EMBED_MODEL"
done

cat <<EOF

$(log "Done.")
Model:   $MODEL_TAG   (embeddings: $EMBED_MODEL)
Corpora: ${COLLECTIONS[*]}

Start the agent:
  $BIN --config $CONFIG --serve         # web UI at http://127.0.0.1:9090
  $BIN --config $CONFIG --prompt "..."  # single-shot CLI
EOF
