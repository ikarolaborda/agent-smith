/*
Package cluster is the control plane for clusterized local inference. The Go
process never performs tensor math itself: it discovers nodes, launches and
supervises external inference runtimes (exo, an MLX/JACCL Python sidecar, or
llama.cpp RPC), routes chat requests to the best available runtime, streams
tokens back, and records metrics. Every backend ultimately exposes an
OpenAI-compatible HTTP endpoint on the coordinator's loopback interface, so the
existing internal/llm/openai client is reused as the data-plane transport.

The package exposes a single llm.Provider implementation (see provider.go) so
it drops into the existing agent/server wiring with no changes to the agent
loop: the cluster is "just another provider" named "cluster".
*/
package cluster

import (
	"context"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
TokenStream is the sink a backend writes streamed output to during Chat. It is
deliberately the same shape the rest of the codebase already streams in
(llm.StreamChunk), so the cluster provider can forward chunks onto the channel
that llm.Provider.ChatStream returns without any translation.
*/
type TokenStream interface {
	OnChunk(llm.StreamChunk)
}

/* TokenStreamFunc adapts a plain function to the TokenStream interface. */
type TokenStreamFunc func(llm.StreamChunk)

/* OnChunk implements TokenStream. */
func (f TokenStreamFunc) OnChunk(c llm.StreamChunk) { f(c) }

/*
BackendConfig is the fully-resolved launch spec handed to a backend's Start.
It is assembled by the manager from the cluster config, the selected model,
and the runtime block, so a backend never has to reach back into global config.
*/
type BackendConfig struct {
	Model   ModelConfig
	Nodes   []Node
	Runtime RuntimeConfig
	/*
		Coordinator is the node that runs the user-facing endpoint (and, for
		llama.cpp, the llama-server that fans out to RPC workers).
	*/
	Coordinator Node
	/*
		Workers are the non-coordinator nodes a distributed backend should
		recruit (exo peers, llama.cpp rpc-server hosts, MLX hostfile entries).
	*/
	Workers []Node
}

/*
BackendCapabilities is the result of Probe: a cheap, side-effect-light check
of whether a backend can plausibly run on this host. Installed reports whether
the runtime binary/package is present; Available reports whether it is both
installed and currently reachable (or launchable). Endpoint is the
OpenAI-compatible base URL once known. Diagnostic carries a human-readable
reason when Available is false (e.g. missing RDMA setup for MLX).
*/
type BackendCapabilities struct {
	Installed  bool
	Available  bool
	Endpoint   string
	MaxContext int
	Diagnostic string
}

/* BackendHealth is the live health of a started backend. */
type BackendHealth struct {
	Backend   string
	Healthy   bool
	Endpoint  string
	Restarts  int
	LastError string
	Detail    string
}

/*
InferenceBackend is one pluggable inference runtime. Implementations live in
backend_*.go. All methods must respect ctx for cancellation. A backend that is
not Start-ed must still answer Probe and Health truthfully.
*/
type InferenceBackend interface {
	Name() string
	Probe(ctx context.Context) (*BackendCapabilities, error)
	Start(ctx context.Context, cfg BackendConfig) error
	Stop(ctx context.Context) error
	Health(ctx context.Context) (*BackendHealth, error)
	Chat(ctx context.Context, req llm.ChatRequest, stream TokenStream) error
}

/*
ClusterManager owns the lifecycle of backends and the topology of nodes. It is
the orchestration surface the provider and CLI talk to.
*/
type ClusterManager interface {
	Discover(ctx context.Context) ([]Node, error)
	StartBackend(ctx context.Context, backend string, model string) error
	StopBackend(ctx context.Context, backend string) error
	Health(ctx context.Context) (*ClusterHealth, error)
}

/*
Scheduler chooses which backend should serve a given request for a given model.
The decision is a pure function of the request, the model spec, node health,
and config — so it is exhaustively unit-testable without any live runtime.
*/
type Scheduler interface {
	SelectBackend(ctx context.Context, req llm.ChatRequest, model ModelConfig) (InferenceBackend, error)
}

/* ClusterHealth aggregates per-backend and per-node health for diagnostics. */
type ClusterHealth struct {
	Mode     string
	Selected string
	Backends []BackendHealth
	Nodes    []NodeHealth
	Metrics  Snapshot
}

/* NodeHealth is the reachability + pressure view of one node. */
type NodeHealth struct {
	ID             string
	Host           string
	Reachable      bool
	MemoryPressure float64
	Detail         string
}
