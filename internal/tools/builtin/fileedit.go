package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

/*
FileEdit performs a surgical exact-string replacement in a UTF-8 text file
inside the workspace. It mirrors Claude Code's Edit contract: old_string must
occur EXACTLY ONCE, which forces the model to supply enough surrounding context
to make the edit unambiguous and prevents accidental mass replacement. Like
FileWrite it is workspace-gated, sandboxed, and writes atomically.
*/
type FileEdit struct {
	Root     string
	MaxBytes int
}

/* NewFileEdit constructs a FileEdit tool rooted at the workspace dir. */
func NewFileEdit(root string) *FileEdit {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &FileEdit{Root: root, MaxBytes: DefaultWorkspaceMaxWriteBytes}
}

func (*FileEdit) Name() string { return "file_edit" }

func (t *FileEdit) Description() string {
	return "Replace an exact text fragment in a file under " + t.Root + ". old_string must appear exactly once (include surrounding context to make it unique); it is replaced with new_string. Fails if old_string is absent or appears more than once. Cannot escape the workspace or follow symlinks."
}

func (*FileEdit) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":       { "type": "string", "description": "Path relative to the workspace root." },
    "old_string": { "type": "string", "description": "Exact text to replace; must occur exactly once in the file." },
    "new_string": { "type": "string", "description": "Replacement text." }
  },
  "required": ["path", "old_string", "new_string"]
}`)
}

/*
Execute reads the target, requires old_string to occur exactly once, applies the
replacement, and persists atomically. Zero or multiple matches are errors so the
edit is never ambiguous.
*/
func (t *FileEdit) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("file_edit: invalid args: %w", err)
	}
	if args.Path == "" {
		return "", errors.New("file_edit: path is required")
	}
	if args.OldString == "" {
		return "", errors.New("file_edit: old_string is required")
	}
	if args.OldString == args.NewString {
		return "", errors.New("file_edit: old_string and new_string are identical")
	}

	abs, err := resolveWorkspaceTarget(t.Root, args.Path)
	if err != nil {
		return "", fmt.Errorf("file_edit: %w", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("file_edit: read: %w", err)
	}
	if !utf8.Valid(data) {
		return "", errors.New("file_edit: file is not valid UTF-8 (this tool edits text only)")
	}
	content := string(data)

	count := strings.Count(content, args.OldString)
	if count == 0 {
		return "", errors.New("file_edit: old_string not found")
	}
	if count > 1 {
		return "", fmt.Errorf("file_edit: old_string occurs %d times; include more surrounding context so it is unique", count)
	}

	updated := strings.Replace(content, args.OldString, args.NewString, 1)
	if len(updated) > t.MaxBytes {
		return "", fmt.Errorf("file_edit: result is %d bytes, exceeds cap of %d", len(updated), t.MaxBytes)
	}
	if err := atomicWrite(abs, []byte(updated)); err != nil {
		return "", fmt.Errorf("file_edit: %w", err)
	}
	return fmt.Sprintf("edited %s (%d -> %d bytes)", args.Path, len(content), len(updated)), nil
}
