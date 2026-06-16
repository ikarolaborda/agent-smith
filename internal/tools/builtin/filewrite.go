package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

/*
FileWrite creates or overwrites a UTF-8 text file inside a workspace root. It is
the mutating counterpart to FileRead and shares the same sandbox model: the
resolved path must stay under Root, the target (if present) must be a regular
file, and writes are atomic. It is only registered when the operator opts into a
workspace, so the default posture stays read-only.
*/
type FileWrite struct {
	Root     string
	MaxBytes int
}

/* NewFileWrite constructs a FileWrite tool rooted at the workspace dir. */
func NewFileWrite(root string) *FileWrite {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &FileWrite{Root: root, MaxBytes: DefaultWorkspaceMaxWriteBytes}
}

func (*FileWrite) Name() string { return "file_write" }

func (t *FileWrite) Description() string {
	return "Create or overwrite a UTF-8 text file under " + t.Root + " with the given content. Use for new files or full rewrites; use file_edit for surgical changes. The parent directory must already exist. Cannot escape the workspace, follow symlinks, or write non-text/binary special files."
}

func (*FileWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":    { "type": "string", "description": "Path relative to the workspace root." },
    "content": { "type": "string", "description": "Full UTF-8 text content to write." }
  },
  "required": ["path", "content"]
}`)
}

/* Execute validates the path, enforces the size cap and UTF-8, and writes atomically. */
func (t *FileWrite) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("file_write: invalid args: %w", err)
	}
	if args.Path == "" {
		return "", errors.New("file_write: path is required")
	}
	if len(args.Content) > t.MaxBytes {
		return "", fmt.Errorf("file_write: content is %d bytes, exceeds cap of %d", len(args.Content), t.MaxBytes)
	}
	if !utf8.ValidString(args.Content) {
		return "", errors.New("file_write: content is not valid UTF-8 (this tool writes text only)")
	}

	abs, err := resolveWorkspaceTarget(t.Root, args.Path)
	if err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	existed := false
	if _, err := os.Stat(abs); err == nil {
		existed = true
	}
	if err := atomicWrite(abs, []byte(args.Content)); err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	verb := "created"
	if existed {
		verb = "overwrote"
	}
	return fmt.Sprintf("%s %s (%d bytes)", verb, args.Path, len(args.Content)), nil
}
