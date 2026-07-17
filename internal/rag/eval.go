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
	Query             string  `json:"query"`
	BaseRecall        float64 `json:"base_recall"`
	GraphRecall       float64 `json:"graph_recall"`
	BaseMatchedRecall float64 `json:"base_matched_recall"`
	GraphAdded        int     `json:"graph_added"`
	GraphAddedHits    int     `json:"graph_added_hits"`
}

/*
EvalReport aggregates a retrieval evaluation run. BaseMatchedRecall is the budget
control: plain search widened to the SAME candidate count the graph hop produced
(k + graph_added), so graph lift over base_matched is real signal, not just more
candidates. GraphAddedPrecision is the fraction of graph-added chunks that were
actually relevant — a low value means the graph added noise, not evidence.
*/
type EvalReport struct {
	K                   int          `json:"k"`
	Cases               int          `json:"cases"`
	BaseRecall          float64      `json:"base_recall_mean"`
	GraphRecall         float64      `json:"graph_recall_mean"`
	BaseMatchedRecall   float64      `json:"base_matched_recall_mean"`
	GraphAdded          float64      `json:"graph_added_mean"`
	GraphAddedPrecision float64      `json:"graph_added_precision"`
	PerCase             []CaseResult `json:"per_case"`
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
	var baseSum, graphSum, matchedSum float64
	var addedSum, addedHitsSum int
	for _, c := range cases {
		relevant := make(map[string]bool, len(c.RelevantChunkIDs))
		for _, id := range c.RelevantChunkIDs {
			relevant[id] = true
		}

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
		added, addedHits := 0, 0
		for _, e := range expanded {
			if graph[e.Chunk.ID] {
				continue
			}
			graph[e.Chunk.ID] = true
			added++
			if relevant[e.Chunk.ID] {
				addedHits++
			}
		}

		/* Budget-matched control: plain search at the same candidate count the
		   graph produced, so lift over this is genuine, not just more candidates. */
		matched := base
		if added > 0 {
			wideHits, err := s.Search(ctx, c.Query, SearchOpts{K: k + added})
			if err != nil {
				return EvalReport{}, err
			}
			matched = make(map[string]bool, len(wideHits))
			for _, h := range wideHits {
				matched[h.Chunk.ID] = true
			}
		}

		br := recallAt(base, c.RelevantChunkIDs)
		gr := recallAt(graph, c.RelevantChunkIDs)
		bmr := recallAt(matched, c.RelevantChunkIDs)
		baseSum += br
		graphSum += gr
		matchedSum += bmr
		addedSum += added
		addedHitsSum += addedHits
		rep.PerCase = append(rep.PerCase, CaseResult{
			Query: c.Query, BaseRecall: br, GraphRecall: gr, BaseMatchedRecall: bmr,
			GraphAdded: added, GraphAddedHits: addedHits,
		})
	}
	if n := len(cases); n > 0 {
		rep.BaseRecall = baseSum / float64(n)
		rep.GraphRecall = graphSum / float64(n)
		rep.BaseMatchedRecall = matchedSum / float64(n)
		rep.GraphAdded = float64(addedSum) / float64(n)
	}
	if addedSum > 0 {
		rep.GraphAddedPrecision = float64(addedHitsSum) / float64(addedSum)
	}
	return rep, nil
}
