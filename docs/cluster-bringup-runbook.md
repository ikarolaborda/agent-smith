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

## 1. Toolchain (BOTH Macs)
```sh
brew install cmake          # coordinator is missing it; worker likely too
xcode-select --install 2>/dev/null || true   # Metal toolchain
```

## 2. Build llama.cpp with RPC + Metal (BOTH Macs)
```sh
git clone https://github.com/ggml-org/llama.cpp && cd llama.cpp
cmake -B build -DGGML_RPC=ON -DGGML_METAL=ON -DCMAKE_BUILD_TYPE=Release
cmake --build build --config Release -j
# verify:
ls build/bin/llama-server build/bin/rpc-server
# put them on PATH (or set runtime.llama_cpp.server to the full path):
sudo cp build/bin/llama-server build/bin/rpc-server /usr/local/bin/
```

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
rpc-server -H 169.254.29.19 -p 50052
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
