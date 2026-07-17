/*
eval.go is an OFFLINE, deterministic retrieval-evaluation harness. It measures
recall@k of plain vector/lexical search versus the same search widened by one hop
of graph expansion, over a fixture set of (query, relevant chunk ids). This
isolates the knowledge graph's contribution to retrieval from the reasoning
model's behaviour: a full agentic-vs-classic comparison additionally needs a live
tool-capable model, but whether the graph surfaces relevant chunks that top-k
alone misses is answerable here without any model or network.
*/
package rag

import "context"

/* EvalCase is one labelled retrieval example. */
type EvalCase struct {
	Query            string   `json:"query"`
	RelevantChunkIDs []string `json:"relevant_chunk_ids"`
}

/* CaseResult is the per-query recall of each retrieval strategy. */
type CaseResult struct {
	Query       string  `json:"query"`
	BaseRecall  float64 `json:"base_recall"`
	GraphRecall float64 `json:"graph_recall"`
}

/* EvalReport aggregates a retrieval evaluation run. */
type EvalReport struct {
	K           int          `json:"k"`
	Cases       int          `json:"cases"`
	BaseRecall  float64      `json:"base_recall_mean"`
	GraphRecall float64      `json:"graph_recall_mean"`
	PerCase     []CaseResult `json:"per_case"`
}

/*
recallAt returns |retrieved ∩ relevant| / |relevant|. An empty relevant set is a
perfect 1.0 (nothing to miss), so it never drags the mean down.
*/
func recallAt(retrieved map[string]bool, relevant []string) float64 {
	if len(relevant) == 0 {
		return 1.0
	}
	hit := 0
	for _, id := range relevant {
		if retrieved[id] {
			hit++
		}
	}
	return float64(hit) / float64(len(relevant))
}

/*
EvalRetrieval runs each case through plain search and through search widened by
one graph hop, and reports the mean recall of each. It is deterministic and makes
no model call.
*/
func (s *Service) EvalRetrieval(ctx context.Context, cases []EvalCase, k int) (EvalReport, error) {
	rep := EvalReport{K: k, Cases: len(cases)}
	var baseSum, graphSum float64
	for _, c := range cases {
		hits, err := s.Search(ctx, c.Query, SearchOpts{K: k})
		if err != nil {
			return EvalReport{}, err
		}
		base := make(map[string]bool, len(hits))
		seeds := make([]string, 0, len(hits))
		for _, h := range hits {
			base[h.Chunk.ID] = true
			seeds = append(seeds, h.Chunk.ID)
		}

		graph := make(map[string]bool, len(base))
		for id := range base {
			graph[id] = true
		}
		expanded, err := s.ExpandGraph(seeds, 1)
		if err != nil {
			return EvalReport{}, err
		}
		for _, e := range expanded {
			graph[e.Chunk.ID] = true
		}

		br := recallAt(base, c.RelevantChunkIDs)
		gr := recallAt(graph, c.RelevantChunkIDs)
		baseSum += br
		graphSum += gr
		rep.PerCase = append(rep.PerCase, CaseResult{Query: c.Query, BaseRecall: br, GraphRecall: gr})
	}
	if len(cases) > 0 {
		rep.BaseRecall = baseSum / float64(len(cases))
		rep.GraphRecall = graphSum / float64(len(cases))
	}
	return rep, nil
}
