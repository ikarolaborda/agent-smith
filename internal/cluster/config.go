/*
Config types and loader for the cluster layer. This is intentionally a
separate file from internal/config so the existing single-node config schema is
untouched: a deployment that never sets a cluster block keeps behaving exactly
as before. The cluster config can live in its own YAML file (recommended) or be
embedded under a top-level "cluster:"/"models:"/"runtime:" set of keys.
*/
package cluster

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

/* Backend name constants. These are the only valid values in config. */
const (
	BackendExo      = "exo"
	BackendMLXJACCL = "mlx_jaccl"
	BackendLlamaRPC = "llama_cpp_rpc"
	BackendLocal    = "local"
)

/* Role constants for nodes. */
const (
	RoleCoordinator = "coordinator"
	RoleWorker      = "worker"
)

/*
Transport names, in the order the sample config prefers them. They are advisory
metadata today: the control plane records the preference and surfaces it to the
backends (e.g. MLX hostfile generation, llama.cpp interface warnings) rather
than implementing a transport stack itself.
*/
const (
	TransportThunderboltRDMA = "thunderbolt_rdma"
	TransportThunderboltTCP  = "thunderbolt_tcp"
	TransportEthernet        = "ethernet"
	TransportLocal           = "local"
)

/* Node is one machine in the cluster. */
type Node struct {
	ID       string   `yaml:"id"`
	Host     string   `yaml:"host"`
	Role     string   `yaml:"role"`
	MemoryGB int      `yaml:"memory_gb"`
	GPUCores int      `yaml:"gpu_cores"`
	Priority int      `yaml:"priority"`
	Backends []string `yaml:"backends"`
}

/* ClusterTopology is the "cluster:" block. */
type ClusterTopology struct {
	/*
		Mode is one of: auto, exo, mlx_jaccl, llama_cpp_rpc, local. "auto" lets
		the scheduler pick by preference + health; any other value pins the
		cluster to a single backend (still subject to fallback unless
		runtime.strict_cluster is set).
	*/
	Mode                string   `yaml:"mode"`
	Coordinator         string   `yaml:"coordinator"`
	TransportPreference []string `yaml:"transport_preference"`
	Nodes               []Node   `yaml:"nodes"`
	/*
		CoordinatorReserveGB is the memory kept free on the coordinator (macOS,
		KV cache growth, Metal compute buffers, headroom) when deciding whether a
		model can run single-node. A model whose min_memory_gb_estimate fits the
		coordinator MINUS this reserve runs single-node and is NOT tensor-split
		onto a worker. Rationale: distributing a fit-capable model only adds a
		fragile node to the critical path. The 24 GB worker kernel-panicked
		(watchdogd 90 s timeout, jetsam memory-pressure death spiral) every time
		it was given a model slice it could not hold; clustering it bought no
		capacity the coordinator did not already have. Zero falls back to the
		built-in default.
	*/
	CoordinatorReserveGB int `yaml:"coordinator_reserve_gb"`
	/*
		Allowlist is the set of hostnames/IPs the control plane is permitted to
		talk to for inter-node traffic. Empty means "derive from nodes": only
		the configured node hosts are allowed. A non-empty list is authoritative.
	*/
	Allowlist []string `yaml:"allowlist"`
}

/* ModelConfig is one entry in the "models:" list. */
type ModelConfig struct {
	ID                string   `yaml:"id"`
	Format            string   `yaml:"format"`
	Path              string   `yaml:"path"`
	ContextTokens     int      `yaml:"context_tokens"`
	PreferredBackends []string `yaml:"preferred_backends"`
	MinMemoryGB       int      `yaml:"min_memory_gb_estimate"`
	Notes             string   `yaml:"notes"`
	/*
		ForceDistribute opts a model out of the single-node-first guard: even when
		it would fit the coordinator alone, the scheduler is allowed to tensor-split
		it across the cluster. Unsafe on memory-tight workers — leave false unless
		you have measured that every node holds its share without memory pressure.
	*/
	ForceDistribute bool `yaml:"force_distribute"`
	/*
		ServedName is the model id strings sent on the wire to the backend's
		OpenAI endpoint. Defaults to ID when empty. Lets a config id like
		"70b-q4-default" map to whatever name the runtime expects.
	*/
	ServedName string `yaml:"served_name"`
}

