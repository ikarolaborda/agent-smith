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

func TestReadDir_LoadsFolderSkippingNoiseAndBinary(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "main.go"), "package main\nfunc main() {}\n")
	mustWrite(t, filepath.Join(root, "README.md"), "# hello\n")
	mustWrite(t, filepath.Join(root, "blob.bin"), "ok\x00binary")
	mustWrite(t, filepath.Join(root, "node_modules", "dep.js"), "console.log('noise')\n")

	rd := builtin.NewReadDir(root)
	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "func main()") {
		t.Errorf("expected main.go contents, got: %q", out)
	}
	if !strings.Contains(out, "README.md") || !strings.Contains(out, "# hello") {
		t.Errorf("expected README.md contents, got: %q", out)
	}
	if strings.Contains(out, "blob.bin") {
		t.Errorf("binary file must be skipped (no header), got: %q", out)
	}
	if strings.Contains(out, "noise") || strings.Contains(out, "dep.js") {
		t.Errorf("node_modules must be skipped, got: %q", out)
	}
}

func TestReadDir_ExtFilterAndRootEscapeRefused(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(root, "b.txt"), "text\n")

	rd := builtin.NewReadDir(root)
	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":".","ext":"go"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "b.txt") {
		t.Errorf("ext filter should include only .go, got: %q", out)
	}
	if _, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"../.."}`)); err == nil {
		t.Error("expected refusal for a path escaping the root")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
