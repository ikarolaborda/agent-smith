package rag

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

/*
These tests target the two residuals flagged when proportional grounding
shipped: lost-in-the-middle at the 24-chunk cap on large windows, and answer
completeness at the 2048 floor. Neither can be fully settled without model
generation, but their MITIGATIONS are structural and verifiable offline against
the real embedded corpus: the highest-scored evidence must appear FIRST (so a
model that attends to the head of the grounding gets the best chunk), and the
single fragment kept at the floor must be the best one, not an arbitrary
survivor.
*/

var scoreLine = regexp.MustCompile(`\(score (\d\.\d\d)\)`)

/* injectedScores returns the per-chunk scores in the order Augment emitted them. */
func injectedScores(augmented string) []float64 {
	var out []float64
	for _, m := range scoreLine.FindAllStringSubmatch(augmented, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

var qualityQueries = []string{
	"how do I prevent SQL injection in a web application",
	"explain cross-site scripting and its mitigations",
	"what is the OSI model and what does each layer do",
	"describe buffer overflow exploitation and defenses",
	"what are the SOLID principles in software design",
	"how does the TCP three-way handshake work",
}

func TestGroundingLargeWindowIsBestFirst(t *testing.T) {
	s := augmentService(t)
	s.ContextTokens = 131072
	for _, q := range qualityQueries {
		out := s.Augment(context.Background(), q, "", false)
		docBlock := out
		if i := strings.Index(out, "## Remembered context"); i >= 0 {
			docBlock = out[:i]
		}
		scores := injectedScores(docBlock)
		if len(scores) < 2 {
			continue
		}
		/*
			Best-first is the lost-in-the-middle mitigation: whatever the model's
			positional bias, the top evidence sits at the head of the grounding.
			The per-(source,heading) dedup may drop a lower chunk, but it must
			never promote a weaker chunk above a stronger earlier one.
		*/
		for i := 1; i < len(scores); i++ {
			if scores[i] > scores[i-1]+1e-9 {
				t.Fatalf("query %q: grounding not best-first at position %d (%.2f after %.2f)", q, i, scores[i], scores[i-1])
			}
		}
	}
}

func TestGroundingFloorKeepsTheBestEvidence(t *testing.T) {
	small := augmentService(t)
	small.ContextTokens = 2048
	big := augmentService(t)
	big.ContextTokens = 131072
	for _, q := range qualityQueries {
		bigScores := injectedScores(big.Augment(context.Background(), q, "", false))
		smallScores := injectedScores(small.Augment(context.Background(), q, "", false))
		if len(bigScores) == 0 || len(smallScores) == 0 {
			continue
		}
		/*
			The completeness-at-the-floor guarantee: the tiny window keeps the
			single strongest chunk the large window would also rank first, so a
			2048-token host loses breadth but never trades down to weaker evidence.
		*/
		if smallScores[0] < bigScores[0]-1e-9 {
			t.Fatalf("query %q: floor kept weaker evidence (%.2f) than the top-ranked chunk (%.2f)", q, smallScores[0], bigScores[0])
		}
		if len(smallScores) > 3 {
			t.Fatalf("query %q: 2048-window injected %d chunks, expected a tight floor", q, len(smallScores))
		}
	}
}

func TestGroundingCapBoundsChunkCount(t *testing.T) {
	s := augmentService(t)
	s.ContextTokens = 131072
	for _, q := range qualityQueries {
		n := len(injectedScores(s.Augment(context.Background(), q, "", false)))
		if n > groundingMaxChunks {
			t.Fatalf("query %q injected %d chunks, over the %d cap", q, n, groundingMaxChunks)
		}
	}
}
