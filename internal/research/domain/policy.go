package domain

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

/* Action is one concrete requested research side effect. */
type Action struct {
	Principal       Principal
	Operation       Operation
	Repository      string
	Revision        string
	WorkspacePath   string
	DestinationHost string
	WallSeconds     int64
	MemoryBytes     int64
	DiskBytes       int64
	PIDs            int64
	ApprovalID      string
}

/* PolicyDecision is a fail-closed authorization result. */
type PolicyDecision struct {
	Allowed          bool   `json:"allowed"`
	ApprovalRequired bool   `json:"approval_required"`
	Reason           string `json:"reason"`
}

/* Validate checks the intrinsic validity and lifetime of a scope. */
func (s AuthorizationScope) Validate(now time.Time) error {
	if s.ID == "" || s.OperatorID == "" || strings.TrimSpace(s.Purpose) == "" {
		return errors.New("research: scope id, operator, and purpose are required")
	}
	if s.TargetRepository == "" || len(s.AllowedRevisions) == 0 || len(s.WorkspaceRoots) == 0 {
		return errors.New("research: scope target, revisions, and workspace roots are required")
	}
	if len(s.AllowedOperations) == 0 {
		return errors.New("research: scope must allow at least one operation")
	}
	allowed := make(map[Operation]bool, len(s.AllowedOperations))
	for _, operation := range s.AllowedOperations {
		if !IsKnownOperation(operation) || allowed[operation] {
			return errors.New("research: scope contains an unknown or duplicate operation")
		}
		allowed[operation] = true
	}
	for _, operation := range s.ApprovalOperations {
		if !IsKnownOperation(operation) || !allowed[operation] {
			return errors.New("research: approval operation must be a known allowed operation")
		}
	}
	if s.ExpiresAt.IsZero() {
		return errors.New("research: scope expiry is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.Before(s.ExpiresAt) {
		return errors.New("research: authorization scope expired")
	}
	if s.RevokedAt != nil && !now.Before(*s.RevokedAt) {
		return errors.New("research: authorization scope revoked")
	}
	return nil
}

/* Authorize validates actor, target, path, egress, budgets, and approval. */
func (s AuthorizationScope) Authorize(action Action, now time.Time) PolicyDecision {
	deny := func(reason string) PolicyDecision { return PolicyDecision{Reason: reason} }
	if err := s.Validate(now); err != nil {
		return deny(err.Error())
	}
	if action.Principal.ID == "" {
		return deny("research: authenticated principal required")
	}
	if !IsKnownOperation(action.Operation) {
		return deny("research: unknown operation")
	}
	if !principalMay(action.Principal, action.Operation) {
		return deny("research: principal role does not permit operation")
	}
	if action.Repository != s.TargetRepository {
		return deny("research: repository is outside authorization scope")
	}
	if !containsString(s.AllowedRevisions, action.Revision) {
		return deny("research: revision is outside authorization scope")
	}
	if !containsOperation(s.AllowedOperations, action.Operation) {
		return deny("research: operation is outside authorization scope")
	}
	if action.WorkspacePath != "" && !insideAnyRoot(action.WorkspacePath, s.WorkspaceRoots) {
		return deny("research: workspace path is outside authorized roots")
	}
	if action.DestinationHost != "" && !hostAllowed(action.DestinationHost, s.AllowedDomains) {
		return deny("research: destination host is outside egress allowlist")
	}
	if exceeds(action.WallSeconds, s.Budget.MaxWallSeconds) || exceeds(action.MemoryBytes, s.Budget.MaxMemoryBytes) || exceeds(action.DiskBytes, s.Budget.MaxDiskBytes) || exceeds(action.PIDs, s.Budget.MaxPIDs) {
		return deny("research: requested resources exceed authorization budget")
	}
	needsApproval := containsOperation(s.ApprovalOperations, action.Operation)
	if needsApproval && action.ApprovalID == "" {
		return PolicyDecision{ApprovalRequired: true, Reason: "research: human approval required"}
	}
	return PolicyDecision{Allowed: true, Reason: "authorized"}
}

func principalMay(p Principal, op Operation) bool {
	for _, role := range p.Roles {
		switch role {
		case RoleAdmin:
			return true
		case RoleReviewer:
			if op == OperationDraftReport || op == OperationDisclose {
				return true
			}
		case RoleOperator:
			if op != OperationDisclose {
				return true
			}
		case RoleAnalyst:
			if op == OperationInspect || op == OperationStaticAnalyze || op == OperationCoverage || op == OperationDraftReport {
				return true
			}
		case RoleViewer:
			if op == OperationInspect {
				return true
			}
		}
	}
	return false
}

func containsOperation(xs []Operation, want Operation) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func insideAnyRoot(path string, roots []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	for _, root := range roots {
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(r); err == nil {
			r = real
		}
		rel, err := filepath.Rel(r, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func hostAllowed(host string, allowed []string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	for _, raw := range allowed {
		a := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:]) && host != a[2:]) {
			return true
		}
	}
	return false
}

func exceeds(requested, maximum int64) bool {
	return requested < 0 || (maximum > 0 && requested > maximum)
}

/* ValidatePrimitive ensures every asserted primitive dimension cites evidence. */
func ValidatePrimitive(p PrimitiveAssessment) error {
	if p.ID == "" || p.CampaignID == "" || p.CrashGroupID == "" || p.Operation == "" || len(p.OperationEvidence) == 0 {
		return errors.New("research: primitive identity, crash group, operation, and operation evidence required")
	}
	switch p.Operation {
	case PrimitiveCrash, PrimitiveOOBRead, PrimitiveOOBWrite, PrimitiveUseAfterFree, PrimitiveTypeConfusion, PrimitiveInvalidFree, PrimitiveControlData, PrimitiveOther:
	default:
		return errors.New("research: unknown primitive operation")
	}
	if !validEvidenceIDs(p.OperationEvidence) {
		return errors.New("research: primitive operation evidence contains an empty or duplicate id")
	}
	fields := map[string]EvidenceValue{
		"attacker_control":   p.AttackerControl,
		"access_width":       p.AccessWidth,
		"value_control":      p.ValueControl,
		"target_relation":    p.TargetRelation,
		"repeatability":      p.Repeatability,
		"reachability":       p.Reachability,
		"mitigations":        p.Mitigations,
		"exploitability_gap": p.ExploitabilityGap,
	}
	for name, field := range fields {
		if field.Known && (strings.TrimSpace(field.Value) == "" || !validEvidenceIDs(field.EvidenceIDs)) {
			return fmt.Errorf("research: primitive field %s is known without evidence", name)
		}
		if !field.Known && (field.Value != "" || len(field.EvidenceIDs) != 0) {
			return fmt.Errorf("research: primitive field %s is unknown but carries a claim", name)
		}
	}
	return nil
}

func validEvidenceIDs(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
