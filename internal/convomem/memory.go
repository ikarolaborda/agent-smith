package convomem

import (
	"context"
	"fmt"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
Memory ties an embedder to a Store: it embeds turns on the way in and queries on
the way out, so callers work in text and never touch vectors. It is the seam the
agent uses to persist a conversation and recall the relevant slice on the next
turn, keeping prompts small while history grows without bound.
*/
type Memory struct {
	Store    *Store
	Embedder llm.Embedder
}

/*
Remember embeds content and stores it as one turn for profile. An empty content
is a no-op. Embedding failure is returned so callers can decide whether to treat
memory as best-effort (log and continue) or hard (fail the turn).
*/
func (m *Memory) Remember(ctx context.Context, profile, role, content string) error {
	if m == nil || m.Store == nil || m.Embedder == nil {
		return fmt.Errorf("convomem: memory not configured")
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	vecs, err := m.Embedder.EmbedTexts(ctx, []string{content})
	if err != nil {
		return fmt.Errorf("convomem: embed turn: %w", err)
	}
	if len(vecs) != 1 {
		return fmt.Errorf("convomem: embedder returned %d rows for 1 input", len(vecs))
	}
	_, err = m.Store.Add(ctx, profile, role, content, vecs[0])
	return err
}

/*
Recall embeds query and returns the k most relevant stored turns for profile. It
is the retrieval half; callers format the result into the prompt (see RecallBlock).
*/
func (m *Memory) Recall(ctx context.Context, profile, query string, k int) ([]Turn, error) {
	if m == nil || m.Store == nil || m.Embedder == nil {
		return nil, fmt.Errorf("convomem: memory not configured")
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	vecs, err := m.Embedder.EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("convomem: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("convomem: embedder returned %d rows for 1 input", len(vecs))
	}
	return m.Store.Recall(ctx, profile, vecs[0], k)
}

/*
RecallBlock recalls and renders the relevant memory as a labelled, untrusted-by-
default context section ready to prepend to the system prompt — the same
convention as the RAG/web sections. It returns "" when nothing relevant is found
or memory is unconfigured, so it is safe to always call. minScore filters weak
matches (cosine below it are dropped) so an off-topic turn does not inject noise.
*/
func (m *Memory) RecallBlock(ctx context.Context, profile, query string, k int, minScore float64) string {
	turns, err := m.Recall(ctx, profile, query, k)
	if err != nil || len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Relevant earlier conversation (recalled memory — may be incomplete)\n")
	kept := 0
	for _, t := range turns {
		if t.Score < minScore {
			continue
		}
		fmt.Fprintf(&b, "- (%s, score %.2f) %s\n", t.Role, t.Score, oneLine(t.Content, 500))
		kept++
	}
	if kept == 0 {
		return ""
	}
	return b.String()
}

/* oneLine collapses whitespace and truncates so a recalled turn stays compact in the prompt. */
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
