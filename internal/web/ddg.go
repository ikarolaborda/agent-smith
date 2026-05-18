package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

/* DDGEndpoint is the DuckDuckGo lite HTML endpoint we scrape. */
const DDGEndpoint = "https://lite.duckduckgo.com/lite/"

/* DDGUserAgent is the desktop UA string we send so DDG returns the lite layout consistently. */
const DDGUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

/* DDGTimeout caps the HTTP round-trip so an unreachable network does not stall the chat handler. */
const DDGTimeout = 4 * time.Second

/* DDGBackendName tags this implementation in cache keys and logs. */
const DDGBackendName = "ddg"

/*
DDGSearcher is the default Searcher. It pairs the in-memory cache with the
lite.duckduckgo.com HTML endpoint and the sanitization layer; nothing
unsanitized ever leaves Search.
*/
type DDGSearcher struct {
	HTTP  *http.Client
	Cache *Cache
}

/* NewDDGSearcher constructs a DDGSearcher with sensible defaults. */
func NewDDGSearcher() *DDGSearcher {
	return &DDGSearcher{
		HTTP:  &http.Client{Timeout: DDGTimeout},
		Cache: NewCache(DefaultCacheTTL, DefaultCacheCapacity),
	}
}

/*
Search returns up to k sanitized results for query. Cache hits are returned
immediately; misses do one HTTP call against DDGEndpoint, parse the lite
layout, sanitize each row, then cache and return.
*/
func (s *DDGSearcher) Search(ctx context.Context, query string, k int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("web: empty query")
	}
	if k <= 0 || k > MaxResultsCap {
		k = MaxResultsCap
	}
	if s.Cache != nil {
		if hit, ok := s.Cache.Get(DDGBackendName, k, query); ok {
			return hit, nil
		}
	}

	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "us-en")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DDGEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("web: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", DDGUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("web: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("web: read body: %w", err)
	}
	results := parseDDGLite(string(body), k)
	if len(results) == 0 {
		return nil, ErrNoResults
	}
	if s.Cache != nil {
		s.Cache.Put(DDGBackendName, k, query, results)
	}
	return results, nil
}

/*
parseDDGLite walks the DuckDuckGo lite HTML and extracts up to k results.
The lite layout uses repeating <tr> rows: result-link anchor in one row,
then optional <td class="result-snippet"> in a later row of the same table.
We capture them in document order and pair them by index.
*/
func parseDDGLite(body string, k int) []Result {
	doc, err := xhtml.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var titles []string
	var urls []string
	var snippets []string
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "a":
				if hasClass(n, "result-link") {
					href := attr(n, "href")
					title := textOf(n)
					if u := sanitizeURL(href, MaxURLChars); u != "" {
						urls = append(urls, u)
						titles = append(titles, sanitizeText(title, MaxTitleChars))
					}
				}
			case "td":
				if hasClass(n, "result-snippet") {
					snippets = append(snippets, sanitizeSnippet(textOf(n), MaxSnippetChars))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	if len(titles) > k {
		titles = titles[:k]
		urls = urls[:k]
	}
	out := make([]Result, 0, len(titles))
	for i := range titles {
		var snip string
		if i < len(snippets) {
			snip = snippets[i]
		}
		if titles[i] == "" || urls[i] == "" {
			continue
		}
		out = append(out, Result{
			Title:   titles[i],
			URL:     urls[i],
			Snippet: snip,
		})
	}
	return out
}

/* hasClass returns true when node n has class containing the named token. */
func hasClass(n *xhtml.Node, class string) bool {
	cls := attr(n, "class")
	if cls == "" {
		return false
	}
	for _, c := range strings.Fields(cls) {
		if c == class {
			return true
		}
	}
	return false
}

/* attr returns the value of a named attribute on n, or "". */
func attr(n *xhtml.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

/* textOf concatenates the textual descendants of n. */
func textOf(n *xhtml.Node) string {
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk = func(x *xhtml.Node) {
		if x.Type == xhtml.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
