# Cluster bring-up runbook — Path B (llama.cpp RPC), tuned for the M5 Max + M5 Pro

Goal: run agent-smith clustered with `Qwen2.5-72B-Instruct-abliterated` Q4 split
across both Macs at the best quality + throughput. Coordinator =
`Ikaros-MacBook-Pro-M5M.local` (64GB), worker = `ikaros-macbook-pro-m5p.local`
(`169.254.29.19`, 24GB).

What's already done on the coordinator (this repo): RAG corpora ingested (11
collections incl. `cybersecurity`); `configs/cluster.local.yaml` tuned with
`mode: auto`, `tensor_split 0.73,0.27`, `context_tokens 32768`, and verified
llama.cpp perf flags (`-fa on`, `-ctk/-ctv q8_0`). `bin/agent` built.

Steps below are operator-run (build + 44GB model + worker are not reachable from
the coordinator's tooling).

## 1–2. Build ONCE on the coordinator, ship the worker binary (DONE)
The worker has no Homebrew/cmake, so build a self-contained static binary on the
coordinator and copy it — this also guarantees RPC version parity (the worker
runs the *identical* binary, verified by matching SHA256). No build tools on the
worker.
```sh
# Coordinator (M5 Max) — already executed this session:
git clone --depth 1 https://github.com/ggml-org/llama.cpp ~/llama.cpp && cd ~/llama.cpp
cmake -B build -DCMAKE_BUILD_TYPE=Release \
  -DGGML_RPC=ON -DGGML_METAL=ON -DGGML_METAL_EMBED_LIBRARY=ON \
  -DBUILD_SHARED_LIBS=OFF -DLLAMA_CURL=OFF
cmake --build build --config Release -j --target llama-server rpc-server
otool -L build/bin/rpc-server     # acceptance gate: only /System + /usr/lib deps (verified PASS)

# Ship rpc-server to the worker (M5 Pro) — already executed:
ssh ikaros-macbook-pro-m5p.local 'mkdir -p ~/bin'
scp build/bin/rpc-server ikaros-macbook-pro-m5p.local:~/bin/rpc-server
ssh ikaros-macbook-pro-m5p.local 'chmod +x ~/bin/rpc-server; shasum -a 256 ~/bin/rpc-server'
# Worker SHA256 must equal the coordinator's = parity by identity (verified match).
```
`configs/cluster.local.yaml` already points `runtime.llama_cpp.server` at
`~/llama.cpp/build/bin/llama-server` (absolute path, no sudo/PATH install).
If you ever rebuild, re-ship `rpc-server` so both sides stay byte-identical.

## 3. Get the model (COORDINATOR — it holds the .gguf; the worker only needs the binary)
```sh
# The huihui-ai repo (huihui-ai/Qwen2.5-72B-Instruct-abliterated) ships
# SAFETENSORS only — no GGUF. Pull the GGUF from mradermacher's quant of those
# exact weights (cardData base_model = huihui-ai/Qwen2.5-72B-Instruct-abliterated),
# a single 47.4GB Q4_K_M file (fits 64+24=88GB cluster, not a single 24GB node).
pipx install "huggingface_hub[cli]"     # gives `hf`
mkdir -p /Users/shared/models
hf download mradermacher/Qwen2.5-72B-Instruct-abliterated-GGUF \
  Qwen2.5-72B-Instruct-abliterated.Q4_K_M.gguf \
  --local-dir /Users/shared/models
# Lands at the path in configs/cluster.local.yaml:
#   /Users/shared/models/Qwen2.5-72B-Instruct-abliterated.Q4_K_M.gguf
```

## 4. Start the worker (WORKER, m5p)
```sh
# Use the helper — it binds rpc-server to the worker's CURRENT Thunderbolt
# bridge0 IP (link-local 169.254.x rotates on reboot, so never hardcode it).
# Over SSH from the coordinator, or in a Terminal on the worker:
ssh ikaros-macbook-pro-m5p.local 'cd ~/path/to/agent-smith && scripts/start-worker-rpc.sh'
#   or, if the repo isn't on the worker, copy the one-liner it runs:
#   IP=$(ipconfig getifaddr bridge0); ~/bin/rpc-server -H "$IP" -p 50052
# Leave it running; it exposes this Mac's Metal device. The coordinator
# AUTO-DISCOVERS this address (resolves the worker hostname + probes :50052),
# so no IP is pinned on either side and a reboot needs no config edit.
```

## 5. Launch agent-smith clustered (COORDINATOR)
```sh
cd /Users/ikarolaborda/Personal/agent-smith
# Context7 must be on for grounded code answers:
echo 'CONTEXT7_API_KEY=...' >> .env     # if not already set
./bin/agent --serve --cluster-config configs/cluster.local.yaml --rag-max-chunks 12
```
agent-smith launches, on the coordinator:
```
llama-server --model /Users/shared/models/…Q4_K_M.gguf --host 127.0.0.1 --port 8082 \
  -ngl 99 --ctx-size 16384 --rpc 169.254.x.y:50052 \
  --tensor-split 0.85,0.15 -fa on -ctk q8_0 -ctv q8_0
```
The `--rpc` target is the live IP agent-smith auto-discovered from the worker
hostname (logged as `cluster: resolved rpc host configured=… selected=…`); if the
worker's rpc-server is down it logs `rpc host … not reachable … refusing to launch
single-node` and falls back to local rather than risk OOMing the coordinator with
a single-node 72B.
Set `cluster.mode: llama_cpp_rpc` to pin this backend (default `auto` tries
exo→mlx→llama→local; with only llama.cpp built it lands here).

## 6. Verify it's actually clustered (COORDINATOR)
```sh
curl -s 127.0.0.1:8080/v1/models
# then send a chat (web UI at :8080 or POST /v1/chat/completions) and watch logs for:
#   cluster: node discovered node=m5pro reachable=true
#   cluster: launching llama-server rpc_hosts=[ikaros-macbook-pro-m5p.local:50052] tensor_split=0.73,0.27
#   cluster: backend selected backend=llama_cpp_rpc   (NOT local)
#   cluster: request complete ttft_ms=… tokens_per_sec=…
# On the worker, rpc-server logs incoming offload activity.
```

## Validation checkpoints (run once, observe both Macs)
The 24GB worker is the tightest constraint — startup can succeed yet a later
larger-batch prompt can still OOM. Validate in order, watching memory on BOTH
Macs (`Activity Monitor` or `sudo memory_pressure` / `vm_stat`):
1. `rpc-server` up on the worker bridge IP; coordinator can reach it (`nc -vz 169.254.29.19 50052`).
2. Launch once with the 72B model; watch **resident memory on each Mac during load and after the first prompt**.
3. Run a short sanity suite: one long-context prompt, one RAG-grounded cybersecurity query, one multi-turn exchange — confirm no truncation, KV corruption, or retrieval regression.
4. Capture, separately: prompt-eval tok/s, generation tok/s, first-token latency.

## Worker kernel panic (observed) and the safe starting profile
The 24GB worker once **kernel-panicked** (`watchdog timeout: no checkins from
watchdogd in 90s` → reboot) while loading its `0.27` share at `32768` ctx — the
system went unresponsive under memory/GPU over-commit. The config now ships a
conservative default: `tensor_split 0.85,0.15`, `context_tokens 16384`. The
coordinator's 64GB carries the bulk; the worker takes only a slice it can hold
after macOS overhead. **Tune UP from here, one knob at a time, only after a clean
warm load + sustained generation while watching the worker's memory_pressure.**

## Tuning ladder (raise capability) / fallback ladder (if it OOMs or thrashes)
From the safe profile, raise ONE knob at a time and watch BOTH Macs' memory:
1. Raise `models[].context_tokens`: 16384 → 24576 → 32768 (KV is the main pressure).
2. Then raise the worker share: `tensor_split` 0.85,0.15 → 0.82,0.18 → 0.78,0.22.
If it OOMs/thrashes/panics, go back down the same ladder (lower ctx first, then
shift `tensor_split` back toward the coordinator). Never run quantized KV
(`-ctk/-ctv q8_0`) without `-fa on`.

## Performance & quality notes
- **KV cache q8_0 + `-fa on`** is the lever that fits 32k context for a 72B Q4
  across 64+24GB. If you have headroom and want max fidelity, drop to f16 KV
  (remove the `-ctk/-ctv` args) and/or lower `context_tokens`.
- **tensor_split 0.73,0.27** matches the 64:24 memory ratio. If the worker OOMs,
  shift more to the coordinator (e.g. `0.8,0.2`); if the coordinator is the
  bottleneck, the reverse. Watch each node's memory pressure.
- **Grounding/quality:** RAG is ingested and `--rag-max-chunks 12` widens
  evidence; the always-on directive makes the model cite-or-abstain on security
  specifics. Re-run `make ingest` whenever you add docs under `docs/<collection>`.
- **Benchmark before trusting throughput** (the only number that matters): run a
  fixed prompt and read `tokens_per_sec` from the logs; compare single-node
  (drop `--rpc`) vs clustered. Thunderbolt-5 RPC adds inter-node latency, so the
  cluster wins only when the model does not fit one node — which is exactly the
  72B case here.
- **Fallback:** `strict_cluster: false` means if the worker/rpc-server is down,
  agent-smith transparently uses the local Ollama provider instead of failing.
