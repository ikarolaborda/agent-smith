/*
Package web implements the pre-chat web-grounding gate. A Searcher returns
short third-party snippets for a free-form user query so the agent can
inject them into the system prompt as untrusted hints rather than relying on
parametric model knowledge. The package contains a single in-process
implementation (DuckDuckGo lite) plus a TTL cache and an aggressive
sanitization layer. All sanitization happens BEFORE caching so the cache
only ever holds prompt-ready strings.
*/
package web

import (
	"context"
	"errors"
)

/* Result is one sanitized search hit ready for prompt rendering. */
type Result struct {
	Title   string
	URL     string
	Snippet string
}

/*
Searcher returns at most k results for query. Implementations must respect
ctx for cancellation and timeout; on any failure (network, parse, rate
limit, zero results) they must return a clean error rather than panic.
*/
type Searcher interface {
	Search(ctx context.Context, query string, k int) ([]Result, error)
}

/* Maximum permitted result count to keep prompt bloat bounded. */
const MaxResultsCap = 5

/* Per-field length caps applied during sanitization. */
const (
	MaxTitleChars   = 160
	MaxURLChars     = 300
	MaxSnippetChars = 400
)

/* MaxWebSectionBytes caps the entire web-results section emitted into the prompt. */
const MaxWebSectionBytes = 3000

/* ErrNoResults is returned by Search when the backend produced zero usable hits. */
var ErrNoResults = errors.New("web: no results")
