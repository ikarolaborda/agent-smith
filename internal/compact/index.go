package compact

import (
	"math"
	"regexp"
	"strings"
)

/*
Excerpt is one retrieval hit: the chunk's position in the original input, its
verbatim text, and a relevance score. Ordinal lets a caller cite where in the
input the passage came from.
*/
type Excerpt struct {
	Ordinal int     `json:"ordinal"`
	Text    string  `json:"text"`
	Score   float64 `json:"score"`
}

/*
Index is an in-memory TF-IDF lexical retriever over the chunks of one oversized
input. It is deliberately dependency-free (no embedder, no network): it exists so
an agent can pull the exact original text back after the bulk was summarized,
which lexical overlap serves well for names, identifiers, and quoted phrases.
*/
type Index struct {
	chunks []string
	tf     []map[string]float64
	idf    map[string]float64
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

func tokenize(s string) []string {
	return wordRe.FindAllString(strings.ToLower(s), -1)
}

/* BuildIndex tokenizes each chunk and precomputes term frequencies and IDF. */
func BuildIndex(chunks []string) *Index {
	idx := &Index{chunks: chunks, tf: make([]map[string]float64, len(chunks)), idf: map[string]float64{}}
	df := map[string]int{}
	for i, c := range chunks {
		counts := map[string]float64{}
		for _, t := range tokenize(c) {
			counts[t]++
		}
		idx.tf[i] = counts
		for t := range counts {
			df[t]++
		}
	}
	n := float64(len(chunks))
	for t, d := range df {
		idx.idf[t] = math.Log(1 + n/float64(d))
	}
	return idx
}

/* Len reports how many chunks the index holds. */
func (i *Index) Len() int {
	if i == nil {
		return 0
	}
	return len(i.chunks)
}

/*
Search returns the top-k chunks by TF-IDF overlap with query, highest first.
Chunks with no overlapping term are omitted, so an off-topic query can return
fewer than k (or zero) results rather than noise.
*/
func (i *Index) Search(query string, k int) []Excerpt {
	if i == nil || len(i.chunks) == 0 {
		return nil
	}
	if k <= 0 {
		k = 3
	}
	qterms := map[string]float64{}
	for _, t := range tokenize(query) {
		qterms[t]++
	}
	out := make([]Excerpt, 0, len(i.chunks))
	for ord, tf := range i.tf {
		var score float64
		for t, qf := range qterms {
			if f, ok := tf[t]; ok {
				w := i.idf[t]
				score += qf * f * w * w
			}
		}
		if score > 0 {
			out = append(out, Excerpt{Ordinal: ord, Text: i.chunks[ord], Score: score})
		}
	}
	sortExcerptsByScore(out)
	if len(out) > k {
		out = out[:k]
	}
	return out
}
