# Local models via llama.cpp

agent-smith can resolve, assess, download, and serve GGUF models without Ollama.
Go remains the control plane: it supervises a loopback `llama-server` process
and talks to its OpenAI-compatible API. Inference stays in upstream
[llama.cpp](https://github.com/ggml-org/llama.cpp), consistent with
[ADR 0001](adr/0001-llama-cpp-native-in-go-feasibility.md).

The design is fail-closed. A model is never downloaded merely because its name
looks compatible, and a downloaded file is never launched merely because it
exists. See [ADR 0004](adr/0004-safe-local-model-admission.md) for the trust and
lifecycle decisions.

## Prerequisite

Install a recent `llama-server` on `PATH`. The fastest path is `./bin/agent
--install-runtime`, which detects this host's OS/GPU, downloads the matching
prebuilt upstream build (Vulkan by default, so one artifact serves AMD, NVIDIA,
and Intel), and links `llama-server` onto `PATH`. Prefer it over a distro package:
multimodal architecture support moves quickly, so a current upstream build is more
likely to load a new projector. The official
[multimodal documentation](https://github.com/ggml-org/llama.cpp/blob/master/docs/multimodal.md)
describes the model-plus-`mmproj` contract.

## Inspect before downloading

`--inspect-model` fetches repository metadata only. It resolves a mutable branch
to a commit, selects an exact GGUF set, reads current host memory and free disk,
estimates weights/projector/KV/scratch/headroom, prints JSON, and exits. A
rejected report has a non-zero exit status and performs no artifact GET.

```sh
./bin/agent --inspect-model \
  hf.co/2lains/Huihui-Step3-VL-10B-abliterated-GGUF:Q4_K_M
```

Important report fields are `manifest.commit_sha`, every artifact's `size` and
`sha256`, `fit.estimated_runtime_bytes`, `fit.available_memory_budget`,
`fit.required_disk_bytes`, and `fit.reasons`. `fit` is admission to try a launch,
not a throughput guarantee. The estimator is deliberately conservative and
does not model every architecture-specific KV layout. Discrete-GPU VRAM is not
guessed: non-zero GPU offload is rejected outside Apple unified-memory hosts.

## Configure an exact model

```yaml
default_provider: llamacpp
providers:
  llamacpp:
    model: huihui-step3-vl-10b-q4
    llama_cpp:
      repo: 2lains/Huihui-Step3-VL-10B-abliterated-GGUF
      file: Huihui-Step3-VL-10B-Q4_K_M.gguf
      mmproj_file: Step3-VL-10B-mmproj-F16.gguf
      revision: main       # resolved to a commit before any artifact transfer
      ctx_size: 4096       # admitted and passed explicitly; never auto-maximized
      parallel: 1          # each sequence increases KV/runtime memory
      gpu_layers: 99       # Apple unified memory only; 0 is explicit CPU mode
      jinja: true
      binary: llama-server
      startup_timeout_seconds: 300
```

Downloaded files default to `~/.agent-smith/models`; set `models_dir` only when
a different volume is intentional. `HF_TOKEN` is used
for gated/private repositories. The token is sent as an authorization header and
is never logged.

For an existing local conversion, use `model_path` and, for vision,
`mmproj_path`; omit all repository selectors. Local files still receive GGUF,
complete-split, size, live-memory, and disk-profile checks immediately before
launch.

## Pull and run

```sh
# Prints the fit plan before transfer, then the committed artifact paths.
./bin/agent --pull \
  hf.co/2lains/Huihui-Step3-VL-10B-abliterated-GGUF:Q4_K_M

./bin/agent --provider llamacpp --prompt "Explain goroutine scheduling"
./bin/agent --provider llamacpp --serve
```

An unused `llamacpp` config is not started by `--serve`; selecting it explicitly
is the consent boundary for download and launch. The server binds loopback only.
Each launch receives an ephemeral API key through a private `0600` key file
(removed after authenticated readiness), and readiness requires an
authenticated `/v1/models` response because llama.cpp intentionally leaves
`/health` public. The child starts offline with UI, slots, and prompt-cache RAM
disabled; batch sizes and GPU offload are explicit. The application rejects
non-loopback hosts, protected resource/network/tool options inside `extra_args`,
and passes a minimal environment that excludes API keys and Hugging Face tokens.

## What the acquisition boundary verifies

- Hugging Face metadata must expose GGUF LFS blob sizes and SHA-256 identities.
- The repository revision is resolved to an immutable commit before payload GETs.
- A split quantization is accepted only when every numbered shard exists.
- An exact `file` pins one model set; when `quant` is also supplied it must match
  that file. Without `file`, the requested quant (default `Q4_K_M`) must select
  exactly one set; a lone non-default artifact is never silently substituted.
- Multiple projectors require an explicit `mmproj_file` pin.
- Disk and current memory admission happens before the first artifact body GET.
- Transfers resume into temporary files, then verify length, SHA-256, and GGUF
  magic before publication. A complete verified cache is usable offline.
- Cross-process locks serialize volume admission/download and one local runtime
  per OS account; lock acquisition is context-cancellable.
- Host capacity and local files are checked again immediately before process
  execution because memory pressure can change during a long download. Remote
  artifacts are also re-hashed against their immutable manifest at this point.
- The child has one lifecycle owner and is not ready until public health plus an
  authenticated OpenAI-compatible model-list probe succeeds.

SHA-256 establishes artifact identity, not safety. Upstream llama.cpp explicitly
recommends running untrusted models in an isolated environment; agent-smith's
subprocess boundary and secret-minimized environment reduce blast radius but are
not a filesystem sandbox. Treat a configured publisher/converter as trusted,
review its provenance and license, and use a container or VM for untrusted GGUFs.
See the upstream [security guidance](https://github.com/ggml-org/llama.cpp/security).

## Status of the two target model families

### Huihui Step3-VL 10B

The requested
[source repository](https://huggingface.co/huihui-ai/Huihui-Step3-VL-10B-abliterated)
contains Transformers/Safetensors plus custom code, not a llama.cpp artifact set;
agent-smith correctly rejects it. The example above deliberately names the
third-party
[2lains GGUF conversion](https://huggingface.co/2lains/Huihui-Step3-VL-10B-abliterated-GGUF/tree/main):
the Q4_K_M model is about 5.03 GB and its required F16 projector about 3.96 GB.
This is a provenance choice, not an endorsement; inspect the repository and
license before trusting it.

Step3-VL support landed in llama.cpp in
[PR 21287](https://github.com/ggml-org/llama.cpp/pull/21287), followed by the F16
projector conversion correction in
[PR 21646](https://github.com/ggml-org/llama.cpp/pull/21646). Use a llama.cpp build
containing commit `1e9d771` or later. Older builds may load text but fail on image
encoding. Vision is advertised in `/v1/models` only after a projector was
resolved, validated, and actually supplied to the runtime.

### Dolphin Mistral 24B Venice Edition

The requested
[Dolphin source](https://huggingface.co/dphn/Dolphin-Mistral-24B-Venice-Edition)
is a 24B BF16 Transformers/Safetensors multimodal model. It is not directly
runnable by this provider. The publisher's similarly named
[GGUF repository](https://huggingface.co/dphn/Dolphin-Mistral-24B-Venice-Edition-GGUF/tree/main)
currently contains only metadata/README files and no GGUF weights, so
agent-smith refuses it instead of silently substituting an unrelated conversion.

Third-party Q4 conversions are roughly 14.4 GB before projector, KV cache,
scratch allocations, and OS headroom. Do not infer that a 16 GB or 24 GB machine
can run one from the quantized file size alone. Choose and pin a conversion only
after verifying its conversion version, chat template, and matching projector;
then let `--inspect-model` make the point-in-time host decision.

## RAG applies to this provider too

The eleven curated knowledge collections are embedded in the Go binary and
retrieved above the provider boundary. llama.cpp, Ollama, cloud, and cluster
models therefore receive the same computer-science, architecture, PHP/OOP,
Clean Code/Clean Architecture, networking, and authorized cybersecurity
grounding. Dense ingestion is optional; first-launch lexical retrieval is not.
`--no-rag` remains an explicit operator kill switch.

## Running fully independent of Ollama

The `llamacpp` provider is self-contained: it downloads the GGUF from Hugging
Face and supervises `llama-server` itself, with no Ollama and no cgo. The
`internal/llm/llamacpp` package imports nothing from `internal/llm/ollama`. To
run the app with no Ollama at all, set `default_provider: llamacpp` (see
`configs/qwythos.yaml`, or `configs/gpt-oss-abliterated.yaml` for the native
equivalent of the Ollama `huihui_ai/gpt-oss-abliterated:20b` model — the same
huihui-ai abliterated gpt-oss-20B served directly from its MXFP4 GGUF) and use a
non-Ollama embedder (`--embedder openai`) or rely on first-launch lexical
retrieval, which needs no embedder. Ollama stays a first-class option — it is the
default in `configs/config.example.yaml` — but it is never a hard dependency.

## Abliterated model downgrade on resource exhaustion

Abliterated models are this app's primary target. When the fit gate refuses the
requested model on this host (not enough memory/VRAM/disk), the rejecting
`FitReport` carries an advisory `suggestion`: a smaller abliterated model from a
curated, offline ladder (`internal/llm/llamacpp/catalog.go`) that the fit gate
re-checks and believes will run here. The suggestion appears in the
`--inspect-model` / `--pull` JSON and in the human-readable error. It is
advisory: the normal fit gate and downloader re-validate the suggested model
(existence, real sizes, live host memory) when you actually pull it, so a stale
ref fails safely with the ordinary error. The ladder is dense and text-only so
its footprint math stays trustworthy; models bundling a vision projector
(mmproj) are not cross-suggested. Override the ladder programmatically via
`FitPolicy.FallbackLadder` (YAML wiring is a follow-up).
