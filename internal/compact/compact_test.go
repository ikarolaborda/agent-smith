package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeSummarizer struct{ calls int }

func (f *fakeSummarizer) Summarize(_ context.Context, text, _ string) (string, error) {
	f.calls++
	return fmt.Sprintf("SUMMARY[%d chars]", len(text)), nil
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(strings.Repeat("x", 400)); got != 100 {
		t.Errorf("EstimateTokens(400 chars) = %d, want 100", got)
	}
}

func TestCompact_BelowTriggerIsNoop(t *testing.T) {
	c := &Compactor{TriggerTokens: 1000}
	res, err := c.Compact(context.Background(), "short input", "")
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Errorf("small input must not be compacted, got %+v", res)
	}
}

func TestCompact_PreservesHeadTailAndSummarizes(t *testing.T) {
	head := "INSTRUCTION: refactor the parser."
	tail := "END: return only the diff."
	middle := strings.Repeat("filler narrative sentence about the codebase. ", 4000) // large
	content := head + "\n" + middle + "\n" + tail

	fs := &fakeSummarizer{}
	c := &Compactor{
		Summarizer:    fs,
		TriggerTokens: 2000,
		HeadTokens:    12,
		TailTokens:    12,
		ChunkChars:    2000,
	}
	res, err := c.Compact(context.Background(), content, "")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("large input must be compacted")
	}
	if fs.calls == 0 {
		t.Error("summarizer was never called")
	}
	if !strings.Contains(res.Compacted, "INSTRUCTION: refactor") {
		t.Error("head (instruction) not preserved verbatim")
	}
	if !strings.Contains(res.Compacted, "return only the diff") {
		t.Error("tail not preserved verbatim")
	}
	if !strings.Contains(res.Compacted, "SUMMARY[") {
		t.Error("summary not embedded")
	}
	if !strings.Contains(res.Compacted, "context_search") {
		t.Error("retrieval pointer not present")
	}
	if res.FinalTokens >= res.OrigTokens {
		t.Errorf("compaction did not shrink the input: %d -> %d", res.OrigTokens, res.FinalTokens)
	}
	if res.Index == nil || res.Index.Len() == 0 {
		t.Error("expected a populated retrieval index")
	}
}

func TestCompact_CacheAvoidsResummarizing(t *testing.T) {
	fs := &fakeSummarizer{}
	c := &Compactor{
		Summarizer:    fs,
		TriggerTokens: 500,
		HeadTokens:    5,
		TailTokens:    5,
		ChunkChars:    1000,
		Cache:         NewSummaryCache(0),
	}
	content := strings.Repeat("repeated paste content across turns. ", 500)
	for i := 0; i < 3; i++ {
		if _, err := c.Compact(context.Background(), content, ""); err != nil {
			t.Fatal(err)
		}
	}
	if fs.calls != 1 {
		t.Errorf("identical content summarized %d times, want 1 (cache miss then two hits)", fs.calls)
	}
}

func TestCompactForce_CompactsBelowTrigger(t *testing.T) {
	c := &Compactor{
		Summarizer:    &fakeSummarizer{},
		TriggerTokens: 100000, // deliberately huge: gated Compact will NOT fire
		HeadTokens:    5,
		TailTokens:    5,
		ChunkChars:    1000,
	}
	content := strings.Repeat("filler sentence about the codebase. ", 400)

	// Gated Compact no-ops because the content is under the (huge) trigger.
	if res, _ := c.Compact(context.Background(), content, ""); res != nil {
		t.Fatal("gated Compact should no-op under the trigger")
	}
	// CompactForce ignores the trigger and shrinks it (the total-budget path).
	res, err := c.CompactForce(context.Background(), content, "")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("CompactForce should compact content above the min floor")
	}
	if res.FinalTokens >= res.OrigTokens {
		t.Errorf("did not shrink: %d -> %d", res.OrigTokens, res.FinalTokens)
	}
	// Tiny content is still left alone (overhead would not help).
	if r, _ := c.CompactForce(context.Background(), "short", ""); r != nil {
		t.Error("tiny content must not be force-compacted")
	}
}

func TestCompact_SummaryFailureIsNonFatal(t *testing.T) {
	c := &Compactor{
		Summarizer:    failSummarizer{},
		TriggerTokens: 500,
		HeadTokens:    5,
		TailTokens:    5,
		ChunkChars:    1000,
	}
	res, err := c.Compact(context.Background(), strings.Repeat("data point alpha. ", 500), "")
	if err != nil {
		t.Fatalf("summary failure must not fail compaction: %v", err)
	}
	if res == nil || res.Index == nil {
		t.Fatal("expected a Result with an index even when summary fails")
	}
	if strings.Contains(res.Compacted, "SUMMARY[") {
		t.Error("did not expect a summary when the summarizer errored")
	}
}

type failSummarizer struct{}

func (failSummarizer) Summarize(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("boom")
}

func TestIndex_RetrievesRelevantChunk(t *testing.T) {
	chunks := []string{
		"the quick brown fox jumps",
		"a rare token zephyrquux appears only here",
		"lorem ipsum dolor sit amet",
	}
	idx := BuildIndex(chunks)
	hits := idx.Search("zephyrquux", 2)
	if len(hits) == 0 {
		t.Fatal("expected a hit for the rare token")
	}
	if hits[0].Ordinal != 1 {
		t.Errorf("top hit ordinal = %d, want 1 (the chunk containing the rare token)", hits[0].Ordinal)
	}
}

func TestIndex_NoOverlapReturnsNothing(t *testing.T) {
	idx := BuildIndex([]string{"alpha beta gamma"})
	if hits := idx.Search("nonexistent", 3); len(hits) != 0 {
		t.Errorf("off-topic query must return no hits, got %d", len(hits))
	}
}

func TestContextSearchTool_Execute(t *testing.T) {
	idx := BuildIndex([]string{"config value FOOBAR is 42", "unrelated text"})
	tool := NewContextSearchTool(idx)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"FOOBAR","k":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "FOOBAR is 42") {
		t.Errorf("expected the matching passage, got: %q", out)
	}
}
