package rag

import "testing"

func chunkIDs(chunks []Chunk) []string {
	out := make([]string, len(chunks))
	for i, c := range chunks {
		out[i] = c.ID
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func docCollection() *Collection {
	return &Collection{Name: "docs", Chunks: []Chunk{
		{ID: "c0", Source: "a.md", Heading: "Intro", Ordinal: 0, Text: "intro one"},
		{ID: "c1", Source: "a.md", Heading: "Intro", Ordinal: 1, Text: "intro two"},
		{ID: "c2", Source: "a.md", Heading: "Setup", Ordinal: 2, Text: "setup"},
		{ID: "d0", Source: "b.md", Heading: "Other", Ordinal: 0, Text: "other doc"},
	}}
}

func TestGraphExpandAdjacencyAndSiblings(t *testing.T) {
	g := BuildGraph([]*Collection{docCollection()})

	/* c0's only neighbor is c1 (adjacent AND same-heading sibling, deduped). */
	hop1 := chunkIDs(g.Expand([]string{"c0"}, 1))
	if len(hop1) != 1 || hop1[0] != "c1" {
		t.Fatalf("hop-1 from c0 = %v, want [c1]", hop1)
	}

	/* Two hops reaches c2 (adjacent to c1), never the seed again. */
	hop2 := chunkIDs(g.Expand([]string{"c0"}, 2))
	if !contains(hop2, "c1") || !contains(hop2, "c2") {
		t.Fatalf("hop-2 from c0 = %v, want to include c1 and c2", hop2)
	}
	if contains(hop2, "c0") {
		t.Error("expansion must not return the seed")
	}

	/* Cross-source isolation: b.md chunk is never reachable from a.md. */
	if contains(hop2, "d0") {
		t.Error("edge leaked across sources")
	}
}

func TestGraphExpandDeterministic(t *testing.T) {
	g := BuildGraph([]*Collection{docCollection()})
	first := chunkIDs(g.Expand([]string{"c0"}, 3))
	for i := 0; i < 5; i++ {
		got := chunkIDs(g.Expand([]string{"c0"}, 3))
		if len(got) != len(first) {
			t.Fatalf("nondeterministic length: %v vs %v", got, first)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("nondeterministic order: %v vs %v", got, first)
			}
		}
	}
}

func TestGraphSkipsMemoryCollections(t *testing.T) {
	mem := &Collection{Name: "memory", Kind: "memory", Chunks: []Chunk{
		{ID: "m0", Source: "x", Ordinal: 0}, {ID: "m1", Source: "x", Ordinal: 1},
	}}
	g := BuildGraph([]*Collection{mem})
	if got := g.Expand([]string{"m0"}, 3); len(got) != 0 {
		t.Fatalf("memory chunks must not be graphed, got %v", chunkIDs(got))
	}
}
