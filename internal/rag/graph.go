/*
graph.go builds a lightweight, deterministic, OFFLINE knowledge graph over the
document corpus — the "Knowledge Graph" leg of agentic-RAG. Nodes are chunks;
edges capture the structural relationships that vector/lexical similarity cannot
see: within-source adjacency (the passage that continues a document) and
shared-heading siblings (passages under the same section). The reasoning loop
EXPANDS from an initial retrieval hit through these edges — pulling the passage
that continues a hit, or the sibling that shares its section — a failure mode
top-K similarity alone does not cover. It needs no entity extraction and makes no
network calls, so it stays offline and cheap; LLM-derived entity edges are a
possible later enrichment.
*/
package rag

import (
	"regexp"
	"sort"
	"strings"
)

const (
	edgeAdjacent = "adjacent"
	edgeSibling  = "sibling"
	/* edgeEntity connects chunks (possibly across sources) that share a salient
	   entity — the "reasoning across disconnected facts" relationship. */
	edgeEntity = "entity"
	/* edgeTopic connects chunks from DIFFERENT sources whose headings share a
	   rare topical token (e.g. "Segmentation and egress" <-> "Firewalls, Zero
	   Trust, and Segmentation") — part of the predefined densification follow-up
	   of the graph-salient benchmark (eval/fixtures/README.md). */
	edgeTopic = "topic"
	/*
		maxGraphExpand caps expansion output so a traversal cannot flood the model.
		Kept deliberately small: the hand-labeled cross-source benchmark
		(eval/fixtures/README.md) measured no recall lift over budget-matched
		lexical widening and near-zero precision of graph-added chunks, even after
		the pre-registered edge densification. graph_expand is consequently
		off-by-default (register behind --graph-expand / Options.GraphExpand); when
		enabled it is a sparse, use-when-connected aid, and the small cap keeps the
		noise the reasoning model must filter bounded.
	*/
	maxGraphExpand = 5
)

type graphEdge struct {
	to   string
	kind string
}

/* Graph is an immutable structural adjacency over chunk ids. */
type Graph struct {
	chunks     map[string]Chunk
	collection map[string]string
	edges      map[string][]graphEdge
}

/*
BuildGraph derives the structural graph from the given collections. Memory
collections are skipped: the graph is a documentation-navigation aid, not a view
over private profile memory. Edge order per node is deterministic (adjacency
before siblings, each in ascending ordinal) so expansion is reproducible.
*/
func BuildGraph(collections []*Collection) *Graph {
	g := &Graph{chunks: map[string]Chunk{}, collection: map[string]string{}, edges: map[string][]graphEdge{}}
	for _, c := range collections {
		if c == nil || c.Kind == "memory" {
			continue
		}
		bySource := map[string][]Chunk{}
		for _, ch := range c.Chunks {
			g.chunks[ch.ID] = ch
			g.collection[ch.ID] = c.Name
			bySource[ch.Source] = append(bySource[ch.Source], ch)
		}
		sources := make([]string, 0, len(bySource))
		for src := range bySource {
			sources = append(sources, src)
		}
		sort.Strings(sources)
		for _, src := range sources {
			chunks := bySource[src]
			sort.Slice(chunks, func(i, j int) bool { return chunks[i].Ordinal < chunks[j].Ordinal })
			for i := 0; i+1 < len(chunks); i++ {
				g.addEdge(chunks[i].ID, chunks[i+1].ID, edgeAdjacent)
				g.addEdge(chunks[i+1].ID, chunks[i].ID, edgeAdjacent)
			}
			byHeading := map[string][]string{}
			headings := []string{}
			for _, ch := range chunks {
				h := strings.TrimSpace(ch.Heading)
				if h == "" {
					continue
				}
				if _, seen := byHeading[h]; !seen {
					headings = append(headings, h)
				}
				byHeading[h] = append(byHeading[h], ch.ID)
			}
			for _, h := range headings {
				ids := byHeading[h]
				for i := range ids {
					for j := i + 1; j < len(ids); j++ {
						g.addEdge(ids[i], ids[j], edgeSibling)
						g.addEdge(ids[j], ids[i], edgeSibling)
					}
				}
			}
		}
	}
	/* Entity and heading-topic edges span sources, so they are derived once over
	   the whole corpus after all structural edges are in place. */
	g.addEntityEdges()
	g.addHeadingTopicEdges()
	return g
}

