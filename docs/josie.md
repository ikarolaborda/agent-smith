# Running the Josiefied-Qwen3.5-0.8B model

[`Josiefied-Qwen3.5-0.8B-gabliterated-v1`](https://huggingface.co/Goekdeniz-Guelmez/Josiefied-Qwen3.5-0.8B-gabliterated-v1)
is a ~0.9B abliterated ("J.O.S.I.E.") chat model. It runs comfortably on a
laptop CPU and fits agent-smith's offline-first design.

agent-smith needs **no code change** to use it: the Ollama provider accepts any
model name and discovers installed models via `/api/tags`. The only work is
getting the model into Ollama and ingesting the RAG corpora.

## Quick start (Ollama — recommended)

```sh
make josie        # or: ./scripts/setup-josie.sh
make serve CONFIG=configs/josie.yaml
# web UI at http://127.0.0.1:9090
```

`scripts/setup-josie.sh`:

1. builds the Ollama model `josie-qwen3.5` from
   [`configs/josie/Modelfile`](../configs/josie/Modelfile), pulling the
   community GGUF (`mradermacher/Josiefied-Qwen3.5-0.8B-gabliterated-v1-GGUF`,
   `Q4_K_M` by default) straight from Hugging Face — the base model is not on
   the Ollama registry yet;
2. pulls `nomic-embed-text` for embeddings;
3. builds `bin/agent`;
4. ingests all eight shipped corpora (Laravel, PHP, NestJS, Tailwind/CSS,
   architectural patterns, NativePHP, CS fundamentals, Go) with the same
   embedder used at query time.

It is re-runnable: `ollama create` and the content-hashed ingest are
idempotent. Override `MODEL_TAG`, `EMBED_MODEL`, or `CONFIG` via env vars.

### Why the model tag must be flat

In `--serve` mode the OpenAI-compatible HTTP layer splits the request model id
on `/` to recover the provider. A slashy Ollama tag (e.g. running the raw
`hf.co/mradermacher/...:Q4_K_M` id directly) would be misrouted. The Modelfile
deliberately creates a **flat** tag, `josie-qwen3.5`; keep it that way.

### Picking a quant

Edit the `FROM` line in `configs/josie/Modelfile`. `Q4_K_M` is a good
size/quality default; `Q8_0` is higher quality and larger; `Q3_K_M` is
smaller. All quants are listed in the GGUF repo.

## Alternative backend: vLLM / SGLang (no GGUF)

The safetensors can be served directly and reached via the existing `openai`
provider — also zero code changes:

```sh
vllm serve "Goekdeniz-Guelmez/Josiefied-Qwen3.5-0.8B-gabliterated-v1" --port 8000
./bin/agent --provider openai --model Goekdeniz-Guelmez/Josiefied-Qwen3.5-0.8B-gabliterated-v1 \
  --config configs/josie.yaml --prompt "hello"
```

with the `openai` block pointed at `base_url: http://localhost:8000/v1` and
`api_key: dummy`. This needs a GPU/CUDA + Python; the Ollama path does not.

## Notes and caveats

- **Web grounding** is on by default for Ollama models, which helps a small
  model resist hallucination. Toggle per-conversation in the UI, or disable
  globally with `--no-web-search`.
- **Tool calling** is unreliable at 0.9B. The agent loop tolerates models that
  never emit tool calls (they just answer); don't rely on multi-step tool use.
- **Thinking tokens**: the model card does not document a Qwen3 `<think>` mode.
  If your chosen GGUF emits visible `<think>…</think>` blocks, disable it in the
  Modelfile (e.g. append `/no_think` in a custom `TEMPLATE`/`SYSTEM`) and
  rebuild — agent-smith forwards model content verbatim and does not strip it.
- **Uncensored**: this is an abliterated model with refusal vectors removed.
  Use it only where that is appropriate and authorized.
