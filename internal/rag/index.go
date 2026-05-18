package rag

import (
	"errors"
	"math"
	"sort"
	"sync"
)

/* SearchResult is one ranked hit from the in-memory index. */
type SearchResult struct {
	Collection string  `json:"collection"`
	Chunk      Chunk   `json:"chunk"`
	Score      float32 `json:"score"`
}

/*
Index is the in-memory cosine-search structure built from a slice of
Collections. It groups collections by embedder identity so a query embedded
by one model only competes against chunks embedded by the same model.
*/
type Index struct {
	mu          sync.RWMutex
	collections map[string]*Collection
}

/* NewIndex constructs an empty Index. */
func NewIndex() *Index {
	return &Index{collections: map[string]*Collection{}}
}

/*
Replace replaces a collection in the index by name. Used after ingest or on
startup load.
*/
func (i *Index) Replace(c *Collection) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.collections[c.Name] = c
}

/* Names returns the sorted list of collection names currently held. */
func (i *Index) Names() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]string, 0, len(i.collections))
	for n := range i.collections {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

/* Get returns a snapshot of one collection or nil if absent. */
func (i *Index) Get(name string) *Collection {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.collections[name]
}

/*
Search runs cosine TopK over the listed collections (or all, if filter is
empty) restricted to the embedder identity of the query. K is the top-K per
search; threshold is the minimum cosine similarity to return; perCollectionCap
limits how many results any single collection can contribute (to avoid one
corpus monopolizing the result set).
*/
func (i *Index) Search(query []float32, embedderID string, filter []string, k, perCollectionCap int, threshold float32) ([]SearchResult, error) {
	if len(query) == 0 {
		return nil, errors.New("rag: empty query vector")
	}
	if k <= 0 {
		k = 4
	}
	if perCollectionCap <= 0 {
		perCollectionCap = k
	}
	queryNorm := normSquared(query)
	if queryNorm == 0 {
		return nil, errors.New("rag: zero-norm query vector")
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	targets := filter
	if len(targets) == 0 {
		targets = make([]string, 0, len(i.collections))
		for n := range i.collections {
			targets = append(targets, n)
		}
		sort.Strings(targets)
	}

	var all []SearchResult
	for _, name := range targets {
		c, ok := i.collections[name]
		if !ok {
			continue
		}
		if c.EmbedderID != embedderID {
			continue
		}
		if c.Dim != len(query) {
			continue
		}
		hits := topKInCollection(c, query, queryNorm, perCollectionCap, threshold)
		all = append(all, hits...)
	}

	sort.SliceStable(all, func(a, b int) bool { return all[a].Score > all[b].Score })
	if len(all) > k {
		all = all[:k]
	}
	return all, nil
}

/* topKInCollection scores every chunk in a collection and returns the top N. */
func topKInCollection(c *Collection, query []float32, queryNorm float32, cap int, threshold float32) []SearchResult {
	type scored struct {
		idx   int
		score float32
	}
	scoredAll := make([]scored, 0, len(c.Chunks))
	for idx, ch := range c.Chunks {
		if len(ch.Vector) != len(query) {
			continue
		}
		s := cosine(query, ch.Vector, queryNorm)
		if s < threshold {
			continue
		}
		scoredAll = append(scoredAll, scored{idx: idx, score: s})
	}
	sort.SliceStable(scoredAll, func(a, b int) bool { return scoredAll[a].score > scoredAll[b].score })
	if len(scoredAll) > cap {
		scoredAll = scoredAll[:cap]
	}
	out := make([]SearchResult, 0, len(scoredAll))
	for _, s := range scoredAll {
		out = append(out, SearchResult{
			Collection: c.Name,
			Chunk:      c.Chunks[s.idx],
			Score:      s.score,
		})
	}
	return out
}

/* cosine computes the cosine similarity of two equal-length vectors. */
func cosine(a, b []float32, aNormSq float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, bNormSq float32
	for i := range a {
		dot += a[i] * b[i]
		bNormSq += b[i] * b[i]
	}
	if aNormSq == 0 || bNormSq == 0 {
		return 0
	}
	return dot / float32(math.Sqrt(float64(aNormSq))*math.Sqrt(float64(bNormSq)))
}

/* normSquared returns the squared L2 norm of v. */
func normSquared(v []float32) float32 {
	var s float32
	for _, x := range v {
		s += x * x
	}
	return s
}
