package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const reviewerToken = "reviewer-token-0123456789-abcdef-0003"

func TestResearchCampaignAPIAndResumableEvents(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := privateDirForTest(workspace); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config:    &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}},
		Providers: map[string]llm.Provider{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResearchMode: &ResearchModeOptions{
			Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{workspace},
			Credentials: []ResearchCredential{
				{Token: operatorToken, Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleAdmin}}},
				{Token: reviewerToken, Principal: domain.Principal{ID: "reviewer", Roles: []domain.Role{domain.RoleReviewer}}},
				{Token: viewerToken, Principal: domain.Principal{ID: "outsider", Roles: []domain.Role{domain.RoleViewer}}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	manifest := domain.ApparatusManifest{
		SchemaVersion: 1, ID: "apparatus", Name: "fixture", Version: "1", Engine: "libfuzzer",
		ImageDigest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sanitizers:    []string{"address"},
		Architectures: []string{"amd64"}, Harnesses: []domain.HarnessManifest{{Name: "parser", Binary: "/build/fuzz_target"}},
		Operations: []domain.Operation{domain.OperationFuzz}, Limits: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 8},
	}
	response := researchJSONRequest(srv, http.MethodPost, "/v1/research/apparatuses", operatorToken, manifest)
	if response.Code != http.StatusCreated {
		t.Fatalf("apparatus status=%d body=%s", response.Code, response.Body.String())
	}
	scopeRequest := domain.AuthorizationScope{
		Purpose: "authorized API test", MemberIDs: []string{"reviewer"}, TargetRepository: "repo", AllowedRevisions: []string{"abc"},
		WorkspaceRoots: []string{workspace}, AllowedOperations: []domain.Operation{domain.OperationInspect, domain.OperationFuzz},
		ApprovalOperations: []domain.Operation{domain.OperationFuzz}, AllowedApparatusIDs: []string{manifest.ID},
		Budget: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxDiskBytes: 1024, MaxPIDs: 8}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/scopes", operatorToken, scopeRequest)
	if response.Code != http.StatusCreated {
		t.Fatalf("scope status=%d body=%s", response.Code, response.Body.String())
	}
	var scope domain.AuthorizationScope
	if err := json.Unmarshal(response.Body.Bytes(), &scope); err != nil {
		t.Fatal(err)
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns", operatorToken, domain.Campaign{ScopeID: scope.ID, Name: "fixture campaign"})
	if response.Code != http.StatusCreated {
		t.Fatalf("campaign status=%d body=%s", response.Code, response.Body.String())
	}
	var campaign domain.Campaign
	if err := json.Unmarshal(response.Body.Bytes(), &campaign); err != nil {
		t.Fatal(err)
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/transition", operatorToken, map[string]any{"state": domain.CampaignScoped})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"scoped"`) {
		t.Fatalf("transition status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/transition", operatorToken, map[string]any{"state": domain.CampaignAcquired})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "evidence_managed_transition") {
		t.Fatalf("managed transition status=%d body=%s", response.Code, response.Body.String())
	}
	if response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID, viewerToken, nil); response.Code != http.StatusForbidden {
		t.Fatalf("outsider status=%d body=%s", response.Code, response.Body.String())
	}
	if response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID, reviewerToken, nil); response.Code != http.StatusOK {
		t.Fatalf("member status=%d body=%s", response.Code, response.Body.String())
	}

	artifact, err := srv.research.store.PutArtifact(t.Context(), domain.Artifact{CampaignID: campaign.ID, Role: "crashing_input", MediaType: "application/octet-stream", Sensitivity: "embargoed"}, strings.NewReader("boom"))
	if err != nil {
		t.Fatal(err)
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/artifacts/"+artifact.ID, reviewerToken, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"storage_path":"blobs`) {
		t.Fatalf("artifact metadata status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/artifacts/"+artifact.ID+"?download=1", reviewerToken, nil)
	if response.Code != http.StatusOK || response.Body.String() != "boom" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("artifact download status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}

	eventRequest := httptest.NewRequest(http.MethodGet, "/v1/research/events?campaign_id="+campaign.ID+"&after=0", nil)
	eventRequest.Header.Set("Authorization", "Bearer "+operatorToken)
	eventRequest.Header.Set("Accept", "text/event-stream")
	eventResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusOK || !strings.Contains(eventResponse.Body.String(), "event: audit") || !strings.Contains(eventResponse.Body.String(), "campaign.transition") {
		t.Fatalf("events status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}
	if response = researchJSONRequest(srv, http.MethodGet, "/v1/research/audit/verify", operatorToken, nil); response.Code != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", response.Code, response.Body.String())
	}
}

func researchJSONRequest(srv *Server, method, path, token string, value any) *httptest.ResponseRecorder {
	var body io.Reader
	if value != nil {
		data, _ := json.Marshal(value)
		body = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	return response
}
