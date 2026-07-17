package rag

import "testing"

func TestExtractEntities(t *testing.T) {
	ents := extractEntities("Use a `rag_search` tool with the Knowledge Graph to answer.")
	if !contains(ents, "rag_search") {
		t.Errorf("backticked identifier not extracted: %v", ents)
	}
	if !contains(ents, "knowledge graph") {
		t.Errorf("multi-word proper noun not extracted: %v", ents)
	}
	/* Lowercase filler words must not become entities. */
	if contains(ents, "the") || contains(ents, "answer") || contains(ents, "use") {
		t.Errorf("noise extracted as entities: %v", ents)
	}
}

/*
Entity edges connect chunks that share a salient term across sources — the
relationship structural adjacency and vector similarity both miss.
*/
func TestEntityEdgesConnectAcrossSources(t *testing.T) {
	col := &Collection{Name: "docs", Chunks: []Chunk{
		{ID: "a0", Source: "a.md", Heading: "Auth", Ordinal: 0, Text: "Send a `Bearer Token` to the Payment Gateway."},
		{ID: "b0", Source: "b.md", Heading: "Billing", Ordinal: 0, Text: "The Payment Gateway rejects an expired `Bearer Token`."},
	}}
	g := BuildGraph([]*Collection{col})

	/* a0 and b0 live in different sources (no adjacency/sibling edge) yet share
	   the "Payment Gateway" entity and the `Bearer Token` identifier. */
	got := chunkIDs(g.Expand([]string{"a0"}, 1))
	if !contains(got, "b0") {
		t.Fatalf("entity edge should connect cross-source chunks sharing an entity; expand(a0)=%v", got)
	}
}

/*
Entity edges must not defeat isolation for chunks that share NOTHING salient:
lowercase-only chunks in different sources stay disconnected.
*/
func TestNoEntityEdgeWithoutSharedEntity(t *testing.T) {
	col := &Collection{Name: "docs", Chunks: []Chunk{
		{ID: "a0", Source: "a.md", Ordinal: 0, Text: "some lowercase words here"},
		{ID: "b0", Source: "b.md", Ordinal: 0, Text: "different lowercase words there"},
	}}
	g := BuildGraph([]*Collection{col})
	if contains(chunkIDs(g.Expand([]string{"a0"}, 2)), "b0") {
		t.Error("chunks sharing no salient entity must not be connected")
	}
}

/*
	An entity that appears in more chunks than maxEntityChunks is stop-word-like

and must produce no entity edges, keeping the graph from over-connecting.
*/
func TestEntityEdgesCapEnforced(t *testing.T) {
	chunks := make([]Chunk, maxEntityChunks+5)
	for i := range chunks {
		chunks[i] = Chunk{
			ID:      "c" + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			Source:  "s" + string(rune('a'+i)),
			Ordinal: 0,
			Text:    "Mentions the Payment Gateway here.",
		}
	}
	g := BuildGraph([]*Collection{{Name: "docs", Chunks: chunks}})
	/* The shared entity exceeds the band, so no chunk gains an entity neighbor. */
	if got := g.Expand([]string{chunks[0].ID}, 1); len(got) != 0 {
		t.Fatalf("over-frequent entity must not create edges, got %v", chunkIDs(got))
	}
}
