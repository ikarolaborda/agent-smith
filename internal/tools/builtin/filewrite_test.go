package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeArgs(t *testing.T, path, content string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestFileWrite(t *testing.T) {
	root := t.TempDir()
	w := NewFileWrite(root)

	t.Run("it_creates_a_new_file", func(t *testing.T) {
		out, err := w.Execute(context.Background(), writeArgs(t, "notes/todo.txt", "hello"))
		if err == nil {
			t.Fatalf("expected error: parent dir does not exist yet, got %q", out)
		}
		/* parent must exist (no arbitrary auto-create in v1) */
		if err := os.Mkdir(filepath.Join(root, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		out, err = w.Execute(context.Background(), writeArgs(t, "notes/todo.txt", "hello"))
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		if !strings.Contains(out, "created") {
			t.Errorf("expected 'created', got %q", out)
		}
		got, _ := os.ReadFile(filepath.Join(root, "notes/todo.txt"))
		if string(got) != "hello" {
			t.Errorf("content = %q, want hello", got)
		}
	})

	t.Run("it_overwrites_and_preserves_mode", func(t *testing.T) {
		p := filepath.Join(root, "a.txt")
		if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Execute(context.Background(), writeArgs(t, "a.txt", "new content")); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != "new content" {
			t.Errorf("content = %q", got)
		}
		if info, _ := os.Stat(p); info.Mode().Perm() != 0o640 {
			t.Errorf("mode = %v, want preserved 0640", info.Mode().Perm())
		}
	})

	t.Run("it_rejects_path_traversal", func(t *testing.T) {
		if _, err := w.Execute(context.Background(), writeArgs(t, "../escape.txt", "x")); err == nil {
			t.Fatal("expected traversal rejection")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
			t.Fatal("traversal write escaped the workspace")
		}
	})

	t.Run("it_rejects_absolute_path_escape", func(t *testing.T) {
		if _, err := w.Execute(context.Background(), writeArgs(t, "/etc/agent-smith-test", "x")); err == nil {
			t.Fatal("expected absolute-path rejection")
		}
	})

	t.Run("it_rejects_symlinked_parent_escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics differ on windows")
		}
		outside := t.TempDir()
		link := filepath.Join(root, "outlink")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := w.Execute(context.Background(), writeArgs(t, "outlink/evil.txt", "x")); err == nil {
			t.Fatal("expected symlinked-parent rejection")
		}
		if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
			t.Fatal("write escaped via symlinked parent")
		}
	})

	t.Run("it_rejects_writing_over_a_symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics differ on windows")
		}
		target := filepath.Join(t.TempDir(), "real.txt")
		_ = os.WriteFile(target, []byte("real"), 0o644)
		link := filepath.Join(root, "slink.txt")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := w.Execute(context.Background(), writeArgs(t, "slink.txt", "x")); err == nil {
			t.Fatal("expected rejection writing over a symlink")
		}
	})

	t.Run("it_works_when_the_workspace_root_is_a_symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink semantics differ on windows")
		}
		realRoot := t.TempDir()
		linkRoot := filepath.Join(t.TempDir(), "ws-link")
		if err := os.Symlink(realRoot, linkRoot); err != nil {
			t.Fatalf("symlink root: %v", err)
		}
		lw := NewFileWrite(linkRoot)
		if _, err := lw.Execute(context.Background(), writeArgs(t, "ok.txt", "data")); err != nil {
			t.Fatalf("write under symlinked root must succeed (consistent canonicalization), got: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(realRoot, "ok.txt"))
		if string(got) != "data" {
			t.Errorf("content via symlinked root = %q, want data", got)
		}
		/* traversal must still be refused even when root is a symlink */
		if _, err := lw.Execute(context.Background(), writeArgs(t, "../escape.txt", "x")); err == nil {
			t.Fatal("traversal must still be rejected when root is a symlink")
		}
	})

	t.Run("it_enforces_the_size_cap", func(t *testing.T) {
		small := NewFileWrite(root)
		small.MaxBytes = 4
		if _, err := small.Execute(context.Background(), writeArgs(t, "big.txt", "toolong")); err == nil {
			t.Fatal("expected size-cap rejection")
		}
	})
}
