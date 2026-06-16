#!/usr/bin/env bash
#
# Start the llama.cpp rpc-server on a worker, bound to THIS Mac's Thunderbolt
# bridge IP only. No hardcoded address: the link-local 169.254.x IP rotates on
# reboot, so we resolve bridge0 at start time. The coordinator auto-discovers
# this address (it resolves the worker hostname and probes the RPC port), so
# nothing on either side pins an IP.
#
# Usage (on the worker, or over ssh from the coordinator):
#   scripts/start-worker-rpc.sh [port]            # default port 50052
#   RPC_SERVER=/path/to/rpc-server scripts/start-worker-rpc.sh
#
# RPC is unauthenticated — binding the bridge IP (not 0.0.0.0) keeps it on the
# private Thunderbolt link only, never the LAN/Wi-Fi interface.
set -euo pipefail

PORT="${1:-50052}"
IFACE="${BRIDGE_IFACE:-bridge0}"
BIN="${RPC_SERVER:-$HOME/bin/rpc-server}"

if [ ! -x "$BIN" ]; then
  echo "error: rpc-server not found/executable at $BIN (set RPC_SERVER=...)" >&2
  exit 1
fi

IP="$(ipconfig getifaddr "$IFACE" 2>/dev/null || true)"
if [ -z "$IP" ]; then
  echo "error: $IFACE has no IP — is the Thunderbolt bridge up? (System Settings > Network)" >&2
  exit 1
fi

echo "starting rpc-server on ${IP}:${PORT} via ${IFACE} (private Thunderbolt link only)"
exec "$BIN" -H "$IP" -p "$PORT"