var headingTokenRE = regexp.MustCompile(`[a-z0-9]{4,}`)

/*
genericHeadingTokens are document-structure words that appear in headings across
any tech corpus and would create hub edges rather than topical ones.
*/
var genericHeadingTokens = map[string]struct{}{
	"best": {}, "practices": {}, "with": {}, "notes": {}, "summary": {},
	"common": {}, "guide": {}, "manual": {}, "basics": {}, "highlights": {},
	"selected": {}, "example": {}, "worked": {}, "overview": {}, "workflow": {},
}

const (
	minTopicChunks = 2
	/* A heading token shared by many chunks is a category word, not a topic;
	   the band keeps topic edges rare and meaningful. */
	maxTopicChunks = 12
)

/*
addHeadingTopicEdges links chunks from DIFFERENT sources whose headings share a
rare topical token. Within-source heading structure is already covered by
sibling edges; this adds the cross-document conceptual hop (e.g. the php and
software-engineering "dependency" sections) that entity extraction misses when
the shared term never appears as a code span, proper noun, or acronym in the
body. Added as the predefined densification follow-up of the graph-salient
benchmark; deterministic like every other edge pass.
*/
func (g *Graph) addHeadingTopicEdges() {
	ids := make([]string, 0, len(g.chunks))
	for id := range g.chunks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tokenChunks := map[string][]string{}
	for _, id := range ids {
		heading := strings.ToLower(g.chunks[id].Heading)
		seen := map[string]struct{}{}
		for _, tok := range headingTokenRE.FindAllString(heading, -1) {
			if _, generic := genericHeadingTokens[tok]; generic {
				continue
			}
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			tokenChunks[tok] = append(tokenChunks[tok], id)
		}
	}

	tokens := make([]string, 0, len(tokenChunks))
	for tok := range tokenChunks {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)

	for _, tok := range tokens {
		members := tokenChunks[tok]
		if len(members) < minTopicChunks || len(members) > maxTopicChunks {
			continue
		}
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				if g.chunks[members[i]].Source == g.chunks[members[j]].Source {
					continue
				}
				g.addEdge(members[i], members[j], edgeTopic)
				g.addEdge(members[j], members[i], edgeTopic)
			}
		}
	}
}

func (g *Graph) addEdge(from, to, kind string) {
	if from == to {
		return
	}
	for _, e := range g.edges[from] {
		if e.to == to {
			return
		}
	}
	g.edges[from] = append(g.edges[from], graphEdge{to: to, kind: kind})
}

/*
Expand returns chunks reachable within hops of any seed id (excluding the seeds),
breadth-first and deterministic, capped at maxGraphExpand. hops is clamped to
[1,3]. Unknown seed ids are simply skipped.
*/
func (g *Graph) Expand(seedIDs []string, hops int) []Chunk {
	if hops < 1 {
		hops = 1
	}
	if hops > 3 {
		hops = 3
	}
	seen := map[string]bool{}
	for _, id := range seedIDs {
		seen[id] = true
	}
	frontier := append([]string(nil), seedIDs...)
	var out []Chunk
	for h := 0; h < hops && len(out) < maxGraphExpand; h++ {
		var next []string
		for _, id := range frontier {
			for _, e := range g.edges[id] {
				if seen[e.to] {
					continue
				}
				seen[e.to] = true
				if ch, ok := g.chunks[e.to]; ok {
					out = append(out, ch)
					next = append(next, e.to)
					if len(out) >= maxGraphExpand {
						return out
					}
				}
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = next
	}
	return out
}

/* CollectionOf reports the collection a chunk id belongs to (empty if unknown). */
func (g *Graph) CollectionOf(id string) string { return g.collection[id] }
