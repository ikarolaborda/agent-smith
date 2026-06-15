/*
Package context7 implements a small, in-process client for the Context7
documentation API (https://context7.com). It mirrors the role of internal/web:
a single method that returns short, prompt-ready text the agent injects into the
system prompt as authoritative library documentation, so offline models answer
with current, version-specific facts instead of stale parametric knowledge.

The flow is the documented two-step: resolve a free-form query to a Context7
library id, then fetch that library's documentation as plain text. Every call is
best-effort — any network, auth, or parse failure returns a clean error so the
caller can simply skip the augmentation rather than fail the chat. All output is
length-capped before it leaves the package so prompt bloat stays bounded.
*/
package context7

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

/*
Docs is one resolved documentation payload ready for prompt rendering. Library
is the Context7 id the text was fetched for (e.g. "/vercel/next.js"); Text is
the length-capped documentation body.
*/
type Docs struct {
	Library string
	Text    string
}

/*
Provider is the seam consumed by the RAG service. It returns documentation for a
free-form user query, or an error when nothing usable is available. Mirrors
web.Searcher so the augmentation layer treats both sources uniformly.
*/
type Provider interface {
	LibraryDocs(ctx context.Context, query string) (Docs, error)
}

/* DefaultBaseURL is the Context7 v2 REST root. Overridable so a path change needs no recompile. */
const DefaultBaseURL = "https://context7.com/api/v2"

/* Endpoint paths under BaseURL. The resolve step ranks libraries; the context step returns docs. */
const (
	searchPath  = "/libs/search"
	contextPath = "/context"
)

/* DefaultTimeout caps the whole resolve+fetch round so a slow API never stalls a chat turn. */
const DefaultTimeout = 6 * time.Second

/* DefaultMaxTokens bounds how much documentation Context7 returns per query. */
const DefaultMaxTokens = 1200

/* MaxDocsBytes caps the documentation section emitted into the prompt. */
const MaxDocsBytes = 4000

/* DefaultCacheTTL keeps resolved docs hot across turns; library docs change slowly. */
const DefaultCacheTTL = 30 * time.Minute

/* backendName tags this implementation in cache keys and logs. */
const backendName = "context7"

/* ErrNoLibrary means the resolve step found no confident library match for the query. */
var ErrNoLibrary = errors.New("context7: no matching library")

/* ErrNoAPIKey means the client was constructed without a key; the caller should not enable context7. */
var ErrNoAPIKey = errors.New("context7: missing api key")

/*
Client is the default Provider. It pairs an HTTP client with a small TTL cache.
APIKey is sent as a Bearer token. BaseURL defaults to the Context7 v2 root.
*/
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	APIKey    string
	MaxTokens int

	mu    sync.Mutex
	cache map[string]docsEntry
	ttl   time.Duration
}

type docsEntry struct {
	docs    Docs
	expires time.Time
}

/*
New constructs a Client. A blank apiKey yields a client whose LibraryDocs always
errors with ErrNoAPIKey, so wiring code can construct unconditionally and let the
presence of the key decide whether the feature is active.
*/
func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		HTTP:      &http.Client{Timeout: DefaultTimeout},
		BaseURL:   strings.TrimRight(baseURL, "/"),
		APIKey:    strings.TrimSpace(apiKey),
		MaxTokens: DefaultMaxTokens,
		cache:     map[string]docsEntry{},
		ttl:       DefaultCacheTTL,
	}
}

/*
LibraryDocs resolves query to a library and returns its documentation. It is the
Provider entrypoint. Cache hits skip both round-trips. On any failure it returns
a clean error and the caller skips augmentation.
*/
func (c *Client) LibraryDocs(ctx context.Context, query string) (Docs, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Docs{}, errors.New("context7: empty query")
	}
	if c.APIKey == "" {
		return Docs{}, ErrNoAPIKey
	}

	if hit, ok := c.cacheGet(query); ok {
		return hit, nil
	}

	libraryID, err := c.resolve(ctx, query)
	if err != nil {
		return Docs{}, err
	}

	text, err := c.fetchDocs(ctx, libraryID, query)
	if err != nil {
		return Docs{}, err
	}
	text = capText(text, MaxDocsBytes)
	if text == "" {
		return Docs{}, ErrNoLibrary
	}

	docs := Docs{Library: libraryID, Text: text}
	c.cachePut(query, docs)
	return docs, nil
}

