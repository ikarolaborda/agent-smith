/*
Package rag implements retrieval-augmented generation over a small set of
in-memory collections persisted as one JSON file per collection. The chunker
splits markdown by heading and size with overlap; the index does brute-force
cosine search and the service wires everything to an llm.Embedder.
*/
package rag

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

/*
Chunk is a single retrievable unit. Source is the on-disk path the chunk
came from (slash-normalized) for "docs" collections, or a synthetic origin
("user-input") for "memory" collections. Heading is the breadcrumb of nested
headings above the chunk text. Ordinal disambiguates multiple chunks under
the same heading.

The fields after Vector are populated only for "memory" collections; they
remain zero values for "docs" chunks and are omitted from JSON when zero.
*/
type Chunk struct {
	ID      string    `json:"id"`
	Source  string    `json:"source"`
	Heading string    `json:"heading"`
	Ordinal int       `json:"ordinal"`
	Text    string    `json:"text"`
	Vector  []float32 `json:"vector,omitempty"`
	/*
		Kind classifies a memory chunk: "preference" | "project_fact" |
		"decision" | "correction". Empty for docs.
	*/
	Kind string `json:"kind,omitempty"`
	/*
		Subject is the profile UUID that owns this memory chunk. Empty for
		docs; required for memory.
	*/
	Subject string `json:"subject,omitempty"`
	/*
		Importance is a memory-only ranking boost in [0,1]. Pinned memories
		default to 1.0; corrections to 0.8; regular facts to 0.5.
	*/
	Importance float32 `json:"importance,omitempty"`
	/* Pinned memories never decay in ranking and survive Forget-by-decay. */
	Pinned bool `json:"pinned,omitempty"`
	/*
		CreatedAt and LastAccessed feed the soft-decay recency factor for
		memory ranking.
	*/
	CreatedAt    string `json:"created_at,omitempty"`
	LastAccessed string `json:"last_accessed,omitempty"`
}

/*
ChunkOptions controls Chunker behavior. MaxChars caps the size of any single
chunk; OverlapChars is the size of the trailing overlap copied into the next
chunk so cross-boundary phrases remain retrievable.
*/
type ChunkOptions struct {
	MaxChars     int
	OverlapChars int
}

/* DefaultChunkOptions is a sensible default for prose markdown. */
var DefaultChunkOptions = ChunkOptions{
	MaxChars:     3000,
	OverlapChars: 300,
}

/*
NormalizeMarkdown strips a UTF-8 BOM, converts CRLF and isolated CR to LF,
and replaces invalid UTF-8 sequences with U+FFFD. This is the single
normalization point used by both chunking and chunk-ID hashing so identical
inputs always produce identical IDs.
*/
func NormalizeMarkdown(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	if !utf8.Valid(b) {
		b = bytes.ToValidUTF8(b, []byte{0xEF, 0xBF, 0xBD})
	}
	return b
}

/*
SplitMarkdown splits a normalized markdown document into chunks. Headings
(#, ##, ###) start new sections; sections larger than opts.MaxChars are
split further on paragraph boundaries; very long paragraphs are hard-split.
The first chunk-end is suffixed with the next chunk-start's prefix to a
length of opts.OverlapChars so phrases that span a boundary are still found.
*/
func SplitMarkdown(source, text string, opts ChunkOptions) []Chunk {
	if opts.MaxChars <= 0 {
		opts = DefaultChunkOptions
	}
	sections := splitByHeadings(text)

	var out []Chunk
	for _, sec := range sections {
		ords := splitBySize(sec.body, opts)
		for i, body := range ords {
			c := Chunk{
				Source:  source,
				Heading: sec.heading,
				Ordinal: i,
				Text:    body,
			}
			out = append(out, c)
		}
	}
	applyOverlap(out, opts.OverlapChars)
	/* IDs describe the final stored text, including its retrieval overlap. */
	for i := range out {
		out[i].ID = chunkID(out[i].Source, out[i].Heading, out[i].Ordinal, out[i].Text)
	}
	return out
}

type section struct {
	heading string
	body    string
}

/*
splitByHeadings walks the document and starts a new section when it sees a
top-level heading (#, ##, ###). The breadcrumb is the trimmed heading text.
Lines before the first heading are emitted under an empty heading.
*/
func splitByHeadings(text string) []section {
	var sections []section
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var heading string
	var body strings.Builder

	flush := func() {
		s := strings.TrimSpace(body.String())
		if s == "" {
			return
		}
		sections = append(sections, section{heading: heading, body: s})
		body.Reset()
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			flush()
			heading = strings.TrimSpace(strings.TrimLeft(line, "# "))
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	return sections
}

/*
splitBySize splits a section body into chunks no larger than opts.MaxChars,
trying to break on paragraph boundaries first and hard-splitting only when a
single paragraph exceeds the cap.
*/
func splitBySize(body string, opts ChunkOptions) []string {
	if utf8.RuneCountInString(body) <= opts.MaxChars {
		return []string{body}
	}
	paragraphs := strings.Split(body, "\n\n")
	var out []string
	var current strings.Builder
	currentChars := 0
	for _, p := range paragraphs {
		pRunes := []rune(p)
		separatorChars := 0
		if current.Len() > 0 {
			separatorChars = 2
		}
		if currentChars+len(pRunes)+separatorChars <= opts.MaxChars {
			if current.Len() > 0 {
				current.WriteString("\n\n")
				currentChars += 2
			}
			current.WriteString(p)
			currentChars += len(pRunes)
			continue
		}
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
			currentChars = 0
		}
		if len(pRunes) > opts.MaxChars {
			for start := 0; start < len(pRunes); start += opts.MaxChars {
				end := start + opts.MaxChars
				if end > len(pRunes) {
					end = len(pRunes)
				}
				out = append(out, string(pRunes[start:end]))
			}
		} else {
			current.WriteString(p)
			currentChars = len(pRunes)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

/*
applyOverlap copies the first opts.OverlapChars characters of chunk i+1
onto the end of chunk i so a phrase split across the boundary is still
retrievable from chunk i.
*/
func applyOverlap(chunks []Chunk, overlap int) {
	if overlap <= 0 || len(chunks) < 2 {
		return
	}
	for i := 0; i < len(chunks)-1; i++ {
		next := []rune(chunks[i+1].Text)
		if len(next) > overlap {
			next = next[:overlap]
		}
		chunks[i].Text += "\n\n" + string(next)
	}
}

/*
chunkID returns a deterministic ID derived from the source path, heading,
ordinal, and the chunk body so re-ingesting unchanged content yields the
same ID while content edits produce a new one.
*/
func chunkID(source, heading string, ordinal int, body string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(source))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(heading))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte{byte(ordinal)})
	_, _ = h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
