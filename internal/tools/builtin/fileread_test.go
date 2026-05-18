package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
)

func setup(t *testing.T) (string, *builtin.FileRead) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("line one\nline two has WORD\nline three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := builtin.NewFileRead(dir)
	return dir, tool
}

func TestFileRead_ReadsBody(t *testing.T) {
	_, tool := setup(t)
	args, _ := json.Marshal(map[string]any{"path": "hello.txt"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line three") {
		t.Fatalf("body missing: %s", out)
	}
}

func TestFileRead_GrepReturnsMatchedLinesWithNumbers(t *testing.T) {
	_, tool := setup(t)
	args, _ := json.Marshal(map[string]any{"path": "hello.txt", "pattern": "WORD"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2: line two has WORD") {
		t.Fatalf("expected matched line with line number, got %q", out)
	}
}

func TestFileRead_BlocksPathTraversal(t *testing.T) {
	_, tool := setup(t)
	args, _ := json.Marshal(map[string]any{"path": "../../etc/passwd"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "outside the configured root") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestFileRead_RejectsDirectory(t *testing.T) {
	dir, tool := setup(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": "sub"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("expected directory refusal, got %v", err)
	}
}

func TestFileRead_BlocksSymlinkEscape(t *testing.T) {
	dir, tool := setup(t)
	/* create an out-of-root secret file and a symlink to it from inside the root */
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP_SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "leak.txt")
	if err := os.Symlink(secret, linkPath); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"path": "leak.txt"})
	out, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("expected refusal of symlink escape, got body: %s", out)
	}
	if !strings.Contains(err.Error(), "outside the configured root") {
		t.Fatalf("expected outside-root error, got %v", err)
	}
}
