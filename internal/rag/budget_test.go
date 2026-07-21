package rag

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestGroundingBudgetTable(t *testing.T) {
	cases := []struct {
		name       string
		ctxTokens  int
		pinned     bool
		wantChunks int
		wantBytes  int
	}{
		{"unknown window keeps static defaults", 0, false, DefaultMaxChunksInjected, DefaultMaxBytesInjected},
		{"tiny 2048 window hits the floor", 2048, false, 1, 1227},
		{"mid 8192 window stays near the old default", 8192, false, 2, 4914},
		{"32k window widens", 32768, false, 9, 19659},
		{"131k window caps out", 131072, false, groundingMaxChunks, groundingCapBytes},
		{"operator pin beats scaling", 131072, true, DefaultMaxChunksInjected, DefaultMaxBytesInjected},
	}
	for _, c := range cases {
		s := &Service{MaxChunks: DefaultMaxChunksInjected, MaxBytes: DefaultMaxBytesInjected,
			ContextTokens: c.ctxTokens, PinnedChunks: c.pinned}
		chunks, bytes := s.groundingBudget()
		if chunks != c.wantChunks || bytes != c.wantBytes {
			t.Fatalf("%s: got (%d chunks, %d bytes), want (%d, %d)", c.name, chunks, bytes, c.wantChunks, c.wantBytes)
		}
	}
}

func TestTrimFragmentRuneSafeAndMarked(t *testing.T) {
	frag := "### heading\nSource: `x`\n\n" + strings.Repeat("é", 400) + "\n\n"
	got := trimFragment(frag, 300)
	if len(got) > 300 {
		t.Fatalf("trimmed fragment exceeds budget: %d", len(got))
	}
	if !strings.Contains(got, "[truncated to fit the context budget]") {
		t.Fatal("trim marker missing")
	}
	if !strings.HasPrefix(got, "### heading") {
		t.Fatal("citation header must survive the trim")
	}
}

func TestGroundingHint(t *testing.T) {
	s := &Service{MaxChunks: 4, MaxBytes: 8000}
	if s.GroundingHint() != "" {
		t.Fatal("unknown window must not add a hint (pre-dynamic directive stays byte-identical)")
	}
	s.ContextTokens = 32768
	hint := s.GroundingHint()
	if !strings.Contains(hint, "grounding budget") || !strings.Contains(hint, "rag_search") {
		t.Fatalf("hint should state the budget and pacing guidance, got %q", hint)
	}
}

/*
augmentService builds a Service over the real embedded lexical corpus only —
no store, no embedders — so Augment's budget/assembly behavior is exercised
against genuine cybersecurity/cs-fundamentals chunks.
*/
func augmentService(t *testing.T) *Service {
	t.Helper()
	lex, err := loadBuiltinLexicalIndex()
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Lexical:      lex,
		Logger:       slog.Default(),
		MaxChunks:    DefaultMaxChunksInjected,
		MaxBytes:     DefaultMaxBytesInjected,
		Threshold:    DefaultThreshold,
		StrictThresh: DefaultStrictThreshold,
	}
}

func TestAugmentScalesWithWindow(t *testing.T) {
	const query = "how do I prevent SQL injection in a web application"
	small := augmentService(t)
	small.ContextTokens = 2048
	big := augmentService(t)
	big.ContextTokens = 131072
	def := augmentService(t)

	outSmall := small.Augment(context.Background(), query, "", false)
	outBig := big.Augment(context.Background(), query, "", false)
	outDef := def.Augment(context.Background(), query, "", false)

	if !strings.Contains(outSmall, "## Relevant documentation") {
		t.Fatal("small window must still carry at least one (trimmed) grounding fragment")
	}
	nSmall := strings.Count(outSmall, "### ")
	nDef := strings.Count(outDef, "### ")
	nBig := strings.Count(outBig, "### ")
	if !(nSmall <= nDef && nDef <= nBig) {
		t.Fatalf("chunk count must grow with the window: small=%d default=%d big=%d", nSmall, nDef, nBig)
	}
	if nBig <= nDef {
		t.Fatalf("131k window should ground more chunks than the static default, got big=%d default=%d", nBig, nDef)
	}
	if len(outSmall) >= len(outBig) {
		t.Fatal("small-window augmentation must be smaller than the large-window one")
	}
}

func TestAugmentSmallWindowStaysNearBudget(t *testing.T) {
	s := augmentService(t)
	s.ContextTokens = 2048
	out := s.Augment(context.Background(), "explain buffer overflow exploitation and mitigation", "", false)
	_, wantBytes := s.groundingBudget()
	/*
		The grounding sections must respect the byte budget; the fixed banner and
		behavior epilogue are additive, so allow them on top rather than asserting
		on the raw total alone.
	*/
	if len(out) > wantBytes+2500 {
		t.Fatalf("2048-window augmentation is %d bytes for a %d-byte grounding budget", len(out), wantBytes)
	}
}

func TestAugmentDedupsSameSection(t *testing.T) {
	s := augmentService(t)
	s.ContextTokens = 131072
	out := s.Augment(context.Background(), "network protocols and the OSI model explained", "", false)
	headers := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "### ") {
			key := line[:strings.LastIndex(line, " (score")+1]
			headers[key]++
			if headers[key] > 2 {
				t.Fatalf("more than two chunks injected for the same section header: %q", key)
			}
		}
	}
}
