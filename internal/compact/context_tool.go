package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

/*
ContextSearchTool exposes an Index to the agent loop as the "context_search"
tool. It is created per turn over the current oversized input's index, so the
agent can retrieve the exact original passages that were summarized away. It
implements the tools.Tool interface structurally (no import of the tools package,
avoiding a dependency cycle with the agent).
*/
type ContextSearchTool struct {
	Index *Index
}

/* NewContextSearchTool binds the tool to one turn's index. */
func NewContextSearchTool(index *Index) *ContextSearchTool {
	return &ContextSearchTool{Index: index}
}

func (t *ContextSearchTool) Name() string { return "context_search" }

func (t *ContextSearchTool) Description() string {
	return "Retrieve verbatim passages from the user's large input that was summarized to fit the context window. Use it whenever you need exact wording, names, numbers, code, or details the summary may have omitted."
}

func (t *ContextSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to look for in the original input"},"k":{"type":"integer","description":"Maximum passages to return (default 3)"}},"required":["query"]}`)
}

func (t *ContextSearchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("context_search: bad arguments: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("context_search: query is required")
	}
	if t.Index == nil || t.Index.Len() == 0 {
		return "No indexed input is available for this turn.", nil
	}
	hits := t.Index.Search(in.Query, in.K)
	if len(hits) == 0 {
		return "No matching passages found in the original input.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d passage(s) from the original input, most relevant first:\n", len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "\n[chunk %d]\n%s\n", h.Ordinal, h.Text)
	}
	return strings.TrimSpace(b.String()), nil
}
