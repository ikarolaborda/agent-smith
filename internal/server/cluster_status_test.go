package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
fakeClusterProvider is a fakeProvider that also exposes Status, matching the
interface /v1/cluster type-asserts. Registered under the "cluster" key so the
handler finds it.
*/
type fakeClusterProvider struct {
	fakeProvider
}

func (f *fakeClusterProvider) Name() string { return "cluster" }

func (f *fakeClusterProvider) Status(context.Context) *cluster.ClusterStatus {
	return &cluster.ClusterStatus{
		Enabled:         true,
		Mode:            "llama_cpp_rpc",
		SelectedBackend: "llama_cpp_rpc",
		Model:           "qwen2.5-72b",
		Nodes: []cluster.ClusterNodeStatus{
			{ID: "m5max", Role: "coordinator", Reachable: true},
			{ID: "m5pro", Role: "worker", Reachable: true},
		},
	}
}

/* listingClusterProvider exposes ListModels so /v1/models surfaces per-model cluster entries. */
type listingClusterProvider struct {
	fakeProvider
}

func (l *listingClusterProvider) Name() string { return "cluster" }

func (l *listingClusterProvider) ListModels() []cluster.ModelInfo {
	return []cluster.ModelInfo{
		{ID: "huihui-moe-60b-abliterated", ContextTokens: 32768, IsDefault: true},
		{ID: "qwen2.5-72b-abliterated", ContextTokens: 8192},
	}
}

/*
A cluster provider declares its models in the cluster YAML, not the app config.
The picker must list one "cluster/<id>" entry per declared model so the clustered
model is selectable from the web UI — not a single empty "cluster/" entry.
*/
func TestServer_Models_ListsClusterModels(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"cluster": &listingClusterProvider{}})
	resp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"cluster/huihui-moe-60b-abliterated"`, `"cluster/qwen2.5-72b-abliterated"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("models list missing cluster model %q: %s", want, body)
		}
	}
	if strings.Contains(string(body), `"cluster/"`) {
		t.Fatalf("models list contains the empty cluster entry: %s", body)
	}
}

func TestServer_ClusterStatus_Enabled(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"cluster": &fakeClusterProvider{}})
	resp, err := http.Get(ts.URL + "/v1/cluster")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"enabled":true`, `"selected_backend":"llama_cpp_rpc"`, `"m5pro"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("cluster status missing %q: %s", want, body)
		}
	}
}

func TestServer_ClusterStatus_FallbackWhenNoCluster(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	resp, err := http.Get(ts.URL + "/v1/cluster")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("expected enabled:false without a cluster provider: %s", body)
	}
}
