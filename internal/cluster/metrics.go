/*
Metrics collection for the cluster layer. A single Collector is shared by the
provider and every backend. It tracks the last completed request's latency and
throughput, cumulative token counts, per-backend process restarts, and the
last-observed node health/memory pressure. Everything is mutex-guarded and
copied out via Snapshot so callers never hold the lock.
*/
package cluster

import (
	"sync"
	"time"
)

/*
Snapshot is an immutable copy of the metrics at one instant. The fields map
directly onto the metrics the task requires: time to first token, tokens per
second, prompt/generated tokens, the backend selected, per-backend restarts,
and node health/memory pressure.
*/
type Snapshot struct {
	Backend          string
	TimeToFirstToken time.Duration
	TokensPerSecond  float64
	PromptTokens     int
	GeneratedTokens  int
	Requests         int
	Errors           int
	Restarts         map[string]int
	NodeHealth       map[string]bool
	MemoryPressure   map[string]float64
	UpdatedAt        time.Time
}

/*
MemoryPressureUnknown is the sentinel recorded when a node's real unified-memory
pressure cannot be observed (the v1 case for remote nodes). It is deliberately
negative so it can never be mistaken for a real 0.0 reading. Memory pressure is
observability-only: the scheduler's placement decision uses the statically
configured memory_gb, never this metric — see scheduler.memoryFits.
*/
const MemoryPressureUnknown = -1.0

/* Collector accumulates cluster metrics across requests. */
type Collector struct {
	mu              sync.Mutex
	backend         string
	ttft            time.Duration
	tokensPerSecond float64
	promptTokens    int
	generatedTokens int
	requests        int
	errors          int
	restarts        map[string]int
	nodeHealth      map[string]bool
	memoryPressure  map[string]float64
	updatedAt       time.Time
}

/* NewCollector returns an initialized Collector. */
func NewCollector() *Collector {
	return &Collector{
		restarts:       map[string]int{},
		nodeHealth:     map[string]bool{},
		memoryPressure: map[string]float64{},
	}
}

/*
RequestRecord is the per-request accounting a backend produces while streaming.
TTFT is measured from request dispatch to the first non-empty delta; tokens per
second is generated tokens over the generation window.
*/
type RequestRecord struct {
	Backend          string
	TimeToFirstToken time.Duration
	TokensPerSecond  float64
	PromptTokens     int
	GeneratedTokens  int
	Err              bool
}

/* Observe folds one completed request into the running metrics. */
func (c *Collector) Observe(r RequestRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backend = r.Backend
	c.ttft = r.TimeToFirstToken
	c.tokensPerSecond = r.TokensPerSecond
	c.promptTokens += r.PromptTokens
	c.generatedTokens += r.GeneratedTokens
	c.requests++
	if r.Err {
		c.errors++
	}
	c.updatedAt = time.Now()
}

/* RecordRestart increments the restart counter for a backend. */
func (c *Collector) RecordRestart(backend string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarts[backend]++
	c.updatedAt = time.Now()
}

/* SetNodeHealth records reachability and memory pressure for a node. */
func (c *Collector) SetNodeHealth(nodeID string, reachable bool, memoryPressure float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeHealth[nodeID] = reachable
	c.memoryPressure[nodeID] = memoryPressure
	c.updatedAt = time.Now()
}

/* Snapshot returns a deep copy of the current metrics. */
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Snapshot{
		Backend:          c.backend,
		TimeToFirstToken: c.ttft,
		TokensPerSecond:  c.tokensPerSecond,
		PromptTokens:     c.promptTokens,
		GeneratedTokens:  c.generatedTokens,
		Requests:         c.requests,
		Errors:           c.errors,
		Restarts:         map[string]int{},
		NodeHealth:       map[string]bool{},
		MemoryPressure:   map[string]float64{},
		UpdatedAt:        c.updatedAt,
	}
	for k, v := range c.restarts {
		s.Restarts[k] = v
	}
	for k, v := range c.nodeHealth {
		s.NodeHealth[k] = v
	}
	for k, v := range c.memoryPressure {
		s.MemoryPressure[k] = v
	}
	return s
}

/* Restarts returns the restart count recorded for a backend. */
func (c *Collector) Restarts(backend string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restarts[backend]
}
