# Cluster bring-up runbook — Path B (llama.cpp RPC), tuned for the M5 Max + M5 Pro

Goal: run agent-smith clustered with `Qwen2.5-72B-Instruct-abliterated` Q4 split
across both Macs at the best quality + throughput. Coordinator =
`Ikaros-MacBook-Pro-M5M.local` (64GB), worker = `ikaros-macbook-pro-m5p.local`
(`169.254.29.19`, 24GB).

What's already done on the coordinator (this repo): RAG corpora ingested (9
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
mkdir -p /Users/shared/models
# Q4_K_M (~44GB) fits the 64+24=88GB cluster, not a single 24GB node.
# Use your normal Hugging Face download flow for:
#   huihui-ai/Qwen2.5-72B-Instruct-abliterated  (a Q4_K_M GGUF)
# Save to the path in configs/cluster.local.yaml:
#   /Users/shared/models/Qwen2.5-72B-Instruct-abliterated-Q4_K_M.gguf
```

## 4. Start the worker (WORKER, m5p)
```sh
# Bind to the Thunderbolt-bridge IP only (RPC is unauthenticated — private link).
# (over SSH from the coordinator, or in a Terminal on the worker)
~/bin/rpc-server -H 169.254.29.19 -p 50052
# leave running; it exposes this Mac's Metal device to the coordinator.
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
  -ngl 99 --ctx-size 32768 --rpc ikaros-macbook-pro-m5p.local:50052 \
  --tensor-split 0.73,0.27 -fa on -ctk q8_0 -ctv q8_0
```
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

## Fallback ladder (if it OOMs or thrashes)
Change ONE knob at a time, cheapest first:
1. Lower `models[].context_tokens`: 32768 → 24576 → 16384 (KV is the main pressure).
2. Only then shift `tensor_split` toward the coordinator to protect the 24GB worker: `0.73,0.27` → `0.75,0.25` → `0.76,0.24`.
3. As a last resort, drop `-fa on`/`-ctk/-ctv q8_0` only if you switch to a smaller ctx with f16 KV (do not run quantized KV without `-fa on`).

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
