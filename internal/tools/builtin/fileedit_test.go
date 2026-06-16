package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func editArgs(t *testing.T, path, oldS, newS string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "old_string": oldS, "new_string": newS})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestFileEdit(t *testing.T) {
	root := t.TempDir()
	e := NewFileEdit(root)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("it_replaces_a_unique_fragment", func(t *testing.T) {
		write("main.go", "package main\nfunc old() {}\n")
		out, err := e.Execute(context.Background(), editArgs(t, "main.go", "func old()", "func renamed()"))
		if err != nil {
			t.Fatalf("edit: %v", err)
		}
		if out == "" {
			t.Error("expected a result summary")
		}
		got, _ := os.ReadFile(filepath.Join(root, "main.go"))
		if string(got) != "package main\nfunc renamed() {}\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("it_rejects_a_non_unique_fragment", func(t *testing.T) {
		write("dup.txt", "x\nx\n")
		if _, err := e.Execute(context.Background(), editArgs(t, "dup.txt", "x", "y")); err == nil {
			t.Fatal("expected rejection: old_string occurs more than once")
		}
		got, _ := os.ReadFile(filepath.Join(root, "dup.txt"))
		if string(got) != "x\nx\n" {
			t.Errorf("file must be unchanged on ambiguous edit, got %q", got)
		}
	})

	t.Run("it_rejects_a_missing_fragment", func(t *testing.T) {
		write("c.txt", "hello")
		if _, err := e.Execute(context.Background(), editArgs(t, "c.txt", "absent", "z")); err == nil {
			t.Fatal("expected rejection: old_string not found")
		}
	})

	t.Run("it_rejects_traversal", func(t *testing.T) {
		if _, err := e.Execute(context.Background(), editArgs(t, "../x", "a", "b")); err == nil {
			t.Fatal("expected traversal rejection")
		}
	})
}
