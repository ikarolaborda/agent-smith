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
	"sort"
	"strings"
)

const (
	edgeAdjacent = "adjacent"
	edgeSibling  = "sibling"
	/* edgeEntity connects chunks (possibly across sources) that share a salient
	   entity — the "reasoning across disconnected facts" relationship. */
	edgeEntity = "entity"
	/* maxGraphExpand caps expansion output so a traversal cannot flood the model. */
	maxGraphExpand = 8
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
	/* Entity edges span sources, so they are derived once over the whole corpus
	   after all structural edges are in place. */
	g.addEntityEdges()
	return g
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
