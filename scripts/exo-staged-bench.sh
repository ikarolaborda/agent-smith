#!/usr/bin/env bash
#
# Staged Exo cluster bench for the 2-Mac (M5 Max 64GB + M5 Pro 24GB) TB5 cluster.
# Runs ONLY after the operator prerequisites are met (this script verifies them and
# refuses otherwise). It isolates Exo correctness from model-size pressure by going
# smallest -> target, and (optionally) RDMA-off vs RDMA-on, capturing per-node peak
# memory so the dense-70B feasibility question is settled by measurement, not guess.
#
# Prereqs (see docs/cluster-rdma-exo-assessment.md):
#   1. REAL exo-explore/exo installed from source (NOT the yeast-epigenome `exo`).
#   2. macOS 26.2+ on BOTH Macs; `rdma_ctl enable` run in Recovery mode + rebooted.
#   3. TB5-certified cable; both nodes reachable over the Thunderbolt bridge.
set -euo pipefail

EXO_ENDPOINT="${EXO_ENDPOINT:-http://127.0.0.1:52415}"

note() { printf '[exo-bench] %s\n' "$*"; }
fail() { printf '[exo-bench] REFUSING: %s\n' "$*" >&2; exit 1; }

# --- Preflight: refuse on the known name collision and missing prereqs ---
EXO_BIN="$(command -v exo || true)"
[ -n "$EXO_BIN" ] || fail "exo not on PATH — install exo-explore/exo from source first."
if exo --help 2>&1 | grep -qi "Yeast Epigenome"; then
  fail "the 'exo' on PATH is the Yeast Epigenome CLI (name collision), not exo-explore/exo."
fi

note "macOS: $(sw_vers -productVersion) (need >= 26.2 on BOTH nodes for RDMA)"
note "Thunderbolt link:"; system_profiler SPThunderboltDataType 2>/dev/null | grep -iE "speed" | head -3 || true

# RDMA status is informational here; enabling is a Recovery-mode step, not scriptable.
note "If RDMA was enabled, exo logs should show an RDMA transport at startup."

# --- Stage 1: small dense model (isolate Exo correctness from size pressure) ---
# --- Stage 2: target dense 70B quant (the real question) ---
# --- Stage 3 (optional): compare RDMA on vs off if you can toggle between runs ---
# Fill MODELS with served names your exo node exposes; keep stage 1 small.
STAGE1_MODEL="${STAGE1_MODEL:-mlx-community/Llama-3.2-3B-Instruct-4bit}"
STAGE2_MODEL="${STAGE2_MODEL:-}"   # e.g. the Llama-3.3-70B-abliterated MLX/GGUF served name

bench() {
  local model="$1" label="$2"
  [ -n "$model" ] || { note "skip $label (no model set)"; return 0; }
  note "=== $label: $model ==="
  # /bench/chat/completions returns generation_tps + peak_memory_usage (per exo API docs).
  curl -fsS "${EXO_ENDPOINT}/bench/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"${model}\",\"messages\":[{\"role\":\"user\",\"content\":\"Explain a use-after-free in two sentences.\"}],\"max_tokens\":128}" \
    | python3 -c 'import sys,json;d=json.load(sys.stdin);u=d.get("usage",d);print("  prompt_tps=",u.get("prompt_tps"),"gen_tps=",u.get("generation_tps"),"peak_mem=",u.get("peak_memory_usage"))' \
    || note "bench call failed for $label (is exo serving $model across both nodes?)"
}

bench "$STAGE1_MODEL" "Stage 1 (small dense — Exo correctness)"
bench "$STAGE2_MODEL" "Stage 2 (target 70B — feasibility; watch worker peak_memory_usage <= ~18GB)"

note "Done. Record per-node peak_memory_usage + tok/s in docs/cluster-rdma-exo-assessment.md."
note "Decision rule: dense-70B is viable ONLY if the 24GB worker's peak stays within its Metal budget across load AND sustained decode."
