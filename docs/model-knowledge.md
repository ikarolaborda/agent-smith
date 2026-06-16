# Imbuing the clustered model with knowledge & standards

Target model: `huihui-ai/Qwen2.5-72B-Instruct-abliterated`, served across the
two-Mac cluster (see `docs/cluster.md`, `configs/cluster.local.yaml`).

## The mechanism (no fine-tuning)

You do **not** retrain or fine-tune the frozen Q4 weights to give the model
domain knowledge or enforce standards. agent-smith imbues it at **runtime**
through three always-on layers that wrap every request (and every backend,
cluster included — the augmentation lives in the agent layer, not the provider):

1. **Enforced system directive** — `pkg/prompt.EngineeringDirective` (+
   `CodingParadigmDirective`) is injected into the system message on every
   request by `agent.composeMessages`. It enforces: PHP OOP + Clean
   Architecture/SOLID/PSR-12; idiomatic, current Go; **mandatory Context7 for any
   third-party code** (never invent APIs); and the authorized-defensive security
   posture.
2. **RAG corpora** — curated markdown under `docs/<collection>` is embedded and
   retrieved per request. Relevant collections: `cs-fundamentals`, `go-lang`,
   `php`, `laravel`, `native-php`, `architectural-patterns`, and the new
   `cybersecurity` (defensive: OWASP, vuln classes, CVE/CWE workflow, secure
   coding). Add files to a collection's folder to extend its knowledge.
3. **Context7** — live, version-correct library/framework docs, on by default
   when `CONTEXT7_API_KEY` is set. The directive makes the model rely on it for
   code rather than its training memory.

## One-time setup

```sh
# Ingest all corpora (needs Ollama running for nomic-embed-text embeddings):
make ingest                       # loops docs/* incl. cybersecurity
# Context7 (mandatory for code): put the key in .env at the repo root
echo 'CONTEXT7_API_KEY=...' >> .env
```

Then run the cluster normally:
```sh
./bin/agent --serve --cluster-config configs/cluster.local.yaml
```
RAG + Context7 + the directive are automatically applied to the clustered Qwen.

## Grounding & anti-hallucination (the real goal for the abliterated model)

In a controlled, authorized cyber-lab the model does not refuse offensive-security
tasks — that is intended. The requirement is therefore **enough context per task +
no fabrication**, which these layers provide:

- **Cite-or-abstain directive** — `EngineeringDirective` makes grounding the hard
  rule: anchor every security claim (CVE IDs, CVSS, affected versions, exploit
  primitives, syscall/API behavior) in retrieved context; if a specific isn't in
  context, say so — never fabricate. "A confident wrong answer is the worst
  outcome; an honest 'not in my context' is correct."
- **RAG confidence band + abstention** (`internal/rag`) — every augmented prompt
  carries `RETRIEVAL CONFIDENCE: high|medium|low` and an instruction to abstain on
  low confidence, now explicitly extended to security specifics (no invented
  CVE/CVSS/version/offset).
- **Wider evidence on the cluster** — `--rag-max-chunks` injects more retrieved
  chunks (byte cap scales with it); the cosine threshold still gates relevance, so
  it adds grounding, not noise.
- **Bigger window** — `context_tokens: 32768` (Qwen2.5 native) so more
  RAG/CVE/code evidence fits per request.
- **Context7** — live API/version facts for code.

Recommended cluster launch (grounding-tuned):
```sh
make ingest                                   # populate RAG (REQUIRED — empty RAG = free-wheeling model)
./bin/agent --serve --cluster-config configs/cluster.local.yaml --rag-max-chunks 12
```
The single biggest anti-hallucination lever is **running `make ingest`**: with empty
collections the model falls back to parametric memory and will invent specifics.

## Security note — abliterated model

"Abliterated" means the model's built-in refusal behavior was removed. The
offensive-security/CVE knowledge is therefore bounded by the **system
directive**, which is the safety layer: use that knowledge to find and **patch**
vulnerabilities in code the operator **owns**, produce fixes + regression tests +
mitigations — and not to generate weaponized exploits, malware, persistence,
credential theft, or to assist intrusion against systems the operator does not
own. Keep `runtime.private_cluster_only: true` and the network on the private
Thunderbolt bridge.

## Verifying it took effect

- `make ingest` prints each collection (including `cybersecurity`).
- Ask the cluster a PHP question → expect Clean Architecture / SOLID framing.
- Ask about a library API → expect it to cite/await Context7 rather than guess.
- Ask to review owned code for vulns → expect finding + secure fix + test, and a
  refusal to produce a weaponized exploit.