/* RuntimeConfig is the "runtime:" block governing binding and supervision. */
type RuntimeConfig struct {
	BindHost            string `yaml:"bind_host"`
	APIPort             int    `yaml:"api_port"`
	PrivateClusterOnly  bool   `yaml:"private_cluster_only"`
	RequireAuthToken    bool   `yaml:"require_auth_token"`
	AuthToken           string `yaml:"auth_token"`
	ProcessRestart      string `yaml:"process_restart_policy"`
	MaxRestartAttempts  int    `yaml:"max_restart_attempts"`
	StreamTimeoutSecond int    `yaml:"stream_timeout_seconds"`
	/*
		StrictCluster disables the single-node local fallback. When true a
		failure to bring up the chosen cluster backend is a hard error rather
		than a silent downgrade to the local runner.
	*/
	StrictCluster bool `yaml:"strict_cluster"`
	/*
		LogPrompts gates request/output logging. Off by default: prompts and
		model output are only written to logs when this is explicitly enabled.
	*/
	LogPrompts bool `yaml:"log_prompts"`

	/* Per-backend launch knobs. All optional; sane defaults applied in Normalize. */
	Exo   ExoRuntime   `yaml:"exo"`
	MLX   MLXRuntime   `yaml:"mlx"`
	Llama LlamaRuntime `yaml:"llama_cpp"`
}

/* ExoRuntime configures the exo backend launcher/connector. */
type ExoRuntime struct {
	/* Binary is the exo executable name/path used for installation detection. */
	Binary string `yaml:"binary"`
	/*
		Endpoint, when set, makes the backend connect to an already-running exo
		service instead of launching one. Format: http://host:port (the "/v1"
		suffix is added automatically).
	*/
	Endpoint string `yaml:"endpoint"`
	/* Port is the loopback port exo is expected to listen on when launched. */
	Port int `yaml:"port"`
	/* ExtraArgs are appended verbatim to the launch command. */
	ExtraArgs []string `yaml:"extra_args"`
}

/* MLXRuntime configures the MLX/JACCL python sidecar. */
type MLXRuntime struct {
	/* Python is the interpreter used to run the sidecar (default: python3). */
	Python string `yaml:"python"`
	/* Sidecar is the path to the launcher script (default: scripts/mlx_sidecar.py). */
	Sidecar string `yaml:"sidecar"`
	/* Port is the loopback port the sidecar's OpenAI server binds. */
	Port int `yaml:"port"`
	/* Pipeline selects pipeline-parallel distribution instead of tensor split. */
	Pipeline bool `yaml:"pipeline"`
	/* FastSync sets MLX_METAL_FAST_SYNCH=1 in the sidecar environment. */
	FastSync bool `yaml:"fast_sync"`
	/* ExtraArgs are appended to the sidecar invocation. */
	ExtraArgs []string `yaml:"extra_args"`
}

/* LlamaRuntime configures the llama.cpp RPC backend. */
type LlamaRuntime struct {
	/* Server is the llama-server binary (built with -DGGML_RPC=ON). */
	Server string `yaml:"server"`
	/* Port is the loopback port llama-server's OpenAI endpoint binds. */
	Port int `yaml:"port"`
	/*
		RPCHosts is the explicit list of host:port rpc-server endpoints to
		offload to. Empty means "derive from worker nodes" using RPCPort.
	*/
	RPCHosts []string `yaml:"rpc_hosts"`
	/* RPCPort is the default rpc-server port assumed for worker nodes. */
	RPCPort int `yaml:"rpc_port"`
	/* GPULayers maps to -ngl (number of layers to offload; 99 = all). */
	GPULayers int `yaml:"gpu_layers"`
	/* TensorSplit maps to --tensor-split (proportional weights per device). */
	TensorSplit string `yaml:"tensor_split"`
	/*
		Parallel maps to --parallel (n_parallel: concurrent decode slots). It
		defaults to 1, NOT llama-server's auto value (which picks 4). Each slot
		multiplies the FIXED per-device memory overhead: KV cache and the compute
		graph buffer scale with n_parallel, so auto=4 inflated a memory-tight RPC
		worker far beyond its weight share (a measured ~3.4GB slice ballooned to
		~13GB wired and tripped the jetsam/watchdogd kernel panic). One slot is
		correct for a single-user agent; raise it only with memory headroom to spare.
	*/
	Parallel int `yaml:"parallel"`
	/* CacheDir, when set, is passed as the prompt/KV cache directory. */
	CacheDir string `yaml:"cache_dir"`
	/* ExtraArgs are appended to the llama-server invocation. */
	ExtraArgs []string `yaml:"extra_args"`
	/*
		StartupTimeoutSeconds bounds how long the backend waits for llama-server
		to finish loading the model and answer its HTTP endpoint before it is
		declared unhealthy. llama-server binds its TCP port within seconds but
		returns 503 "loading model" for the whole load, which for a 72B Q4 split
		over Thunderbolt RPC is minutes — so a short gate falsely fails it and the
		scheduler degrades to the single-node fallback. Default 600s in Normalize.
	*/
	StartupTimeoutSeconds int `yaml:"startup_timeout_seconds"`
}

