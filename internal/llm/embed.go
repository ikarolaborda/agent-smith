package llm

import "context"

/*
Embedder is the optional companion interface to Provider for embedding text.
Not every provider supports embeddings (Anthropic, for example, does not), so
Embedder is intentionally a separate seam. The agent.RAG service holds an
Embedder, not a Provider.

EmbedTexts returns one row per input string. All rows have the same dimension.
Identity returns a stable identifier such as "openai:text-embedding-3-small"
or "ollama:nomic-embed-text" that is persisted in collection metadata so a
later search can verify the query is being embedded by the same model.
*/
type Embedder interface {
	Identity() string
	Dim() int
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}
