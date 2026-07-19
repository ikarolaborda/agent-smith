package domain

import (
	"path/filepath"
	"testing"
	"time"
)

func scopeFixture(root string) AuthorizationScope {
	return AuthorizationScope{
		ID: "scope", OperatorID: "owner", Purpose: "authorized fixture research",
		TargetRepository: "https://example.test/project.git", AllowedRevisions: []string{"abc123"},
		WorkspaceRoots: []string{root}, AllowedOperations: []Operation{OperationInspect, OperationFuzz, OperationDisclose},
		ApprovalOperations: []Operation{OperationFuzz, OperationDisclose}, AllowedDomains: []string{"example.test", "*.example.test"},
		Budget:    ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1024, MaxDiskBytes: 2048, MaxPIDs: 32},
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour), DisclosureContact: "security@example.test",
	}
}

func TestScopeAuthorizeEnforcesPathBudgetAndApproval(t *testing.T) {
	root := t.TempDir()
	s := scopeFixture(root)
	principal := Principal{ID: "operator", Roles: []Role{RoleOperator}}
	a := Action{Principal: principal, Operation: OperationFuzz, Repository: s.TargetRepository, Revision: "abc123", WorkspacePath: filepath.Join(root, "target"), WallSeconds: 30, MemoryBytes: 512}
	d := s.Authorize(a, time.Now())
	if d.Allowed || !d.ApprovalRequired {
		t.Fatalf("missing approval decision = %#v", d)
	}
	a.ApprovalID = "approval"
	if d = s.Authorize(a, time.Now()); !d.Allowed {
		t.Fatalf("authorized action denied: %#v", d)
	}
	a.WorkspacePath = filepath.Join(root, "..", "escape")
	if d = s.Authorize(a, time.Now()); d.Allowed {
		t.Fatalf("path escape authorized: %#v", d)
	}
}

func TestScopeAuthorizeEnforcesRolesAndEgress(t *testing.T) {
	root := t.TempDir()
	s := scopeFixture(root)
	base := Action{Principal: Principal{ID: "viewer", Roles: []Role{RoleViewer}}, Operation: OperationInspect, Repository: s.TargetRepository, Revision: "abc123", WorkspacePath: root}
	if d := s.Authorize(base, time.Now()); !d.Allowed {
		t.Fatalf("viewer inspect denied: %#v", d)
	}
	base.DestinationHost = "metadata.internal"
	if d := s.Authorize(base, time.Now()); d.Allowed {
		t.Fatalf("unlisted egress allowed: %#v", d)
	}
	base.DestinationHost = "api.example.test:443"
	if d := s.Authorize(base, time.Now()); !d.Allowed {
		t.Fatalf("allowed subdomain denied: %#v", d)
	}
	base.Operation = OperationDisclose
	base.ApprovalID = "approval"
	if d := s.Authorize(base, time.Now()); d.Allowed {
		t.Fatalf("viewer disclosure allowed: %#v", d)
	}
}

func TestUnknownOperationsFailClosed(t *testing.T) {
	scope := scopeFixture(t.TempDir())
	scope.AllowedOperations = append(scope.AllowedOperations, Operation("model_shell"))
	if err := scope.Validate(time.Now().UTC()); err == nil {
		t.Fatal("scope accepted unknown operation")
	}
}
