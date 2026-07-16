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

func TestReadDir_BlocksSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, outside, "TOP SECRET KEY MATERIAL\n")
	mustWrite(t, filepath.Join(root, "keep.md"), "# in-root\n")
	if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	rd := builtin.NewReadDir(root)
	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "TOP SECRET") {
		t.Errorf("read_dir followed a symlink outside the root: %q", out)
	}
	if !strings.Contains(out, "in-root") {
		t.Errorf("expected in-root file to still be read, got: %q", out)
	}
}

func TestReadDir_InRootSymlinkStillRead(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "real", "doc.md"), "# real doc\n")
	if err := os.Symlink(filepath.Join(root, "real", "doc.md"), filepath.Join(root, "alias.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	rd := builtin.NewReadDir(root)
	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "real doc") {
		t.Errorf("an in-root symlink target must still be readable, got: %q", out)
	}
}

func TestReadDir_BoundsOversizedFileToCap(t *testing.T) {
	root := t.TempDir()
	rd := builtin.NewReadDir(root)
	big := strings.Repeat("A", rd.MaxFileBytes*2)
	mustWrite(t, filepath.Join(root, "big.txt"), big)

	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "file truncated") {
		t.Errorf("oversized file should be truncated with a marker, got len=%d", len(out))
	}
	if strings.Count(out, "A") > rd.MaxFileBytes+16 {
		t.Errorf("included more than the per-file cap: %d 'A's", strings.Count(out, "A"))
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
