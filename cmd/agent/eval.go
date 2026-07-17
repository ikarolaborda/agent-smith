package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/rag"
)

/*
runEvalRAG runs the offline retrieval-evaluation harness: it loads a fixtures
file of labelled queries and reports recall@k of plain search versus
graph-widened search, so the knowledge graph's contribution can be measured
without a model or network. It reuses the same RAG service the server and CLI
build, and prints a JSON report to stdout.
*/
func runEvalRAG(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	raw, err := os.ReadFile(f.evalRAG)
	if err != nil {
		return fmt.Errorf("read eval fixtures %q: %w", f.evalRAG, err)
	}
	var cases []rag.EvalCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		return fmt.Errorf("parse eval fixtures %q (want a JSON array of {query, relevant_chunk_ids}): %w", f.evalRAG, err)
	}
	if len(cases) == 0 {
		return fmt.Errorf("eval fixtures %q contain no cases", f.evalRAG)
	}

	ragSvc, err := buildRAG(cfg, &f, logger)
	if err != nil {
		return fmt.Errorf("initialize knowledge layer for eval: %w", err)
	}

	k := f.ragMaxChunks
	if k <= 0 {
		k = 5
	}
	report, err := ragSvc.EvalRetrieval(ctx, cases, k)
	if err != nil {
		return fmt.Errorf("run retrieval eval: %w", err)
	}
	return writeJSON(os.Stdout, report)
}