/* searchResult mirrors the fields we need from a Context7 library match. */
type searchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Name  string `json:"name"`
}

/*
resolve calls the search endpoint and returns the best library id. Context7 has
returned the match list as either a bare array or an object with a "results"
key across API revisions, so both shapes are accepted. The library name passed
to the API doubles as the free-form query; relevance ranking is server-side.
*/
func (c *Client) resolve(ctx context.Context, query string) (string, error) {
	q := url.Values{}
	q.Set("libraryName", query)
	q.Set("query", query)

	body, err := c.get(ctx, searchPath, q)
	if err != nil {
		return "", err
	}

	var arr []searchResult
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		if id := firstID(arr); id != "" {
			return id, nil
		}
	}

	var wrapped struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		if id := firstID(wrapped.Results); id != "" {
			return id, nil
		}
	}

	return "", ErrNoLibrary
}

/* firstID returns the first non-empty library id in the list. */
func firstID(rs []searchResult) string {
	for _, r := range rs {
		if id := strings.TrimSpace(r.ID); id != "" {
			return id
		}
	}
	return ""
}

/* contextSnippet mirrors one documentation object when Context7 replies in JSON. */
type contextSnippet struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

/*
fetchDocs calls the context endpoint for libraryID, requesting plain text. The
endpoint can also answer with a JSON snippet array; when the body looks like
JSON we join the snippet contents, otherwise the body is already prompt-ready
text.
*/
func (c *Client) fetchDocs(ctx context.Context, libraryID, query string) (string, error) {
	q := url.Values{}
	q.Set("libraryId", libraryID)
	q.Set("query", query)
	q.Set("type", "txt")
	if c.MaxTokens > 0 {
		q.Set("tokens", fmt.Sprintf("%d", c.MaxTokens))
	}

	body, err := c.get(ctx, contextPath, q)
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		if joined := joinSnippets(body); joined != "" {
			return joined, nil
		}
	}
	return trimmed, nil
}

/*
joinSnippets extracts and concatenates documentation content when Context7
answered with JSON despite the txt request. It accepts both a bare array and a
{"results":[...]} wrapper, and returns "" when nothing parseable is present.
*/
func joinSnippets(body []byte) string {
	var arr []contextSnippet
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
		var wrapped struct {
			Results []contextSnippet `json:"results"`
		}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			return ""
		}
		arr = wrapped.Results
	}

	var b strings.Builder
	for _, s := range arr {
		content := strings.TrimSpace(s.Content)
		if content == "" {
			continue
		}
		if title := strings.TrimSpace(s.Title); title != "" {
			b.WriteString("### ")
			b.WriteString(title)
			b.WriteString("\n")
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

/* get performs an authenticated GET against BaseURL+path and returns the body, capped at 1 MiB. */
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	endpoint := c.BaseURL + path
	if enc := q.Encode(); enc != "" {
		endpoint += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("context7: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json, text/plain")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context7: http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("context7: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

/*
capText trims text to at most maxBytes, cutting back to the last newline within
the budget so the injected block never ends mid-line. Returns "" for blank input.
*/
func capText(text string, maxBytes int) string {
	text = strings.TrimSpace(text)
	if len(text) <= maxBytes {
		return text
	}
	cut := text[:maxBytes]
	if i := strings.LastIndexByte(cut, '\n'); i > maxBytes/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

func (c *Client) cacheGet(query string) (Docs, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[cacheKey(query)]
	if !ok || time.Now().After(e.expires) {
		return Docs{}, false
	}
	return e.docs, true
}

func (c *Client) cachePut(query string, docs Docs) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[cacheKey(query)] = docsEntry{docs: docs, expires: time.Now().Add(c.ttl)}
}

/* cacheKey normalizes a query so trivial whitespace/case differences share an entry. */
func cacheKey(query string) string {
	return backendName + ":" + strings.ToLower(strings.Join(strings.Fields(query), " "))
}
