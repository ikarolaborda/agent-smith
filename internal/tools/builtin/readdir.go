package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

/*
ReadDir loads an entire folder into context in a single call — the agent
equivalent of an IDE's "@folder": it recursively reads the UTF-8 text files
under a directory and returns them concatenated with path headers, bounded by a
total byte budget. It reuses FileRead's root-confinement (insideRoot) so it can
neither escape the configured root nor read binaries; noise directories and
oversized/binary files are skipped so the budget is spent on source.
*/
type ReadDir struct {
	Root         string
	MaxBytes     int
	MaxFileBytes int
}

/* DefaultReadDirMaxBytes caps the total payload across all files returned to the model. */
const DefaultReadDirMaxBytes = 200 * 1024

/* DefaultReadDirMaxFileBytes caps how much of any single file is included. */
const DefaultReadDirMaxFileBytes = 32 * 1024

/* readDirSkip is the set of directory names never descended into (noise/build/VCS). */
var readDirSkip = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".agent": true,
	"dist": true, "build": true, "target": true, "__pycache__": true,
	".idea": true, ".vscode": true, ".serena": true, ".next": true,
}

/* NewReadDir constructs a ReadDir rooted like FileRead (empty root = CWD). */
func NewReadDir(root string) *ReadDir {
	if root == "" {
		root, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &ReadDir{Root: root, MaxBytes: DefaultReadDirMaxBytes, MaxFileBytes: DefaultReadDirMaxFileBytes}
}

/*
readCappedFile reads at most maxBytes+1 bytes so allocation is bounded by the
per-file cap (a multi-GB file cannot OOM a single call), while the extra byte
still lets the caller detect that truncation occurred.
*/
func readCappedFile(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
}

func (*ReadDir) Name() string { return "read_dir" }

func (t *ReadDir) Description() string {
	return "Load an entire folder under " + t.Root + " into context at once (like an IDE @folder): recursively reads the UTF-8 text files in a directory and returns them concatenated with path headers, up to a total byte budget. Skips noise dirs (.git, node_modules, vendor, ...) and binary/oversized files. Use it to grok a whole project area before working on it; narrow with `ext` or a deeper `path` if it truncates. Cannot escape the configured root."
}

func (*ReadDir) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":      { "type": "string", "description": "Directory path relative to the root ('.' or omitted = the root)." },
    "ext":       { "type": "string", "description": "Optional comma-separated extensions to include, e.g. 'go,md'. Default = all text files." },
    "max_bytes": { "type": "integer", "description": "Override the total byte budget across all files." }
  }
}`)
}

/*
Execute walks the requested directory, gathers matching text files in sorted
path order, and returns them concatenated with "===== <relpath> =====" headers
until the byte budget is reached. Binary, oversized, and unreadable files are
skipped and counted in the trailing summary.
*/
func (t *ReadDir) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path     string `json:"path"`
		Ext      string `json:"ext"`
		MaxBytes int    `json:"max_bytes"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("read_dir: invalid args: %w", err)
		}
	}

	rel := args.Path
	if rel == "" {
		rel = "."
	}
	clean := filepath.Clean(rel)
	abs, err := filepath.Abs(filepath.Join(t.Root, clean))
	if err != nil {
		return "", fmt.Errorf("read_dir: abs path: %w", err)
	}
	if !insideRoot(abs, t.Root) {
		return "", fmt.Errorf("read_dir: refused: %q is outside the configured root", clean)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("read_dir: stat: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("read_dir: %q is not a directory; use file_read for a single file", clean)
	}

	budget := args.MaxBytes
	if budget <= 0 {
		budget = t.MaxBytes
	}

	exts := map[string]bool{}
	for _, e := range strings.Split(args.Ext, ",") {
		if e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, "."))); e != "" {
			exts[e] = true
		}
	}

	var files []string
	_ = filepath.WalkDir(abs, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if p != abs && readDirSkip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(exts) > 0 {
			if !exts[strings.ToLower(strings.TrimPrefix(filepath.Ext(d.Name()), "."))] {
				return nil
			}
		}
		files = append(files, p)
		return nil
	})
	sort.Strings(files)

	rootReal, rootErr := filepath.EvalSymlinks(t.Root)
	if rootErr != nil {
		rootReal = t.Root
	}

	var b strings.Builder
	used := 0
	included, skipped := 0, 0
	truncated := false
	for _, p := range files {
		/*
			Mirror file_read's symlink confinement: WalkDir yields a symlinked
			file as a plain entry, so without resolving the link a `docs/x ->
			/etc/passwd` symlink would read the outside target. Require the
			resolved real path to stay inside the canonicalized root.
		*/
		if real, err := filepath.EvalSymlinks(p); err == nil && !insideRoot(real, rootReal) {
			skipped++
			continue
		}
		data, err := readCappedFile(p, t.MaxFileBytes)
		if err != nil {
			skipped++
			continue
		}
		if bytes.IndexByte(data, 0) >= 0 {
			skipped++
			continue
		}
		if len(data) > t.MaxFileBytes {
			data = append(data[:t.MaxFileBytes:t.MaxFileBytes], []byte("\n…(file truncated)")...)
		}
		rp, _ := filepath.Rel(t.Root, p)
		chunk := "\n===== " + rp + " =====\n" + string(data) + "\n"
		if used+len(chunk) > budget {
			truncated = true
			break
		}
		b.WriteString(chunk)
		used += len(chunk)
		included++
	}

	summary := fmt.Sprintf("\n\n----- read_dir: %d files loaded, %d skipped (binary/unreadable)", included, skipped)
	if truncated {
		summary += fmt.Sprintf("; stopped at the %d-byte budget with %d file(s) not loaded — narrow with `ext` or a deeper `path`", budget, len(files)-included-skipped)
	}
	summary += " -----"
	if included == 0 {
		return "(no text files loaded under " + clean + ")" + summary, nil
	}
	return b.String() + summary, nil
}