/* ClusterConfig is the top-level shape parsed from the cluster YAML file. */
type ClusterConfig struct {
	Cluster ClusterTopology `yaml:"cluster"`
	Models  []ModelConfig   `yaml:"models"`
	Runtime RuntimeConfig   `yaml:"runtime"`
}

/* LoadClusterConfig reads and validates a cluster YAML file. */
func LoadClusterConfig(path string) (*ClusterConfig, error) {
	if path == "" {
		return nil, errors.New("cluster: config path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cluster: read %s: %w", path, err)
	}
	var cfg ClusterConfig
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), &cfg); err != nil {
		return nil, fmt.Errorf("cluster: parse %s: %w", path, err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

/* Normalize fills in defaults so downstream code never branches on empties. */
func (c *ClusterConfig) Normalize() {
	if c.Cluster.Mode == "" {
		c.Cluster.Mode = "auto"
	}
	if c.Cluster.CoordinatorReserveGB == 0 {
		/*
			20 GB headroom on the coordinator: macOS baseline (~8 GB) + KV cache
			growth at large context + Metal compute buffers + safety. A model
			whose estimate fits coordinator_memory_gb - 20 runs single-node.
		*/
		c.Cluster.CoordinatorReserveGB = 20
	}
	if c.Runtime.BindHost == "" {
		c.Runtime.BindHost = "127.0.0.1"
	}
	if c.Runtime.APIPort == 0 {
		c.Runtime.APIPort = 8080
	}
	if c.Runtime.ProcessRestart == "" {
		c.Runtime.ProcessRestart = "on_failure"
	}
	if c.Runtime.MaxRestartAttempts == 0 {
		c.Runtime.MaxRestartAttempts = 3
	}
	if c.Runtime.StreamTimeoutSecond == 0 {
		c.Runtime.StreamTimeoutSecond = 900
	}
	if c.Runtime.Exo.Binary == "" {
		c.Runtime.Exo.Binary = "exo"
	}
	if c.Runtime.Exo.Port == 0 {
		c.Runtime.Exo.Port = 52415
	}
	if c.Runtime.MLX.Python == "" {
		c.Runtime.MLX.Python = "python3"
	}
	if c.Runtime.MLX.Sidecar == "" {
		c.Runtime.MLX.Sidecar = "scripts/mlx_sidecar.py"
	}
	if c.Runtime.MLX.Port == 0 {
		c.Runtime.MLX.Port = 8081
	}
	if c.Runtime.Llama.Server == "" {
		c.Runtime.Llama.Server = "llama-server"
	}
	if c.Runtime.Llama.Port == 0 {
		c.Runtime.Llama.Port = 8082
	}
	if c.Runtime.Llama.RPCPort == 0 {
		c.Runtime.Llama.RPCPort = 50052
	}
	if c.Runtime.Llama.GPULayers == 0 {
		c.Runtime.Llama.GPULayers = 99
	}
	if c.Runtime.Llama.Parallel == 0 {
		/* One decode slot: avoids the n_parallel KV/compute multiplier that
		   kernel-panicked the memory-tight RPC worker. */
		c.Runtime.Llama.Parallel = 1
	}
	if c.Runtime.Llama.StartupTimeoutSeconds == 0 {
		c.Runtime.Llama.StartupTimeoutSeconds = 600
	}
	for i := range c.Models {
		if c.Models[i].ServedName == "" {
			c.Models[i].ServedName = c.Models[i].ID
		}
	}
}

/* Validate enforces the invariants the manager and scheduler rely on. */
func (c *ClusterConfig) Validate() error {
	if len(c.Cluster.Nodes) == 0 {
		return errors.New("cluster: at least one node is required")
	}
	seen := map[string]bool{}
	hasCoordinator := false
	for _, n := range c.Cluster.Nodes {
		if n.ID == "" {
			return errors.New("cluster: node id is required")
		}
		if seen[n.ID] {
			return fmt.Errorf("cluster: duplicate node id %q", n.ID)
		}
		seen[n.ID] = true
		if n.Role == RoleCoordinator {
			hasCoordinator = true
		}
	}
	if c.Cluster.Coordinator != "" && !seen[c.Cluster.Coordinator] {
		return fmt.Errorf("cluster: coordinator %q is not a known node", c.Cluster.Coordinator)
	}
	if !hasCoordinator && c.Cluster.Coordinator == "" {
		return errors.New("cluster: no coordinator node (set a node role: coordinator or cluster.coordinator)")
	}
	if len(c.Models) == 0 {
		return errors.New("cluster: at least one model is required")
	}
	if c.Runtime.PrivateClusterOnly && !isLoopback(c.Runtime.BindHost) {
		return fmt.Errorf("cluster: private_cluster_only is set but bind_host %q is not loopback", c.Runtime.BindHost)
	}
	return nil
}

/* CoordinatorNode resolves the coordinator, preferring the explicit name. */
func (c *ClusterConfig) CoordinatorNode() Node {
	if c.Cluster.Coordinator != "" {
		if n, ok := c.nodeByID(c.Cluster.Coordinator); ok {
			return n
		}
	}
	for _, n := range c.Cluster.Nodes {
		if n.Role == RoleCoordinator {
			return n
		}
	}
	return c.Cluster.Nodes[0]
}

/* WorkerNodes returns every node that is not the coordinator. */
func (c *ClusterConfig) WorkerNodes() []Node {
	coord := c.CoordinatorNode()
	out := make([]Node, 0, len(c.Cluster.Nodes))
	for _, n := range c.Cluster.Nodes {
		if n.ID != coord.ID {
			out = append(out, n)
		}
	}
	return out
}

/* ModelByID looks up a configured model. */
func (c *ClusterConfig) ModelByID(id string) (ModelConfig, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelConfig{}, false
}

/* AllowedHosts is the effective inter-node allowlist (explicit or derived). */
func (c *ClusterConfig) AllowedHosts() map[string]bool {
	out := map[string]bool{}
	if len(c.Cluster.Allowlist) > 0 {
		for _, h := range c.Cluster.Allowlist {
			out[strings.ToLower(strings.TrimSpace(h))] = true
		}
		return out
	}
	for _, n := range c.Cluster.Nodes {
		out[strings.ToLower(strings.TrimSpace(n.Host))] = true
	}
	return out
}

func (c *ClusterConfig) nodeByID(id string) (Node, bool) {
	for _, n := range c.Cluster.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

/* lowerTrim normalizes a host string for allowlist comparison. */
func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

/* isLoopback reports whether a bind host is loopback-only. */
func isLoopback(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

/* isPrivateHost reports whether a host is loopback or RFC1918/link-local. */
func isPrivateHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if isLoopback(h) {
		return true
	}
	/*
		String-prefix checks are sufficient here: Thunderbolt bridges and the
		local LAN use these private ranges, and we only warn (never block) on a
		miss, so an over-broad match is harmless.
	*/
	privatePrefixes := []string{"10.", "192.168.", "169.254.", "172.16.", "172.17.", "172.18.", "172.19.", "172.2", "172.30.", "172.31."}
	for _, p := range privatePrefixes {
		if strings.HasPrefix(h, p) {
			return true
		}
	}
	return strings.HasSuffix(h, ".local")
}

/*
isPublicIP reports whether an IP is globally routable (i.e. NOT loopback,
private RFC1918/ULA, link-local, or unspecified). This is the pure classifier
behind the private_cluster_only DNS check, kept separate so it is unit-testable
without touching the resolver.
*/
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}

/*
hostResolvesPublic resolves host and reports whether it maps to a public IP.
Resolution is best-effort: a literal private IP or an unresolvable name (the
common offline-cluster case) returns false so private_cluster_only never breaks
a legitimately private/offline setup. It returns true only when resolution
SUCCEEDS and yields at least one globally routable address — the case worth
refusing under private_cluster_only.
*/
func hostResolvesPublic(host string) bool {
	h := lowerTrim(host)
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return isPublicIP(ip)
	}
	addrs, err := net.LookupHost(h)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && isPublicIP(ip) {
			return true
		}
	}
	return false
}
