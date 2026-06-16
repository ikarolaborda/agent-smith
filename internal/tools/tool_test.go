package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/tools"
)

type fakeTool struct {
	name string
}

func (f fakeTool) Name() string                                                 { return f.name }
func (f fakeTool) Description() string                                          { return "fake" }
func (f fakeTool) Schema() json.RawMessage                                      { return json.RawMessage(`{}`) }
func (f fakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil }

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := tools.NewRegistry()
	if err := r.Register(fakeTool{name: "a"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register(fakeTool{name: "a"}); err == nil {
		t.Fatalf("expected error registering duplicate tool")
	}
	got, err := r.Get("a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "a" {
		t.Fatalf("got %q want %q", got.Name(), "a")
	}
}

func TestRegistry_GetNotFound(t *testing.T) {
	r := tools.NewRegistry()
	if _, err := r.Get("missing"); !errors.Is(err, tools.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRegistry_ListAndDefinitions(t *testing.T) {
	r := tools.NewRegistry()
	_ = r.Register(fakeTool{name: "b"})
	_ = r.Register(fakeTool{name: "a"})
	names := r.List()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("List sorted incorrectly: %v", names)
	}
	defs := r.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}
}
