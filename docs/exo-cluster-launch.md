# exo 2-node Thunderbolt cluster — launch runbook

Working fix (2026-06-17): exo's IPv6 link-local multicast discovery is non-deterministic on
multi-homed macOS and its link-local zenoh session expires. We patch exo to dial peers over
the routable Thunderbolt IPv4 link via a new env var.

## Patch
`~/exo/rust/networking/src/lib.rs` cfg() injects zenoh `connect/endpoints` from
`EXO_CONNECT_ENDPOINTS` (opt-in; no change when unset). Preserved at:
- branch `agent/exo-tb-connect-endpoints` (commit ee78f10) in ~/exo on BOTH Macs
- `patches/exo-connect-endpoints.patch` in this repo (re-apply after any `git pull` in ~/exo, then rebuild)

Rebuild after (re)applying: `cd ~/exo && uv sync --reinstall-package exo_rs`

## Launch (BOTH Macs)
TB link: coordinator en2=10.0.0.1, worker en1=10.0.0.2 (static /24). en10=StarTech 10GbE = internet, do NOT touch.

Coordinator (M5 Max):
```
cd ~/exo
EXO_CONNECT_ENDPOINTS='tcp/10.0.0.1:52414,tcp/10.0.0.2:52414' uv run exo --namespace exo-tb-cluster
```
Worker (M5 Pro, ssh ikaros-macbook-pro-m5p.local):
```
cd ~/exo
EXO_CONNECT_ENDPOINTS='tcp/10.0.0.1:52414,tcp/10.0.0.2:52414' uv run exo --no-api --namespace exo-tb-cluster
```
(--namespace optional since both at same version, but explicit is safer.)

## Verify peered
- `lsof -nP -iTCP:52414 | grep ESTABLISHED` shows 10.0.0.1<->10.0.0.2:52414 on both.
- coordinator log: `discovered: Some(<peer-zid>)` (a zid != self).
- `curl -s http://127.0.0.1:52415/v1/models` serves (API up).

## Stop
exo daemonizes: `pkill -f 'uv run exo'; [ -f ~/.exo/exo.pid ] && kill $(cat ~/.exo/exo.pid)` (no `exo stop`).

## Next milestone (not yet done)
scripts/exo-staged-bench.sh — small dense model first, then 70B only after checking worker peak_memory (24GB ceiling).
