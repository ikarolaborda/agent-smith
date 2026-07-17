#!/usr/bin/env bash
#
# run-local.sh — build and run agent-smith locally, optimized for this machine.
#
# It performs a FULL production build (frontend + Go binary with the SPA embedded
# via go:embed) tuned to the host, then serves the app. Leaving gpu_layers and
# ctx_size unset in the config lets the app auto-tune the local model to the
# detected hardware at launch.
#
# Usage:
#   scripts/run-local.sh [options] [-- extra agent args]
#
# Options:
#   --config PATH     Config file (default: configs/config.example.yaml)
#   --addr HOST:PORT  Bind address (default: 127.0.0.1:9090)
#   --port PORT       Shortcut to set the port on 127.0.0.1
#   --build-only      Build everything but do not start the server
#   --skip-frontend   Reuse the existing web/dist instead of rebuilding it
#   -h, --help        Show this help
#
# Environment overrides:
#   GOAMD64=v1|v2|v3|v4   Force the amd64 microarchitecture level (else auto)
#   NPM, GO               Override the npm/go binaries
#
set -euo pipefail

# This script uses bash arrays and bash-only parameter expansion. Running it
# under /bin/sh (dash on Debian/Mint) fails in confusing ways, so refuse early.
if [ -z "${BASH_VERSION:-}" ]; then
  echo "error: run this with bash, not sh (e.g. 'bash scripts/run-local.sh' or './scripts/run-local.sh')" >&2
  exit 1
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

GO="${GO:-go}"
NPM="${NPM:-npm}"
CONFIG="configs/config.example.yaml"
ADDR="127.0.0.1:9090"
BUILD_ONLY=0
SKIP_FRONTEND=0
EXTRA_ARGS=()

c_bold=$'\033[1m'; c_green=$'\033[32m'; c_yellow=$'\033[33m'; c_red=$'\033[31m'; c_dim=$'\033[2m'; c_reset=$'\033[0m'
info()  { printf '%s==>%s %s\n' "${c_green}" "${c_reset}" "$*"; }
warn()  { printf '%swarn:%s %s\n' "${c_yellow}" "${c_reset}" "$*" >&2; }
die()   { printf '%serror:%s %s\n' "${c_red}" "${c_reset}" "$*" >&2; exit 1; }

usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

while [ $# -gt 0 ]; do
  case "$1" in
    --config)       CONFIG="${2:?--config needs a path}"; shift 2 ;;
    --addr)         ADDR="${2:?--addr needs HOST:PORT}"; shift 2 ;;
    --port)         ADDR="127.0.0.1:${2:?--port needs a number}"; shift 2 ;;
    --build-only)   BUILD_ONLY=1; shift ;;
    --skip-frontend) SKIP_FRONTEND=1; shift ;;
    -h|--help)      usage ;;
    --)             shift; EXTRA_ARGS=("$@"); break ;;
    *)              die "unknown option: $1 (use --help)" ;;
  esac
done

# ---------------------------------------------------------------------------
# 1. Prerequisites
# ---------------------------------------------------------------------------
command -v "$GO" >/dev/null 2>&1 || die "Go toolchain not found (install from https://go.dev/dl or your package manager)"
if [ "$SKIP_FRONTEND" -eq 0 ]; then
  command -v node >/dev/null 2>&1 || die "node not found (install Node.js 20+); or pass --skip-frontend to reuse web/dist"
  command -v "$NPM" >/dev/null 2>&1 || die "npm not found; or pass --skip-frontend to reuse web/dist"
fi

# ---------------------------------------------------------------------------
# 2. Detect the host (advisory: shown to the user; the app does its own
#    detection at launch and auto-tunes the local model to it).
# ---------------------------------------------------------------------------
OS="$(uname -s)"; ARCH="$(uname -m)"
CORES=1; RAM_GB="?"; GPU="none detected (CPU inference)"
case "$OS" in
  Darwin)
    CORES="$(sysctl -n hw.ncpu 2>/dev/null || echo 1)"
    RAM_GB="$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1024 / 1024 / 1024 ))"
    gpu_name="$(system_profiler SPDisplaysDataType 2>/dev/null | awk -F': ' '/Chipset Model/{print $2; exit}')"
    GPU="${gpu_name:-Apple GPU} (Metal, unified memory)"
    ;;
  Linux)
    CORES="$(nproc 2>/dev/null || echo 1)"
    if [ -r /proc/meminfo ]; then
      RAM_GB="$(( $(awk '/MemTotal/{print $2}' /proc/meminfo) / 1024 / 1024 ))"
    fi
    if command -v nvidia-smi >/dev/null 2>&1; then
      GPU="$(nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>/dev/null | head -1) (CUDA)"
    elif command -v rocm-smi >/dev/null 2>&1; then
      GPU="$(rocm-smi --showproductname 2>/dev/null | awk -F': ' '/Card (Series|Model)/{print $NF; exit}') (ROCm)"
    elif command -v vulkaninfo >/dev/null 2>&1; then
      GPU="Vulkan-capable GPU detected"
    fi
    ;;
esac

printf '\n%sagent-smith · build + run%s\n' "${c_bold}" "${c_reset}"
printf '  host   : %s/%s · %s cores · %s GB RAM\n' "$OS" "$ARCH" "$CORES" "$RAM_GB"
printf '  gpu    : %s\n' "$GPU"
printf '  config : %s\n' "$CONFIG"
printf '  serve  : http://%s\n\n' "$ADDR"

