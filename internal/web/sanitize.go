package web

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

/*
urlLikeRe matches anything that looks like an absolute URL inside a
snippet body. We strip these because we already render the canonical
result URL on its own line — repeating URLs inside the description is a
phishing-via-URL surface (an adversary can stuff a different domain into
the meta description than the canonical result URL).
*/
var urlLikeRe = regexp.MustCompile(`https?://\S+`)

/* zwInvisibleRe matches zero-width and bidi-override codepoints used in smuggling attacks. */
var zwInvisibleRe = regexp.MustCompile(`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{206F}\x{FEFF}]`)

/* whitespaceRunRe collapses any run of whitespace (including newlines) into a single space. */
var whitespaceRunRe = regexp.MustCompile(`\s+`)

/*
sanitizeText scrubs a raw third-party string before it lands in a prompt.
The order matters: strip HTML and decode entities first so we operate on
plain text, then remove smuggling codepoints, then collapse whitespace, then
finally truncate to the caller-supplied cap.
*/
func sanitizeText(raw string, maxChars int) string {
	s := stripHTMLTags(raw)
	s = html.UnescapeString(s)
	s = removeNonPrintable(s)
	s = zwInvisibleRe.ReplaceAllString(s, "")
	s = whitespaceRunRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if maxChars > 0 && len(s) > maxChars {
		s = strings.TrimSpace(s[:maxChars]) + "…"
	}
	return s
}

/* sanitizeSnippet additionally strips URL-like substrings from the body. */
func sanitizeSnippet(raw string, maxChars int) string {
	s := stripHTMLTags(raw)
	s = html.UnescapeString(s)
	/*
		Strip invisible/zero-width characters BEFORE redacting URLs. Otherwise a
		zero-width codepoint planted inside a scheme ("ht<zwsp>tps://evil") slips
		past urlLikeRe, and removing it afterward reassembles a live link in the
		snippet — defeating the whole point of redacting URLs from the body.
	*/
	s = removeNonPrintable(s)
	s = zwInvisibleRe.ReplaceAllString(s, "")
	s = urlLikeRe.ReplaceAllString(s, "[link]")
	s = whitespaceRunRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if maxChars > 0 && len(s) > maxChars {
		s = strings.TrimSpace(s[:maxChars]) + "…"
	}
	return s
}

/*
sanitizeURL keeps only absolute http(s) URLs and rejects everything else.
We do not URL-decode here; rendering as-is is safer than producing a string
that decodes to something unexpected when the model echoes it back.
*/
func sanitizeURL(raw string, maxChars int) string {
	s := strings.TrimSpace(raw)
	s = removeNonPrintable(s)
	s = zwInvisibleRe.ReplaceAllString(s, "")
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	if i := strings.IndexAny(s, " \t\r\n\""); i >= 0 {
		s = s[:i]
	}
	if maxChars > 0 && len(s) > maxChars {
		s = s[:maxChars]
	}
	return s
}

/* removeNonPrintable drops control characters except tab/newline/CR (which whitespace collapse later normalizes). */
func removeNonPrintable(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

/*
stripHTMLTags removes all <...> spans without trying to parse HTML
semantically. We pair it with html.UnescapeString to recover characters like
"&amp;" that survived the markup. Good enough for snippet-grade text; full
DOM walking happens in ddg.go when we need actual structure.
*/
func stripHTMLTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inside := false
	for _, r := range s {
		switch {
		case r == '<':
			inside = true
		case r == '>':
			inside = false
		case !inside:
			b.WriteRune(r)
		}
	}
	return b.String()
}
