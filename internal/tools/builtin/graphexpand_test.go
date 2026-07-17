package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/rag"
)

type fakeExpander struct {
	gotSeeds []string
	gotHops  int
	results  []rag.SearchResult
}

func (f *fakeExpander) ExpandGraph(seedIDs []string, hops int) ([]rag.SearchResult, error) {
	f.gotSeeds = seedIDs
	f.gotHops = hops
	return f.results, nil
}

func TestGraphExpandToolCitesUntrustedNeighbors(t *testing.T) {
	fake := &fakeExpander{results: []rag.SearchResult{result("c1", "a.md", "Intro", "the continuation", 0)}}
	tool := NewGraphExpandTool(fake)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"chunk_ids":["c0"," "],"hops":2}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fake.gotSeeds) != 1 || fake.gotSeeds[0] != "c0" {
		t.Errorf("blank seeds should be dropped, got %v", fake.gotSeeds)
	}
	if fake.gotHops != 2 {
		t.Errorf("hops not forwarded: %d", fake.gotHops)
	}
	if !strings.Contains(strings.ToUpper(out), "UNTRUSTED") {
		t.Errorf("neighbors must be labelled untrusted:\n%s", out)
	}
	if !strings.Contains(out, "chunk=c1") {
		t.Errorf("neighbor chunk id missing:\n%s", out)
	}
}

func TestGraphExpandToolRejectsEmptySeeds(t *testing.T) {
	tool := NewGraphExpandTool(&fakeExpander{})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chunk_ids":[" "]}`)); err == nil {
		t.Fatal("expected an error when no usable chunk id is given")
	}
}

func TestGraphExpandToolDefaultsHops(t *testing.T) {
	fake := &fakeExpander{}
	tool := NewGraphExpandTool(fake)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"chunk_ids":["c0"]}`)); err != nil {
		t.Fatal(err)
	}
	if fake.gotHops != defaultGraphHops {
		t.Errorf("hops should default to %d, got %d", defaultGraphHops, fake.gotHops)
	}
}
