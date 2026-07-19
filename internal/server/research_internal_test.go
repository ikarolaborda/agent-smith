package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/runner"
	"github.com/ikarolaborda/agent-smith/internal/research/sourcefetch"
)

const reviewerToken = "reviewer-token-0123456789-abcdef-0003"

type researchToolCaptureProvider struct {
	request llm.ChatRequest
}

type sourceHTTPDoerFunc func(*http.Request) (*http.Response, error)

func (function sourceHTTPDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (p *researchToolCaptureProvider) Name() string { return "capture" }

func (p *researchToolCaptureProvider) Chat(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.request = request
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (p *researchToolCaptureProvider) ChatStream(_ context.Context, request llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	p.request = request
	stream := make(chan llm.StreamChunk, 1)
	stream <- llm.StreamChunk{Done: true}
	close(stream)
	return stream, nil
}

func TestResearchChatRegistersPrincipalScopedQueryTool(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := privateDirForTest(workspace); err != nil {
		t.Fatal(err)
	}
	provider := &researchToolCaptureProvider{}
	srv, err := New(Options{
		Config:    &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}, Agent: config.AgentConfig{MaxIterations: 1}},
		Providers: map[string]llm.Provider{"fake": provider}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResearchMode: &ResearchModeOptions{Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{workspace},
			Credentials: []ResearchCredential{{Token: operatorToken, Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleAdmin}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	response := researchJSONRequest(srv, http.MethodPost, "/v1/chat/completions", operatorToken, map[string]any{
		"model": "fake/test", "stream": true, "messages": []map[string]string{{"role": "user", "content": "summarize the campaign"}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", response.Code, response.Body.String())
	}
	for _, definition := range provider.request.Tools {
		if definition.Name == "research_query" {
			return
		}
	}
	t.Fatalf("research_query missing from provider tools: %#v", provider.request.Tools)
}

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
		Operations: []domain.Operation{domain.OperationFuzz}, Limits: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxCPUSeconds: 30, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1},
	}
	response := researchJSONRequest(srv, http.MethodPost, "/v1/research/apparatuses", operatorToken, manifest)
	if response.Code != http.StatusCreated {
		t.Fatalf("apparatus status=%d body=%s", response.Code, response.Body.String())
	}
	scopeRequest := domain.AuthorizationScope{
		Purpose: "authorized API test", MemberIDs: []string{"reviewer"}, TargetRepository: "repo", AllowedRevisions: []string{"abc"},
		WorkspaceRoots: []string{workspace}, AllowedOperations: []domain.Operation{domain.OperationInspect, domain.OperationFuzz},
		ApprovalOperations: []domain.Operation{domain.OperationFuzz}, AllowedApparatusIDs: []string{manifest.ID},
		Budget: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1024, MaxCPUSeconds: 30, MaxDiskBytes: 1024, MaxInodes: 64, MaxPIDs: 8, MaxConcurrent: 1}, ExpiresAt: time.Now().UTC().Add(time.Hour),
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
	if eventResponse.Code != http.StatusOK || !strings.Contains(eventResponse.Body.String(), "event: campaign_state") || !strings.Contains(eventResponse.Body.String(), "campaign.transition") {
		t.Fatalf("events status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}
	if response = researchJSONRequest(srv, http.MethodGet, "/v1/research/audit/verify", operatorToken, nil); response.Code != http.StatusOK {
		t.Fatalf("audit status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResearchJobAPIBuildToMachineParsedCrash(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := privateDirForTest(workspace); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "fixture.cc"), []byte("int fixture = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision := initializeServerGitFixture(t, workspace)
	backend := &runner.FakeBackend{ExecuteFunc: func(_ context.Context, job domain.WorkerJob, staging string) (runner.Execution, error) {
		switch job.Operation {
		case domain.OperationBuild:
			if err := os.WriteFile(filepath.Join(staging, "fuzz_target"), []byte("fixture-binary"), 0o555); err != nil {
				return runner.Execution{}, err
			}
			if err := os.WriteFile(filepath.Join(staging, "build-provenance.json"), []byte(`{"compiler":"clang fixture"}`), 0o444); err != nil {
				return runner.Execution{}, err
			}
			return runner.Execution{Status: domain.RunCompleted}, nil
		case domain.OperationFuzz:
			if err := os.WriteFile(filepath.Join(staging, "crash-input"), []byte("SMIT"), 0o644); err != nil {
				return runner.Execution{}, err
			}
			log := "==1==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\nWRITE of size 1 at 0x1 thread T0\n    #0 0x51933a in LLVMFuzzerTestOneInput /source/fuzz_target.cc:16:38\nSUMMARY: AddressSanitizer: heap-buffer-overflow /source/fuzz_target.cc:16:38 in LLVMFuzzerTestOneInput\n"
			return runner.Execution{Status: domain.RunFailed, Stderr: []byte(log)}, nil
		case domain.OperationReproduce:
			log := "==2==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\nWRITE of size 1 at 0x1 thread T0\n    #0 0x51933a in LLVMFuzzerTestOneInput /source/fuzz_target.cc:16:38\nSUMMARY: AddressSanitizer: heap-buffer-overflow /source/fuzz_target.cc:16:38 in LLVMFuzzerTestOneInput\n"
			return runner.Execution{Status: domain.RunFailed, Stderr: []byte(log)}, nil
		case domain.OperationMinimize:
			if err := os.WriteFile(filepath.Join(staging, "minimized"), []byte("SMI"), 0o644); err != nil {
				return runner.Execution{}, err
			}
			log := "==3==ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\nWRITE of size 1 at 0x1 thread T0\n    #0 0x51933a in LLVMFuzzerTestOneInput /source/fuzz_target.cc:16:38\nSUMMARY: AddressSanitizer: heap-buffer-overflow /source/fuzz_target.cc:16:38 in LLVMFuzzerTestOneInput\n"
			return runner.Execution{Status: domain.RunFailed, Stderr: []byte(log)}, nil
		case domain.OperationSymbolize:
			if err := os.WriteFile(filepath.Join(staging, "symbolized.txt"), []byte("LLVMFuzzerTestOneInput\n/source/fuzz_target.cc:16:38\n"), 0o444); err != nil {
				return runner.Execution{}, err
			}
			return runner.Execution{Status: domain.RunCompleted}, nil
		default:
			return runner.Execution{Status: domain.RunCompleted}, nil
		}
	}}
	srv, err := New(Options{
		Config:    &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}},
		Providers: map[string]llm.Provider{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResearchMode: &ResearchModeOptions{Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{workspace}, RunnerBackend: backend,
			Credentials: []ResearchCredential{{Token: operatorToken, Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleAdmin}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	manifest := domain.ApparatusManifest{SchemaVersion: 1, ID: "apparatus", Name: "fixture", Version: "1", Engine: "libfuzzer", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sanitizers: []string{"address"}, Architectures: []string{"amd64"}, Harnesses: []domain.HarnessManifest{{Name: "parser", Binary: "/build/fuzz_target"}},
		Operations: []domain.Operation{domain.OperationBuild, domain.OperationFuzz, domain.OperationReproduce, domain.OperationMinimize, domain.OperationSymbolize}, Limits: domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1 << 20, MaxCPUSeconds: 30, MaxDiskBytes: 1 << 20, MaxInodes: 1024, MaxPIDs: 8, MaxConcurrent: 1}}
	if response := researchJSONRequest(srv, http.MethodPost, "/v1/research/apparatuses", operatorToken, manifest); response.Code != http.StatusCreated {
		t.Fatalf("apparatus status=%d body=%s", response.Code, response.Body.String())
	}
	scopeRequest := domain.AuthorizationScope{Purpose: "pipeline test", TargetRepository: "repo", AllowedRevisions: []string{revision, "HEAD"}, WorkspaceRoots: []string{workspace},
		AllowedOperations: []domain.Operation{domain.OperationAcquire, domain.OperationBuild, domain.OperationFuzz, domain.OperationReproduce, domain.OperationMinimize, domain.OperationSymbolize}, AllowedApparatusIDs: []string{manifest.ID}, Budget: manifest.Limits, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	response := researchJSONRequest(srv, http.MethodPost, "/v1/research/scopes", operatorToken, scopeRequest)
	if response.Code != http.StatusCreated {
		t.Fatalf("scope status=%d body=%s", response.Code, response.Body.String())
	}
	var scope domain.AuthorizationScope
	_ = json.Unmarshal(response.Body.Bytes(), &scope)
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns", operatorToken, domain.Campaign{ScopeID: scope.ID, Name: "pipeline"})
	if response.Code != http.StatusCreated {
		t.Fatalf("campaign status=%d body=%s", response.Code, response.Body.String())
	}
	var campaign domain.Campaign
	_ = json.Unmarshal(response.Body.Bytes(), &campaign)
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/transition", operatorToken, map[string]any{"state": domain.CampaignScoped})
	if response.Code != http.StatusOK {
		t.Fatalf("scope transition status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/target", operatorToken, map[string]any{
		"repository": "repo", "revision": "HEAD", "source_dir": workspace, "language": "c++", "architecture": "amd64", "correlation_id": "symbolic-acquire",
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "exact lowercase Git commit required") {
		t.Fatalf("symbolic target status=%d body=%s", response.Code, response.Body.String())
	}
	if err := os.WriteFile(filepath.Join(workspace, "untracked.cc"), []byte("untrusted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/target", operatorToken, map[string]any{
		"repository": "repo", "revision": revision, "source_dir": workspace, "language": "c++", "architecture": "amd64", "correlation_id": "dirty-acquire",
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "checkout content does not match requested commit") {
		t.Fatalf("dirty target status=%d body=%s", response.Code, response.Body.String())
	}
	if err := os.Remove(filepath.Join(workspace, "untracked.cc")); err != nil {
		t.Fatal(err)
	}
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/target", operatorToken, map[string]any{
		"repository": "repo", "revision": revision, "source_dir": workspace, "language": "c++", "architecture": "amd64", "correlation_id": "acquire-correlation",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("target acquisition status=%d body=%s", response.Code, response.Body.String())
	}
	jobURL := "/v1/research/campaigns/" + campaign.ID + "/jobs"
	response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
		"operation": "build", "harness": "parser", "revision": revision, "sanitizer": "address", "correlation_id": "build-correlation",
	}})
	if response.Code != http.StatusAccepted {
		t.Fatalf("build enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	var buildRun domain.ExperimentRun
	_ = json.Unmarshal(response.Body.Bytes(), &buildRun)
	if result, err := srv.research.broker.Wait(t.Context(), buildRun.ID); err != nil || result.Status != domain.RunCompleted {
		t.Fatalf("build result=%#v err=%v", result, err)
	}
	corpusPath := filepath.Join(srv.research.store.Root(), "worker-inputs", campaign.ID, "corpora", "parser")
	response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
		"operation": "fuzz", "harness": "parser", "revision": revision, "sanitizer": "address", "build_id": buildRun.ID,
	}})
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing correlation status=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(corpusPath); !os.IsNotExist(err) {
		t.Fatalf("unauthorized request materialized corpus path: %v", err)
	}
	response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
		"operation": "fuzz", "harness": "parser", "revision": revision, "sanitizer": "address", "build_id": buildRun.ID, "correlation_id": "fuzz-correlation", "arguments": map[string]string{"max-total-time": "1"},
	}})
	if response.Code != http.StatusAccepted {
		t.Fatalf("fuzz enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	var fuzzRun domain.ExperimentRun
	_ = json.Unmarshal(response.Body.Bytes(), &fuzzRun)
	if result, err := srv.research.broker.Wait(t.Context(), fuzzRun.ID); err != nil || result.Status != domain.RunFailed {
		t.Fatalf("fuzz result=%#v err=%v", result, err)
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID+"/builds", operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"compiler":"clang fixture"`) {
		t.Fatalf("build list status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID+"/crash-groups", operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "sha256:") {
		t.Fatalf("crash groups status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID, operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"crash_observed"`) {
		t.Fatalf("evidence state status=%d body=%s", response.Code, response.Body.String())
	}
	groups, err := srv.research.store.ListCrashGroups(t.Context(), campaign.ID, 10)
	if err != nil || len(groups) != 1 || groups[0].CanonicalInputID == "" {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
			"operation": "reproduce", "harness": "parser", "revision": revision, "sanitizer": "address", "build_id": buildRun.ID,
			"input_artifact_id": groups[0].CanonicalInputID, "correlation_id": fmt.Sprintf("reproduce-%d", attempt),
		}})
		if response.Code != http.StatusAccepted {
			t.Fatalf("reproduce enqueue status=%d body=%s", response.Code, response.Body.String())
		}
		var replayRun domain.ExperimentRun
		_ = json.Unmarshal(response.Body.Bytes(), &replayRun)
		if result, err := srv.research.broker.Wait(t.Context(), replayRun.ID); err != nil || result.Status != domain.RunFailed {
			t.Fatalf("reproduce result=%#v err=%v", result, err)
		}
	}
	response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
		"operation": "minimize", "harness": "parser", "revision": revision, "sanitizer": "address", "build_id": buildRun.ID,
		"input_artifact_id": groups[0].CanonicalInputID, "correlation_id": "minimize-correlation",
	}})
	if response.Code != http.StatusAccepted {
		t.Fatalf("minimize enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	var minimizeRun domain.ExperimentRun
	_ = json.Unmarshal(response.Body.Bytes(), &minimizeRun)
	if result, err := srv.research.broker.Wait(t.Context(), minimizeRun.ID); err != nil || result.Status != domain.RunFailed {
		t.Fatalf("minimize result=%#v err=%v", result, err)
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID, operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"minimized"`) {
		t.Fatalf("minimized state status=%d body=%s", response.Code, response.Body.String())
	}
	artifacts, err := srv.research.store.ListArtifacts(t.Context(), campaign.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var crashLogID string
	for _, artifact := range artifacts {
		if artifact.RunID == fuzzRun.ID && artifact.Role == "stderr_log" {
			crashLogID = artifact.ID
			break
		}
	}
	if crashLogID == "" {
		t.Fatal("fuzz crash log was not retained")
	}
	response = researchJSONRequest(srv, http.MethodPost, jobURL, operatorToken, map[string]any{"manifest_id": manifest.ID, "job": map[string]any{
		"operation": "symbolize", "harness": "parser", "revision": revision, "sanitizer": "address", "build_id": buildRun.ID,
		"input_artifact_id": crashLogID, "correlation_id": "symbolize-correlation",
	}})
	if response.Code != http.StatusAccepted {
		t.Fatalf("symbolize enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	var symbolizeRun domain.ExperimentRun
	_ = json.Unmarshal(response.Body.Bytes(), &symbolizeRun)
	if result, err := srv.research.broker.Wait(t.Context(), symbolizeRun.ID); err != nil || result.Status != domain.RunCompleted {
		t.Fatalf("symbolize result=%#v err=%v", result, err)
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID, operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"primitive_assessed"`) {
		t.Fatalf("primitive state status=%d body=%s", response.Code, response.Body.String())
	}
	response = researchJSONRequest(srv, http.MethodGet, "/v1/research/campaigns/"+campaign.ID+"/primitive-assessments", operatorToken, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"operation":"out_of_bounds_write"`) || !strings.Contains(response.Body.String(), `"attacker_control":{"known":false}`) {
		t.Fatalf("primitive list status=%d body=%s", response.Code, response.Body.String())
	}
}

func initializeServerGitFixture(t *testing.T, root string) string {
	t.Helper()
	for _, arguments := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.test"},
		{"config", "user.name", "Test"},
		{"add", "."},
		{"commit", "-q", "-m", "fixture"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func TestResearchAPIAcquiresOnlyAuthorizedPinnedSourceBundles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := privateDirForTest(workspace); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	content := []byte("int fetched = 1;\n")
	if err := writer.WriteHeader(&tar.Header{Name: "source.c", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	bundle := archive.Bytes()
	digest := sha256.Sum256(bundle)
	const revision = "0123456789abcdef0123456789abcdef01234567"
	const repositoryURL = "https://example.test/authorized.git"
	const bundleURL = "https://sources.example.test/authorized/source.tar"
	requests := 0
	doer := sourceHTTPDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bundle)), ContentLength: int64(len(bundle)), Request: request}, nil
	})
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifestNow := time.Now().UTC()
	signedManifest, err := sourcefetch.SignManifest(sourcefetch.Manifest{SchemaVersion: 1, IssuedAt: manifestNow, ExpiresAt: manifestNow.Add(time.Hour), Sources: []sourcefetch.Source{{Name: "mirror", Repository: repositoryURL, Bundles: []sourcefetch.Bundle{{Commit: revision, URL: bundleURL, SHA256: "sha256:" + hex.EncodeToString(digest[:])}}}}}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verifiedManifest, err := sourcefetch.VerifyManifest(signedManifest, publicKey, manifestNow)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Config: &config.Config{DefaultProvider: "fake", Providers: map[string]config.ProviderConfig{"fake": {Model: "test"}}}, Providers: map[string]llm.Provider{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResearchMode: &ResearchModeOptions{Enabled: true, DataDir: filepath.Join(root, "state"), WorkspaceRoots: []string{workspace},
			Credentials:    []ResearchCredential{{Token: operatorToken, Principal: domain.Principal{ID: "operator", Roles: []domain.Role{domain.RoleAdmin}}}},
			SourceManifest: verifiedManifest, SourceHTTPClient: doer,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if capabilities := srv.capabilityStatus(); capabilities["research_source_acquisition"] != true || capabilities["research_source_manifest_signed"] != true {
		t.Fatalf("source acquisition capabilities=%#v", capabilities)
	}

	createCampaign := func(name string, domains []string) domain.Campaign {
		t.Helper()
		budget := domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1 << 20, MaxCPUSeconds: 30, MaxDiskBytes: 1 << 20, MaxInodes: 100, MaxPIDs: 8, MaxConcurrent: 1}
		scopeRequest := domain.AuthorizationScope{Purpose: name, TargetRepository: repositoryURL, AllowedRevisions: []string{revision}, WorkspaceRoots: []string{workspace},
			AllowedOperations: []domain.Operation{domain.OperationAcquire}, AllowedDomains: domains, Budget: budget, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		response := researchJSONRequest(srv, http.MethodPost, "/v1/research/scopes", operatorToken, scopeRequest)
		if response.Code != http.StatusCreated {
			t.Fatalf("scope status=%d body=%s", response.Code, response.Body.String())
		}
		var scope domain.AuthorizationScope
		_ = json.Unmarshal(response.Body.Bytes(), &scope)
		response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns", operatorToken, domain.Campaign{ScopeID: scope.ID, Name: name})
		if response.Code != http.StatusCreated {
			t.Fatalf("campaign status=%d body=%s", response.Code, response.Body.String())
		}
		var campaign domain.Campaign
		_ = json.Unmarshal(response.Body.Bytes(), &campaign)
		response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+campaign.ID+"/transition", operatorToken, map[string]any{"state": domain.CampaignScoped})
		if response.Code != http.StatusOK {
			t.Fatalf("transition status=%d body=%s", response.Code, response.Body.String())
		}
		return campaign
	}

	request := map[string]any{"repository": repositoryURL, "revision": revision, "source_name": "mirror", "language": "c", "architecture": "amd64", "correlation_id": "fetch-source"}
	deniedCampaign := createCampaign("denied egress", nil)
	response := researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+deniedCampaign.ID+"/target", operatorToken, request)
	if response.Code != http.StatusForbidden || requests != 0 || !strings.Contains(response.Body.String(), "egress allowlist") {
		t.Fatalf("denied fetch status=%d requests=%d body=%s", response.Code, requests, response.Body.String())
	}

	allowedCampaign := createCampaign("allowed egress", []string{"sources.example.test"})
	request["correlation_id"] = "fetch-source-allowed"
	response = researchJSONRequest(srv, http.MethodPost, "/v1/research/campaigns/"+allowedCampaign.ID+"/target", operatorToken, request)
	if response.Code != http.StatusCreated || requests != 1 {
		t.Fatalf("fetch status=%d requests=%d body=%s", response.Code, requests, response.Body.String())
	}
	var target domain.TargetRevision
	_ = json.Unmarshal(response.Body.Bytes(), &target)
	if target.Commit != revision || target.Acquisition.Method != "https_pinned_tar" || target.Acquisition.SourceName != "mirror" || target.Acquisition.BundleSHA256 != "sha256:"+hex.EncodeToString(digest[:]) || target.Acquisition.ManifestKeyID != verifiedManifest.KeyID() || target.SourceSHA256 == "" {
		t.Fatalf("target=%#v", target)
	}
	captured, err := os.ReadFile(filepath.Join(srv.research.store.Root(), "worker-inputs", allowedCampaign.ID, "sources", target.ID, "source.c"))
	if err != nil || !bytes.Equal(captured, content) {
		t.Fatalf("captured=%q err=%v", captured, err)
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
