/*
entities.go adds entity edges to the knowledge graph. Entity edges connect
chunks that mention the same salient term ACROSS sources — the relationship that
lets agentic-RAG reason over facts scattered through different documents, which
neither similarity search nor within-source structure can reach.

Extraction is a cheap OFFLINE heuristic (backticked code identifiers + capitalized
multi-word proper-noun phrases), so ingestion needs no model and stays
deterministic. A model-based extractor is a possible later enrichment; the
heuristic is deliberately conservative and capped so entity edges add signal, not
noise.
*/
package rag

import (
	"regexp"
	"sort"
	"strings"
)

var (
	/* Backticked identifiers, e.g. `rag_search`, `Service.Search`. */
	codeSpanRE = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_:.]{2,63})`")
	/* Capitalized multi-word proper nouns, e.g. "Bearer Token", "Knowledge Graph".
	   Requiring at least two words avoids the sentence-start / single-word noise. */
	properNounRE = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+(?: [A-Z][A-Za-z0-9]+){1,3}\b`)
	/* All-caps acronyms, e.g. SQL, XSS, CSRF, PDO, OWASP — the domain terms that
	   link security and code docs across sources and that the proper-noun and
	   code-span heuristics both miss. Anchored to a letter so "2024" is excluded;
	   the word boundary trims hyphenated ids (CVE-2024 -> CVE). */
	acronymRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,7}\b`)
)

/*
genericAcronyms are protocol/format acronyms so ubiquitous in a tech corpus that
linking on them creates hubs, not signal. Security-relevant acronyms (SQL, XSS,
CSRF, SSRF, RCE, PDO, OWASP, CVE, …) are deliberately NOT listed — they are the
edges worth having.
*/
var genericAcronyms = map[string]struct{}{
	"api": {}, "http": {}, "https": {}, "json": {}, "xml": {}, "html": {},
	"css": {}, "url": {}, "uri": {}, "rest": {}, "sdk": {}, "cli": {},
	"dns": {}, "tcp": {}, "udp": {}, "ssl": {}, "tls": {}, "yaml": {}, "csv": {},
}

const (
	maxEntitiesPerChunk = 16
	/* An entity in only one chunk relates nothing; one in very many is a stop-word
	   masquerading as an entity. Edges are built only for entities in this band.
	   The upper bound is generous enough for recurring acronyms yet still skips
	   ubiquitous terms; the denylist above trims the worst offenders first. */
	minEntityChunks = 2
	maxEntityChunks = 40
)

/*
extractEntities returns a deterministic, deduped, lowercased set of candidate
entity keys from a chunk's text. It is intentionally cheap and offline.
*/
func extractEntities(text string) []string {
	set := map[string]struct{}{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < 3 {
			return
		}
		set[strings.ToLower(s)] = struct{}{}
	}
	for _, m := range codeSpanRE.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range properNounRE.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range acronymRE.FindAllString(text, -1) {
		if _, generic := genericAcronyms[strings.ToLower(m)]; generic {
			continue
		}
		add(m)
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > maxEntitiesPerChunk {
		out = out[:maxEntitiesPerChunk]
	}
	return out
}

/*
addEntityEdges links chunks that share a salient entity across the whole corpus.
It runs after structural edges and is deterministic: entities and their chunk
lists are sorted before edges are added, and addEdge dedups. Ubiquitous or
unique entities are skipped so edges stay meaningful.
*/
func (g *Graph) addEntityEdges() {
	ids := make([]string, 0, len(g.chunks))
	for id := range g.chunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entityChunks := map[string][]string{}
	for _, id := range ids {
		for _, e := range extractEntities(g.chunks[id].Text) {
			entityChunks[e] = append(entityChunks[e], id)
		}
	}

	entities := make([]string, 0, len(entityChunks))
	for e := range entityChunks {
		entities = append(entities, e)
	}
	sort.Strings(entities)

	for _, e := range entities {
		members := entityChunks[e]
		if len(members) < minEntityChunks || len(members) > maxEntityChunks {
			continue
		}
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				g.addEdge(members[i], members[j], edgeEntity)
				g.addEdge(members[j], members[i], edgeEntity)
			}
		}
	}
}
