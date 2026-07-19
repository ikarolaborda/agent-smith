package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const (
	operatorToken = "operator-token-0123456789-abcdef-0001"
	viewerToken   = "viewer-token-0123456789ab-abcdef-0002"
)

func TestResearchModeAuthenticationAndFixedWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	child := filepath.Join(allowed, "target")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{allowed, child, outside} {
		if err := privateDirForTest(dir); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(Options{
		Config:    &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}},
		Providers: map[string]llm.Provider{},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResearchMode: &ResearchModeOptions{
			Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{allowed},
			ArtifactEncryptionKeys: [][]byte{bytes.Repeat([]byte{0x41}, 32)},
			Credentials: []ResearchCredential{
				{Token: operatorToken, Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleOperator}}},
				{Token: viewerToken, Principal: domain.Principal{ID: "viewer", Roles: []domain.Role{domain.RoleViewer}}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if recorder := performRequest(srv, http.MethodGet, "/healthz", "", nil); recorder.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := performRequest(srv, http.MethodGet, "/v1/models", "", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := performRequest(srv, http.MethodGet, "/v1/models", "invalid-invalid-invalid-invalid-invalid", nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := performRequest(srv, http.MethodGet, "/v1/models", viewerToken, nil); recorder.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := postWorkspaceRequest(srv, viewerToken, child); recorder.Code != http.StatusForbidden {
		t.Fatalf("viewer workspace status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := postWorkspaceRequest(srv, operatorToken, outside); recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "workspace_out_of_scope") {
		t.Fatalf("outside workspace status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := postWorkspaceRequest(srv, operatorToken, child); recorder.Code != http.StatusOK {
		t.Fatalf("allowed workspace status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := srv.getWorkspace(); got != child {
		t.Fatalf("workspace=%q want %q", got, child)
	}
	capabilities := srv.capabilityStatus()
	for _, key := range []string{"authentication", "research_mode", "artifact_persistence", "research_persistence", "research_artifact_encryption"} {
		if capabilities[key] != true {
			t.Fatalf("capability %s=%v", key, capabilities[key])
		}
	}
}

func TestResearchModeFailsClosedWithoutCredentialOrRoot(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}}
	if _, err := New(Options{Config: cfg, ResearchMode: &ResearchModeOptions{Enabled: true, DataDir: t.TempDir()}}); err == nil {
		t.Fatal("research mode started without roots or credentials")
	}
	root := t.TempDir()
	if _, err := New(Options{Config: cfg, ResearchMode: &ResearchModeOptions{
		Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{root},
		Credentials: []ResearchCredential{{Token: "short", Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleAdmin}}}},
	}}); err == nil {
		t.Fatal("research mode accepted weak bearer token")
	}
}

func performRequest(srv *Server, method, path, token string, body *bytes.Reader) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = body
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	return recorder
}

func postWorkspaceRequest(srv *Server, token, path string) *httptest.ResponseRecorder {
	body := bytes.NewBufferString(`{"path":"` + path + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/workspace", body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, request)
	return recorder
}

func privateDirForTest(path string) error {
	return os.MkdirAll(path, 0o700)
}
