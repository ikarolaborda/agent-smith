package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

/*
FileRead is a grounding tool that reads files from a configured root. It is
intentionally narrow: the model can quote and grep local files but cannot
escape the root, write, or execute. Path traversal is blocked by checking
that the cleaned absolute path remains a descendant of Root.
*/
type FileRead struct {
	Root       string
	MaxBytes   int
	MaxMatches int
}

/* DefaultFileReadMaxBytes caps the body returned to the model. */
const DefaultFileReadMaxBytes = 16 * 1024

/* DefaultFileReadMaxMatches caps how many grep hits we return. */
const DefaultFileReadMaxMatches = 50

/*
NewFileRead constructs a FileRead tool. An empty root resolves to the
process working directory at construction time; in --serve mode that is the
binary's CWD which is typically the repository root.
*/
func NewFileRead(root string) *FileRead {
	if root == "" {
		root, _ = os.Getwd()
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return &FileRead{
		Root:       root,
		MaxBytes:   DefaultFileReadMaxBytes,
		MaxMatches: DefaultFileReadMaxMatches,
	}
}

func (*FileRead) Name() string { return "file_read" }

func (t *FileRead) Description() string {
	return "Read a UTF-8 text file under " + t.Root + ", optionally filtering lines that match a regex pattern. Returns the body or matched lines with line numbers. Cannot escape the configured root."
}

func (*FileRead) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":    { "type": "string", "description": "Path relative to the configured root." },
    "pattern": { "type": "string", "description": "Optional Go regexp; when set, only matching lines are returned with their line numbers." },
    "max_bytes": { "type": "integer", "description": "Override the response byte cap." }
  },
  "required": ["path"]
}`)
}

/*
Execute reads the requested file. When pattern is set, every line matching
the compiled regex is emitted as "<line_number>: <line>". When pattern is
empty, the leading max_bytes of the file are returned verbatim.
*/
func (t *FileRead) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		MaxBytes int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("file_read: invalid args: %w", err)
	}
	if args.Path == "" {
		return "", errors.New("file_read: path is required")
	}

	clean := filepath.Clean(args.Path)
	resolved := filepath.Join(t.Root, clean)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("file_read: abs path: %w", err)
	}
	if !insideRoot(abs, t.Root) {
		return "", fmt.Errorf("file_read: refused: %q is outside the configured root", clean)
	}
	/*
		Resolve symlinks so a link inside the root pointing outside cannot
		escape. We require the resolved real path to ALSO be inside the
		canonicalized root.
	*/
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		rootReal, rerr := filepath.EvalSymlinks(t.Root)
		if rerr != nil {
			rootReal = t.Root
		}
		if !insideRoot(real, rootReal) {
			return "", fmt.Errorf("file_read: refused: %q resolves outside the configured root", clean)
		}
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("file_read: stat: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("file_read: %q is a directory; this tool reads files only", clean)
	}

	cap := args.MaxBytes
	if cap <= 0 {
		cap = t.MaxBytes
	}

	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("file_read: open: %w", err)
	}
	defer f.Close()

	if args.Pattern != "" {
		re, err := regexp.Compile(args.Pattern)
		if err != nil {
			return "", fmt.Errorf("file_read: invalid pattern: %w", err)
		}
		return scanMatches(f, re, t.MaxMatches, cap)
	}

	body := make([]byte, cap)
	n, _ := f.Read(body)
	suffix := ""
	if int64(n) < info.Size() {
		suffix = "\n\n…truncated; file is " + fmt.Sprintf("%d", info.Size()) + " bytes, showing first " + fmt.Sprintf("%d", n) + "."
	}
	return string(body[:n]) + suffix, nil
}

/* insideRoot reports whether abs is the same as root or a descendant of it. */
func insideRoot(abs, root string) bool {
	absClean := filepath.Clean(abs)
	rootClean := filepath.Clean(root)
	if absClean == rootClean {
		return true
	}
	rel, err := filepath.Rel(rootClean, absClean)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

/* scanMatches walks the file line by line, returning hits up to maxMatches or maxBytes. */
func scanMatches(f *os.File, re *regexp.Regexp, maxMatches, maxBytes int) (string, error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var b strings.Builder
	hits := 0
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if !re.MatchString(text) {
			continue
		}
		row := fmt.Sprintf("%d: %s\n", line, text)
		if b.Len()+len(row) > maxBytes {
			b.WriteString("…truncated at byte cap.\n")
			return b.String(), nil
		}
		b.WriteString(row)
		hits++
		if hits >= maxMatches {
			b.WriteString("…truncated at " + fmt.Sprintf("%d", maxMatches) + " matches.\n")
			return b.String(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return b.String(), fmt.Errorf("file_read: scan: %w", err)
	}
	if hits == 0 {
		return "(no matches)\n", nil
	}
	return b.String(), nil
}
