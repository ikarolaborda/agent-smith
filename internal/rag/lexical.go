package rag

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	shippeddocs "github.com/ikarolaborda/agent-smith/docs"
)

/*
builtinCollections is the set of embedded docs/ directories that are knowledge
corpora rather than project documentation. The future names are intentionally
present now: docs.Corpus uses a tolerant wildcard, so adding either directory
and its first Markdown file automatically makes it searchable.
*/
var builtinCollections = map[string]struct{}{
	"architectural-patterns": {},
	"computer-networks":      {},
	"cs-fundamentals":        {},
	"cybersecurity":          {},
	"go-lang":                {},
	"laravel":                {},
	"native-php":             {},
	"nestjs":                 {},
	"php":                    {},
	"software-engineering":   {},
	"tailwind-css":           {},
}

/* lexicalTokenRE preserves code and security identifiers while tokenizing prose. */
var lexicalTokenRE = regexp.MustCompile(`[\p{L}\p{N}]+(?:[._:/%+-][\p{L}\p{N}]+)*`)

/* Common prose terms add noise without helping identify a technical source. */
var lexicalStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {},
	"anything": {},
	"be":       {}, "by": {}, "can": {}, "do": {}, "does": {}, "for": {},
	"from": {}, "how": {}, "i": {}, "in": {}, "is": {}, "it": {},
	"hello": {}, "hi": {},
	"of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "this": {},
	"there": {}, "to": {}, "use": {}, "using": {}, "what": {}, "when": {}, "with": {},
}

/* lexicalDocument is the immutable indexed representation of one Markdown chunk. */
type lexicalDocument struct {
	result SearchResult
	terms  map[string]int
	length int
}

/*
LexicalIndex is a compact in-memory BM25-style index over the corpus embedded in
the binary. It is immutable after construction and therefore safe for concurrent
searches without locks.
*/
type LexicalIndex struct {
	documents []lexicalDocument
	docFreq   map[string]int
	avgLength float64
}

/* loadBuiltinLexicalIndex chunks and indexes every allowlisted embedded document. */
func loadBuiltinLexicalIndex() (*LexicalIndex, error) {
	type source struct {
		collection string
		path       string
		body       []byte
	}
	var sources []source
	err := fs.WalkDir(shippeddocs.Corpus, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(path.Ext(name), ".md") {
			return nil
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return nil
		}
		collection := parts[0]
		if _, ok := builtinCollections[collection]; !ok {
			return nil
		}
		body, err := shippeddocs.Corpus.ReadFile(name)
		if err != nil {
			return err
		}
		sources = append(sources, source{collection: collection, path: name, body: body})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rag: load embedded corpus: %w", err)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].path < sources[j].path })

	var chunks []SearchResult
	for _, source := range sources {
		text := string(NormalizeMarkdown(source.body))
		for _, chunk := range SplitMarkdown("docs/"+source.path, text, DefaultChunkOptions) {
			chunk.Vector = nil
			chunks = append(chunks, SearchResult{Collection: source.collection, Chunk: chunk})
		}
	}
	return newLexicalIndex(chunks), nil
}

/* newLexicalIndex builds document frequencies and average document length. */
func newLexicalIndex(chunks []SearchResult) *LexicalIndex {
	idx := &LexicalIndex{docFreq: map[string]int{}}
	var totalLength int
	for _, result := range chunks {
		terms := lexicalTermFrequency(chunkEmbedInput(result.Chunk))
		if len(terms) == 0 {
			continue
		}
		length := 0
		for term, count := range terms {
			length += count
			idx.docFreq[term]++
		}
		idx.documents = append(idx.documents, lexicalDocument{result: result, terms: terms, length: length})
		totalLength += length
	}
	if len(idx.documents) > 0 {
		idx.avgLength = float64(totalLength) / float64(len(idx.documents))
	}
	return idx
}

/*
Search returns deterministic BM25-style matches. Scores are mapped into [0,1]
so they remain usable by the existing confidence-band and hybrid merge logic;
raw BM25 remains the primary ordering key.
*/
func (l *LexicalIndex) Search(query string, filter []string, k int) []SearchResult {
	if l == nil || len(l.documents) == 0 || k <= 0 {
		return nil
	}
	queryTerms := uniqueLexicalTerms(query)
	if len(queryTerms) == 0 {
		return nil
	}
	requireStructuredMatch := hasStructuredLexicalTerm(query)
	allowed := makeStringSet(filter)
	type scored struct {
		result   SearchResult
		raw      float64
		coverage float64
		exact    int
	}
	pool := make([]scored, 0, len(l.documents))
	const (
		k1 = 1.2
		b  = 0.75
	)
	for _, document := range l.documents {
		if len(allowed) > 0 {
			if _, ok := allowed[document.result.Collection]; !ok {
				continue
			}
		}
		var raw float64
		var matched, exact int
		for _, term := range queryTerms {
			tf := document.terms[term]
			if tf == 0 {
				continue
			}
			matched++
			df := l.docFreq[term]
			idf := math.Log(1 + (float64(len(l.documents)-df)+0.5)/(float64(df)+0.5))
			norm := 1 - b
			if l.avgLength > 0 {
				norm += b * float64(document.length) / l.avgLength
			}
			termScore := idf * (float64(tf) * (k1 + 1)) / (float64(tf) + k1*norm)
			if isStructuredLexicalTerm(term) {
				termScore *= 1.35
				exact++
			}
			raw += termScore
		}
		/*
			A dotted/path/CVE-style query is an exact-identifier lookup first.
			Component tokens improve ranking after that identifier matches, but
			they must not turn an unknown identifier into broad prose matches.
		*/
		if raw == 0 || (requireStructuredMatch && exact == 0) {
			continue
		}
		coverage := float64(matched) / float64(len(queryTerms))
		pool = append(pool, scored{result: document.result, raw: raw, coverage: coverage, exact: exact})
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].raw != pool[j].raw {
			return pool[i].raw > pool[j].raw
		}
		return searchResultLess(pool[i].result, pool[j].result)
	})
	if len(pool) > k {
		pool = pool[:k]
	}
	results := make([]SearchResult, 0, len(pool))
	for _, hit := range pool {
		confidence := 0.25 + 0.45*(1-math.Exp(-hit.raw/4)) + 0.20*hit.coverage
		if hit.exact > 0 {
			confidence += 0.05
		}
		if confidence > 0.95 {
			confidence = 0.95
		}
		hit.result.Score = float32(confidence)
		results = append(results, hit.result)
	}
	return results
}

