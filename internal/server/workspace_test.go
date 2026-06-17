package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

func postWorkspace(t *testing.T, base, path string) (*http.Response, string) {
	t.Helper()
	body := bytes.NewBufferString(`{"path":` + jsonString(path) + `}`)
	resp, err := http.Post(base+"/v1/workspace", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func getJSON(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestWorkspace_GetSetClear(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})

	resp, body := getJSON(t, ts.URL+"/v1/workspace")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"writable":false`) {
		t.Fatalf("default workspace should be empty/read-only: %d %s", resp.StatusCode, body)
	}

	dir := t.TempDir()
	resp, body = postWorkspace(t, ts.URL, dir)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"writable":true`) {
		t.Fatalf("opening a folder should make it writable: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, jsonString(dir)) {
		t.Fatalf("response should echo the opened folder: %s", body)
	}

	if _, b := getJSON(t, ts.URL+"/v1/workspace"); !strings.Contains(b, `"writable":true`) {
		t.Fatalf("GET should reflect the opened folder: %s", b)
	}

	resp, body = postWorkspace(t, ts.URL, "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"writable":false`) {
		t.Fatalf("empty path should clear the workspace: %d %s", resp.StatusCode, body)
	}
}

func TestWorkspace_RejectsBadPaths(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})

	if resp, _ := postWorkspace(t, ts.URL, filepath.Join(t.TempDir(), "does-not-exist")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("nonexistent folder should be rejected, got %d", resp.StatusCode)
	}

	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if resp, _ := postWorkspace(t, ts.URL, file); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a file (not dir) should be rejected, got %d", resp.StatusCode)
	}
}

func TestWorkspace_TreeListsAndFiltersNoise(t *testing.T) {
	ts := newTestServer(t, map[string]llm.Provider{"fake": &fakeProvider{}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if resp, _ := postWorkspace(t, ts.URL, dir); resp.StatusCode != http.StatusOK {
		t.Fatalf("open folder failed: %d", resp.StatusCode)
	}

	resp, body := getJSON(t, ts.URL+"/v1/workspace/tree")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tree status %d", resp.StatusCode)
	}
	if !strings.Contains(body, "main.go") {
		t.Errorf("tree should list main.go: %s", body)
	}
	if strings.Contains(body, "dep.js") || strings.Contains(body, "node_modules") {
		t.Errorf("tree must skip node_modules: %s", body)
	}
}
