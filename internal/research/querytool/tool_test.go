package querytool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/service"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

func TestToolEnforcesMembershipAndHidesArtifactPaths(t *testing.T) {
	repository, svc, campaign := queryFixture(t)
	member := domain.Principal{ID: "member", Roles: []domain.Role{domain.RoleViewer}}
	tool, err := New(repository, svc, member)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"operation":"get","object_type":"campaign","id":"campaign"}`))
	if err != nil || !strings.Contains(result, `"state":"draft"`) {
		t.Fatalf("result=%q err=%v", result, err)
	}

	artifact, err := repository.PutArtifact(t.Context(), domain.Artifact{CampaignID: campaign.ID, Role: "crashing_input", MediaType: "application/octet-stream", Sensitivity: "embargoed"}, strings.NewReader("boom"))
	if err != nil {
		t.Fatal(err)
	}
	result, err = tool.Execute(t.Context(), json.RawMessage(`{"operation":"get","object_type":"artifact","id":"`+artifact.ID+`"}`))
	if err != nil || strings.Contains(result, repository.Root()) || strings.Contains(result, "blobs/") {
		t.Fatalf("artifact result=%q err=%v", result, err)
	}

	outsiderTool, err := New(repository, svc, domain.Principal{ID: "outsider", Roles: []domain.Role{domain.RoleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outsiderTool.Execute(t.Context(), json.RawMessage(`{"operation":"get","object_type":"campaign","id":"campaign"}`)); err == nil {
		t.Fatal("outsider read campaign metadata")
	}
}

func TestToolListsOnlyAuthorizedCampaignsAndRejectsInvalidShape(t *testing.T) {
	repository, svc, _ := queryFixture(t)
	tool, err := New(repository, svc, domain.Principal{ID: "member", Roles: []domain.Role{domain.RoleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateCampaign(t.Context(), domain.Campaign{ID: "other", ScopeID: "other-scope", Name: "hidden"}); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(t.Context(), json.RawMessage(`{"operation":"list","object_type":"campaign","limit":20}`))
	if err != nil || !strings.Contains(result, `"id":"campaign"`) || strings.Contains(result, `"id":"other"`) {
		t.Fatalf("result=%q err=%v", result, err)
	}
	for _, raw := range []string{
		`{"operation":"get","object_type":"campaign"}`,
		`{"operation":"list","object_type":"run"}`,
		`{"operation":"get","object_type":"campaign","id":"campaign","extra":true}`,
	} {
		if _, err := tool.Execute(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid arguments: %s", raw)
		}
	}
}

func TestToolBoundsModelOutput(t *testing.T) {
	repository, svc, campaign := queryFixture(t)
	campaign.Goal = strings.Repeat("x", maxQueryOutputBytes)
	campaign.Version++
	campaign.UpdatedAt = time.Now().UTC()
	if err := repository.UpdateCampaign(t.Context(), campaign, campaign.Version-1); err != nil {
		t.Fatal(err)
	}
	tool, err := New(repository, svc, domain.Principal{ID: "member", Roles: []domain.Role{domain.RoleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"operation":"get","object_type":"campaign","id":"campaign"}`)); err == nil || !strings.Contains(err.Error(), "bounded metadata response") {
		t.Fatalf("expected bounded response error, got %v", err)
	}
}

func queryFixture(t *testing.T) (*store.Store, *service.Service, domain.Campaign) {
	t.Helper()
	repository, err := store.Open(context.Background(), store.Config{Root: filepath.Join(t.TempDir(), "research")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	now := time.Now().UTC()
	budget := domain.ResourceBudget{MaxWallSeconds: 30, MaxMemoryBytes: 1 << 20, MaxCPUSeconds: 30, MaxDiskBytes: 1 << 20, MaxInodes: 1024, MaxPIDs: 8, MaxConcurrent: 1}
	for _, scope := range []domain.AuthorizationScope{
		{SchemaVersion: 1, ID: "scope", OperatorID: "operator", MemberIDs: []string{"member"}, Purpose: "query fixture", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{t.TempDir()}, AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: budget, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{SchemaVersion: 1, ID: "other-scope", OperatorID: "other", Purpose: "hidden fixture", TargetRepository: "repo", AllowedRevisions: []string{"abc"}, WorkspaceRoots: []string{t.TempDir()}, AllowedOperations: []domain.Operation{domain.OperationInspect}, Budget: budget, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	} {
		if err := repository.CreateScope(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	campaign, err := repository.CreateCampaign(t.Context(), domain.Campaign{SchemaVersion: 1, ID: "campaign", ScopeID: "scope", Name: "visible", Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(repository, 3)
	if err != nil {
		t.Fatal(err)
	}
	return repository, svc, campaign
}
