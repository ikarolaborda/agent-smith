package server

import (
	"strings"
	"testing"
)

/* TestSanitizeTitle covers the messy outputs real (and abliterated/reasoning) models emit. */
func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Procedural vs OOP in Go", "Procedural vs OOP in Go"},
		{"trailing_punct", "Database indexing tips.", "Database indexing tips"},
		{"surrounding_quotes", "\"REST API design\"", "REST API design"},
		{"title_label", "Title: Goroutine leak debugging", "Goroutine leak debugging"},
		{"title_label_lower", "title: kubernetes pod scheduling", "kubernetes pod scheduling"},
		{"think_block", "<think>The user is asking about X.</think>\nCaching strategies", "Caching strategies"},
		{"multiline_takes_first", "Sorting algorithms\nextra rambling line", "Sorting algorithms"},
		{"markdown_wrap", "**Vector embeddings**", "Vector embeddings"},
		{"collapse_whitespace", "Concurrency   patterns\t\tin   Go", "Concurrency patterns in Go"},
		{"word_cap", "one two three four five six seven eight nine ten", "one two three four five six seven eight"},
		{"empty", "", ""},
		{"only_think", "<think>just reasoning, no answer</think>", ""},
		{"only_punct", "...", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeTitle(c.in); got != c.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

/*
TestSanitizeTitleRuneCap verifies the rune cap trims long output to a whole word
(never mid-word) without panicking on multibyte input.
*/
func TestSanitizeTitleRuneCap(t *testing.T) {
	long := "abcdéfghij abcdéfghij abcdéfghij abcdéfghij abcdéfghij abcdéfghij abcdéfghij abcdéfghij"
	got := sanitizeTitle(long)
	if n := len([]rune(got)); n > titleMaxRunes {
		t.Errorf("sanitizeTitle rune length = %d, want <= %d", n, titleMaxRunes)
	}
	for _, w := range strings.Fields(got) {
		if w != "abcdéfghij" {
			t.Errorf("rune cap left a partial word %q in %q", w, got)
		}
	}
}

/* TestSanitizeTitleRuneCapNoSpace falls back to a hard cut when there is no word boundary. */
func TestSanitizeTitleRuneCapNoSpace(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := sanitizeTitle(long)
	if n := len([]rune(got)); n != titleMaxRunes {
		t.Errorf("no-space rune cap length = %d, want %d", n, titleMaxRunes)
	}
}
