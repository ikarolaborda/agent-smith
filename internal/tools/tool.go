/*
Package tools defines the Tool interface and a thread-safe Registry used by
the agent loop. Each tool advertises a JSON Schema describing its accepted
arguments and is invoked with the raw JSON arguments emitted by the model.
*/
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* Tool is a single executable capability exposed to the model. */
type Tool interface {
	/*
		Name uniquely identifies the tool. Must be stable and provider-safe
		(alphanumeric + underscores; provider-specific length limits apply).
	*/
	Name() string

	/*
		Description is shown to the model and should explain when to invoke
		the tool, in one or two sentences.
	*/
	Description() string

	/* Schema returns the JSON Schema of the accepted arguments object. */
	Schema() json.RawMessage

	/*
		Execute runs the tool with the raw JSON arguments emitted by the model
		and returns the textual result to send back as a tool message.
	*/
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

/*
ErrNotFound is returned by Registry.Get when no tool is registered under the
requested name.
*/
var ErrNotFound = errors.New("tools: not found")

/* Registry is a concurrent-safe collection of tools indexed by Name(). */
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

/* NewRegistry constructs an empty Registry. */
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

/*
Register adds t to the registry. Registering two tools with the same Name()
is a programming error and returns an error rather than silently shadowing.
*/
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tools: %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

/* Get returns the tool registered under name, or ErrNotFound. */
func (r *Registry) Get(name string) (Tool, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return t, nil
}

/* List returns the names of all registered tools in sorted order. */
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

/*
Definitions returns ToolDefinitions ready to be attached to a
llm.ChatRequest, in the same sorted order as List.
*/
func (r *Registry) Definitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]llm.ToolDefinition, 0, len(names))
	for _, n := range names {
		t := r.tools[n]
		out = append(out, llm.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return out
}
