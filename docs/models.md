# Local model collection

agent-smith auto-discovers every installed Ollama model via `/api/tags`, so
adding a model to the collection is just an `ollama pull` / `ollama create` away
— no code change. `/v1/models` reports each model's `supports_vision` flag
(derived from Ollama's `/api/show` capabilities), which the chat UI uses to
gate image paste.

## Installed abliterated models

| Model (Ollama tag) | Size | Notes |
| --- | --- | --- |
| `josie-qwen3.5` | ~0.6 GB | Josiefied-Qwen3.5-0.8B-gabliterated — tiny, fast, text-only. Built from `configs/josie/Modelfile`. See [`josie.md`](josie.md). |
| `huihui_ai/gemma3-abliterated:12b-q8_0` | ~13 GB | Abliterated Gemma 3 12B, **vision-capable**. The practical vision model for a 24 GB machine. |
| `huihui_ai/gpt-oss-abliterated:20b` | ~13 GB | Abliterated GPT-OSS 20B, text-only. |

```sh
ollama pull huihui_ai/gemma3-abliterated:12b-q8_0   # add the vision model
make josie                                          # build + ingest for josie
```

## Picking a quant for your RAM

A model must fit in unified memory (weights + KV cache + overhead). On a **24 GB**
machine:

- **27B is impractical** — huihui ships gemma3-abliterated 27B only as f16 (55 GB)
  or q8_0 (30 GB); neither fits, and even a Q4 (~17 GB) swaps hard and crawls.
- **12B q8_0 (~13 GB)** fits with headroom — the sweet spot here.
- The bare `:12b` tag is **fp16 (24 GB)** — do not use it on 24 GB RAM; pick an
  explicit quantized tag (`:12b-q8_0`).

## Vision / image paste

Gemma 3 is multimodal. Selecting a vision-capable model (e.g.
`huihui_ai/gemma3-abliterated:12b-q8_0`) makes `/v1/models` report
`supports_vision: true`, which enables pasting images into the composer. Text-only
models (josie, gpt-oss) leave image paste disabled by design. See
[`vision`](../web/src/components/Composer.tsx) gating and `internal/server`
capability detection.