/* lexicalTermFrequency expands structured identifiers with their component words. */
func lexicalTermFrequency(text string) map[string]int {
	frequency := map[string]int{}
	for _, term := range lexicalTerms(text) {
		frequency[term]++
	}
	return frequency
}

/* uniqueLexicalTerms retains query order while preventing repeated-term bias. */
func uniqueLexicalTerms(text string) []string {
	seen := map[string]struct{}{}
	var terms []string
	for _, term := range lexicalTerms(text) {
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

/* lexicalTerms tokenizes prose, CVEs, package paths, and dotted code identifiers. */
func lexicalTerms(text string) []string {
	matches := lexicalTokenRE.FindAllString(strings.ToLower(text), -1)
	terms := make([]string, 0, len(matches)*2)
	for _, match := range matches {
		if !isLexicalStopWord(match) {
			terms = append(terms, match)
		}
		if !isStructuredLexicalTerm(match) {
			continue
		}
		for _, part := range strings.FieldsFunc(match, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if part != match && !isLexicalStopWord(part) {
				terms = append(terms, part)
			}
		}
	}
	return terms
}

func isLexicalStopWord(term string) bool {
	if len([]rune(term)) < 2 {
		return true
	}
	_, ok := lexicalStopWords[term]
	return ok
}

func isStructuredLexicalTerm(term string) bool {
	return strings.ContainsAny(term, "._:/%+-")
}

func hasStructuredLexicalTerm(text string) bool {
	for _, match := range lexicalTokenRE.FindAllString(strings.ToLower(text), -1) {
		if isStructuredLexicalTerm(match) {
			return true
		}
	}
	return false
}

func makeStringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

/* searchResultLess is the deterministic tie-break shared by lexical and hybrid ranking. */
func searchResultLess(a, b SearchResult) bool {
	if a.Collection != b.Collection {
		return a.Collection < b.Collection
	}
	if a.Chunk.Source != b.Chunk.Source {
		return a.Chunk.Source < b.Chunk.Source
	}
	if a.Chunk.Heading != b.Chunk.Heading {
		return a.Chunk.Heading < b.Chunk.Heading
	}
	return a.Chunk.ID < b.Chunk.ID
}
