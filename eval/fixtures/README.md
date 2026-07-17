# Graph-salient retrieval benchmark — frozen protocol

This directory holds the decision-grade benchmark for the `graph_expand`
knowledge-graph hop. It exists because two earlier fixture designs were each
biased in opposite directions (handpicked-graph-favoring vs lexical-top-N
label leakage) and left the graph's value **underdetermined**. This protocol
was frozen BEFORE the first run; queries, labels, and decision rules must not
change after results are seen.

## Corpus snapshot

- Labels were authored against the embedded lexical corpus at commit `e36de01`
  (355 chunks; `docs/**/*.md` via the standard chunker). Chunk ids are
  `sha256(source||heading||ordinal||body)`-derived, so any re-chunking or doc
  edit invalidates affected labels.
- Retrieval budget: `k = 3` (`--rag-max-chunks 3`), graph hop = 1,
  `maxGraphExpand = 5` (as shipped).

## Files

- `graph_salient_queries.json` — Stratum A: multi-hop / cross-source queries.
- `simple_control_queries.json` — Stratum B: single-hop noise controls.

Both are the `--eval-rag` fixture shape (`[{query, relevant_chunk_ids}]`).
Extra fields (`justification`, `gold`, `annotations`) are documentation for
humans; the Go harness ignores unknown JSON fields.

## Construction rules (Stratum A)

1. Each query must be answerable ONLY by combining >= 2 chunks, preferably
   from different source files or non-adjacent sections.
2. The justification must state the required cross-source/hop relationship.
3. A candidate query satisfiable by a single chunk is rejected.

## Labeling rules (blind protocol)

1. Gold labels were chosen CONTENT-FIRST: by reading `docs/**/*.md` sections
   and judging relevance from the text, before running the query through
   lexical search or the graph for that query.
2. Only after gold labels were frozen were they annotated with
   `lexical_rank` (position in lexical ranking at wide k, 0 = not in top-20)
   and `graph_reachable` (within 1 hop of the lexical top-3 seeds).
3. Labels are (source, heading, ordinal) mapped to chunk ids with the
   dev-only `raginspect -dump` instrument (id mapping only, not ranking).

## Metrics (per stratum)

- mean recall@budget: base (k=3), graph (k=3 + 1 hop), budget-matched control
  (plain search at k + graph_added — same final candidate count).
- per-query win rate: graph recall > matched recall.
- graph coverage: fraction of gold chunks reachable within 1 hop of seeds
  (Stratum A only; reported separately, never folded into recall).
- graph_added_precision as the noise diagnostic.

## Decision rules (frozen before first run)

Stratum A (n = 10):
- KEEP: graph beats matched control by >= 0.10 mean recall AND wins >= 60%
  of queries AND coverage >= 50%.
- DEMOTE to opt-in: lift in [0.05, 0.10) with coverage >= 50%, or mixed
  results straddling the thresholds.
- REMOVE / disable-by-default: lift <= 0 on Stratum A AND Stratum B shows
  equal-or-worse noise behavior.
- COVERAGE BOTTLENECK: coverage < 50% is an edge-construction verdict, not a
  graph-value verdict. Then run exactly ONE predefined follow-up (below) and
  re-run this benchmark once, changing nothing else.

Stratum B guardrail (n = 6): the graph result must not fall below the
budget-matched control by more than 0.05 mean recall on simple queries;
a larger gap counts as graph-noise evidence even if Stratum A improves.

## Predefined follow-up (the only allowed tuning between runs)

If coverage < 50%: densify cross-source edges by (a) widening the entity band
(`maxEntityChunks`) and/or (b) adding heading-token overlap edges across
sources. No query, label, threshold, or budget changes.

## Results (frozen run at k=3)

Reproduce: `go run ./cmd/agent --eval-rag eval/fixtures/graph_salient_queries.json --rag-max-chunks 3`
(and the `simple_control_queries.json` file). Deterministic across runs.

| stratum | base | graph | matched control | lift (graph−matched) | wins | added/q | added precision |
|---------|------|-------|-----------------|----------------------|------|---------|-----------------|
| A (graph-salient, n=10) | 0.233 | 0.267 | 0.383 | −0.117 | 0/10 | 4.5 | 0.02 |
| B (simple control, n=6) | 0.833 | 0.833 | 1.000 | −0.167 | 0/6 | 4.0 | 0.00 |

Graph **coverage of gold labels** (fraction of hand-labeled chunks reachable
within 1 hop of the lexical top-3 seeds): **~4%** — of 34 Stratum-A gold
chunks, only 1 non-seed gold was graph-reachable. This is a COVERAGE
BOTTLENECK per the decision rules, not a graph-value verdict: the edges needed
to reach the genuinely-relevant cross-source chunks mostly did not exist.

### Predefined follow-up applied, then re-run

Densified cross-source edges exactly as pre-registered: entity band
`maxEntityChunks` 40→64, plus new heading-topic edges
(`Graph.addHeadingTopicEdges`, `edgeTopic`) linking different-source chunks
that share a rare heading token. Re-ran the frozen benchmark unchanged:

| stratum | graph | matched | lift | wins | added precision |
|---------|-------|---------|------|------|-----------------|
| A densified (n=10) | 0.300 | 0.383 | −0.083 | 0/10 | 0.04 |
| B densified (n=6) | 0.833 | 1.000 | −0.167 | 0/6 | 0.00 |

Densification roughly doubled coverage and nudged graph recall up (0.267→0.300)
but still lost to the budget-matched control on every query, and added
precision stayed near-zero. The graph hop reaches a few more chunks but not the
*relevant* ones: the cross-source relationships a human uses to answer these
queries (e.g. "PHP-migration hazard" ↔ "SQL-injection class") are semantic,
whereas the available offline edges are lexical-token, structural, and
entity-overlap. In this corpus and evaluation those heuristics were
insufficient to recover the needed semantic links for a default-on benefit —
this is a result about these heuristics on this corpus, not a proof that graph
expansion cannot help a future reranked/agentic pipeline that filters neighbors
with a model.

### Verdict → DEMOTE to opt-in

Under the frozen decision rules: lift ≤ 0 on Stratum A even after the allowed
densification, and Stratum B shows the graph strictly under the control by
0.167 (worse-than-guardrail noise on simple queries). This is the REMOVE/
disable-by-default branch. Action taken: `graph_expand` is now **off by
default**, registered only behind `--graph-expand` (CLI) / `Options.GraphExpand`
(server). The tool and its offline graph remain for corpora with strong
cross-document structure and for the agentic path where a reasoning model
filters the added candidates live (previously observed with gpt-5.5), but it is
no longer in the default tool surface.

## Honest limitations

- n is small (10 + 6); per-case variance was +-0.127 in the prior broad eval,
  so borderline results must be read as DEMOTE, not KEEP.
- The same author wrote queries and labels; justifications are recorded so a
  second reviewer can audit them.
- Real-traffic prevalence of graph-salient queries is unmeasured (no query
  logs exist). A benchmark win here proves capability, not demand.