# ---------------------------------------------------------------------------
# 3. Full frontend build (before the Go build, so go:embed captures a
#    consistent dist — a partial/stale dist blanks the SPA at runtime).
# ---------------------------------------------------------------------------
if [ "$SKIP_FRONTEND" -eq 0 ]; then
  info "Building the web UI (npm ci + production build)…"
  # Remove the entire prior dist so no stale artifact of any shape (hashed
  # assets, manifests, auxiliary chunks, a leftover index.html) can survive into
  # the next go:embed. Vite regenerates everything under web/dist — including
  # whatever it copies from web/public — so clearing it wholesale is safe and
  # closes the stale-embed blank-SPA failure mode more completely than clearing
  # only assets/.
  rm -rf web/dist
  (
    cd web
    # npm ci is reproducible from the lockfile; fall back to install if absent.
    if [ -f package-lock.json ]; then
      "$NPM" ci
    else
      warn "no package-lock.json — falling back to 'npm install' (not reproducible)"
      "$NPM" install
    fi
    "$NPM" run build
  )
else
  warn "--skip-frontend: reusing existing web/dist"
fi

[ -f web/dist/index.html ] || die "web/dist/index.html missing — run without --skip-frontend to build the UI"

# Fail fast if the built index.html references assets that are not present:
# that mismatch is exactly what blanks the SPA behind a masking 200.
missing_assets="$(grep -oE '/assets/[^"]+' web/dist/index.html | while read -r a; do
  [ -f "web/dist${a}" ] || echo "$a"
done)"
[ -z "$missing_assets" ] || die "web/dist is inconsistent (missing: ${missing_assets}); rebuild the frontend"

# ---------------------------------------------------------------------------
# 4. Optimized Go build.
#    - -trimpath + -ldflags '-s -w' : smaller, reproducible production binary.
#    - GOAMD64 : on amd64 pick the highest microarchitecture level the CPU
#      actually supports (v3 = AVX2/BMI/FMA, v4 = AVX-512). Only bump when the
#      features are confirmed, so the binary always runs on this host. arm64
#      (Apple Silicon) needs no such tuning.
# ---------------------------------------------------------------------------
GOAMD64_LEVEL="${GOAMD64:-}"
if [ -z "$GOAMD64_LEVEL" ] && [ "$ARCH" = "x86_64" ] && [ -r /proc/cpuinfo ]; then
  flags="$(awk -F': ' '/^flags/{print $2; exit}' /proc/cpuinfo)"
  if printf '%s' "$flags" | grep -qw avx512f; then
    GOAMD64_LEVEL="v4"
  elif printf '%s' "$flags" | grep -qw avx2 && printf '%s' "$flags" | grep -qw bmi2 && printf '%s' "$flags" | grep -qw fma; then
    GOAMD64_LEVEL="v3"
  elif printf '%s' "$flags" | grep -qw sse4_2 && printf '%s' "$flags" | grep -qw popcnt; then
    GOAMD64_LEVEL="v2"
  fi
  # If none matched (unusual/old CPU, or an ambiguous flags line), stay unset and
  # let Go build the safe v1 baseline rather than risk an illegal-instruction
  # binary from over-targeting.
  [ -n "$GOAMD64_LEVEL" ] || warn "GOAMD64: no recognized microarchitecture flags; building portable baseline"
elif [ -z "$GOAMD64_LEVEL" ] && [ "$ARCH" = "x86_64" ]; then
  warn "GOAMD64: /proc/cpuinfo unreadable; building portable baseline"
fi

# Set GOAMD64 inline (not via an array) so this stays portable to the bash 3.2
# that ships on macOS, where expanding an empty array under `set -u` errors.
if [ -n "$GOAMD64_LEVEL" ]; then
  info "Building the Go binary (native ${ARCH}, GOAMD64=${GOAMD64_LEVEL}, trimmed)…"
  GOAMD64="$GOAMD64_LEVEL" "$GO" build -trimpath -ldflags '-s -w' -o bin/agent ./cmd/agent
else
  info "Building the Go binary (native ${ARCH}, trimmed)…"
  "$GO" build -trimpath -ldflags '-s -w' -o bin/agent ./cmd/agent
fi

bin_size="$(du -h bin/agent | awk '{print $1}')"
info "Built bin/agent (${bin_size})"

if [ "$BUILD_ONLY" -eq 1 ]; then
  info "--build-only: skipping run. Start it with:  ./bin/agent --config ${CONFIG} --serve --addr ${ADDR}"
  exit 0
fi

# ---------------------------------------------------------------------------
# 5. Pre-run port check + run.
# ---------------------------------------------------------------------------
port="${ADDR##*:}"
if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
  die "port ${port} is already in use (a stale server can answer while the new bind silently fails); free it or pass --port"
fi

[ -f "$CONFIG" ] || warn "config ${CONFIG} not found — the server may start with no providers; pass --config"

info "Starting agent-smith…  (Ctrl-C to stop)"
printf '%sOpen http://%s in your browser.%s\n\n' "${c_dim}" "${ADDR}" "${c_reset}"
# The ${arr[@]+"${arr[@]}"} form expands to nothing (not an error) when the
# array is empty under `set -u` on bash 3.2.
exec ./bin/agent --config "$CONFIG" --serve --addr "$ADDR" ${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}
