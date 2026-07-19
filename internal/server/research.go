package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/acquisition"
	"github.com/ikarolaborda/agent-smith/internal/research/apparatus"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/pipeline"
	"github.com/ikarolaborda/agent-smith/internal/research/service"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

const maxResearchBodyBytes = 1 << 20

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request) {
	if s.research == nil {
		writeError(w, http.StatusNotFound, "research_mode_disabled", "research mode is not enabled")
		return
	}
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authenticated principal required")
		return
	}
	path := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/research"), "/"), "/")
	segments := strings.Split(path, "/")
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"object": "research_control_plane", "schema_version": 1})
		return
	}
	switch segments[0] {
	case "scopes":
		s.handleResearchScopes(w, r, principal, segments[1:])
	case "campaigns":
		s.handleResearchCampaigns(w, r, principal, segments[1:])
	case "apparatuses":
		s.handleResearchApparatuses(w, r, principal, segments[1:])
	case "approvals":
		s.handleResearchApproval(w, r, principal, segments[1:])
	case "runs":
		s.handleResearchRun(w, r, principal, segments[1:])
	case "artifacts":
		s.handleResearchArtifact(w, r, principal, segments[1:])
	case "events":
		s.handleResearchEvents(w, r, principal)
	case "audit":
		s.handleResearchAudit(w, r, principal, segments[1:])
	default:
		writeError(w, http.StatusNotFound, "not_found", "research route not found")
	}
}

func (s *Server) handleResearchApparatuses(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			values, err := s.research.store.ListApparatus(r.Context(), queryLimit(r, 100))
			s.writeResearchList(w, values, err)
		case http.MethodPost:
			var manifest domain.ApparatusManifest
			if !decodeJSONRequest(w, r, &manifest, maxResearchBodyBytes) {
				return
			}
			created, err := s.research.service.RegisterApparatus(r.Context(), principal, manifest)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		}
		return
	}
	if len(rest) != 1 || r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "not_found", "apparatus route not found")
		return
	}
	manifest, err := s.research.store.GetApparatus(r.Context(), rest[0])
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleResearchScopes(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			scopes, err := s.research.store.ListScopes(r.Context(), queryLimit(r, 100))
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			filtered := scopes[:0]
			for _, scope := range scopes {
				if principalHasAnyRole(principal, domain.RoleAdmin) || scope.OperatorID == principal.ID || containsID(scope.MemberIDs, principal.ID) {
					filtered = append(filtered, scope)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": filtered})
		case http.MethodPost:
			var scope domain.AuthorizationScope
			if !decodeJSONRequest(w, r, &scope, maxResearchBodyBytes) {
				return
			}
			created, err := s.research.service.CreateScope(r.Context(), principal, scope)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		}
		return
	}
	if len(rest) != 1 || r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	scope, err := s.research.store.GetScope(r.Context(), rest[0])
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	if !principalHasAnyRole(principal, domain.RoleAdmin) && scope.OperatorID != principal.ID && !containsID(scope.MemberIDs, principal.ID) {
		writeError(w, http.StatusForbidden, "forbidden", "principal is not a scope member")
		return
	}
	writeJSON(w, http.StatusOK, scope)
}

func (s *Server) handleResearchCampaigns(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			campaigns, err := s.research.store.ListCampaigns(r.Context(), queryLimit(r, 100))
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			filtered := campaigns[:0]
			for _, campaign := range campaigns {
				allowed, _ := s.research.service.CanReadCampaign(r.Context(), principal, campaign.ID)
				if allowed {
					filtered = append(filtered, campaign)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": filtered})
		case http.MethodPost:
			var campaign domain.Campaign
			if !decodeJSONRequest(w, r, &campaign, maxResearchBodyBytes) {
				return
			}
			created, err := s.research.service.CreateCampaign(r.Context(), principal, campaign)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		}
		return
	}
	campaignID := rest[0]
	allowed, err := s.research.service.CanReadCampaign(r.Context(), principal, campaignID)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "principal is not a campaign member")
		return
	}
	if len(rest) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		campaign, err := s.research.store.GetCampaign(r.Context(), campaignID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, campaign)
		return
	}
	switch rest[1] {
	case "transition":
		s.handleResearchTransition(w, r, principal, campaignID)
	case "target":
		s.handleResearchTarget(w, r, principal, campaignID)
	case "targets":
		s.handleResearchTargets(w, r, principal, campaignID)
	case "approvals":
		s.handleCampaignApprovals(w, r, principal, campaignID)
	case "runs":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListRuns(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "builds":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListBuilds(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "artifacts":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListArtifacts(r.Context(), campaignID, queryLimit(r, 1000))
		for index := range values {
			values[index].StoragePath = ""
		}
		s.writeResearchList(w, values, err)
	case "crash-groups":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListCrashGroups(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "crash-observations":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListCrashes(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "primitive-assessments":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListPrimitives(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "findings":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
			return
		}
		values, err := s.research.store.ListFindings(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case "revision-checks":
		s.handleResearchRevisionChecks(w, r, principal, campaignID)
	case "branch-review":
		s.handleResearchBranchReview(w, r, principal, campaignID)
	case "source-evidence":
		s.handleResearchSourceEvidence(w, r, campaignID)
	case "lookups":
		s.handleResearchLookups(w, r, principal, campaignID)
	case "source-reviews":
		s.handleResearchSourceReviews(w, r, principal, campaignID)
	case "novelty-review":
		s.handleResearchNoveltyReview(w, r, principal, campaignID)
	case "candidate-patches":
		s.handleResearchCandidatePatches(w, r, principal, campaignID)
	case "remediations":
		s.handleResearchRemediations(w, r, principal, campaignID)
	case "reports":
		s.handleResearchReports(w, r, principal, campaignID)
	case "disclosures":
		s.handleResearchDisclosures(w, r, principal, campaignID)
	case "jobs":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
			return
		}
		var request struct {
			ManifestID string               `json:"manifest_id"`
			Job        apparatus.JobRequest `json:"job"`
			ApprovalID string               `json:"approval_id"`
		}
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		campaign, err := s.research.store.GetCampaign(r.Context(), campaignID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		manifest, err := s.research.store.GetApparatus(r.Context(), request.ManifestID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		preflightBudget := request.Job.Budget
		if preflightBudget == (domain.ResourceBudget{}) {
			preflightBudget = manifest.Limits
		}
		selectedTargetID := request.Job.TargetID
		if request.Job.BuildID != "" {
			selectedBuild, buildErr := s.research.store.GetBuild(r.Context(), request.Job.BuildID)
			if buildErr != nil {
				s.writeResearchError(w, buildErr)
				return
			}
			if selectedBuild.CampaignID != campaign.ID || selectedTargetID != "" && selectedTargetID != selectedBuild.TargetID {
				writeError(w, http.StatusBadRequest, "invalid_target", "build and requested target do not match this campaign")
				return
			}
			selectedTargetID = selectedBuild.TargetID
		}
		if selectedTargetID == "" {
			selectedTargetID = campaign.TargetID
		}
		var selectedTarget domain.TargetRevision
		if selectedTargetID != "" {
			selectedTarget, err = s.research.store.GetTarget(r.Context(), selectedTargetID)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
			if selectedTarget.CampaignID != campaign.ID || request.Job.Revision != selectedTarget.Commit {
				writeError(w, http.StatusBadRequest, "invalid_target", "job revision does not match its campaign-owned captured target")
				return
			}
		}
		if err := s.research.service.PreauthorizeEnqueue(r.Context(), principal, campaignID, request.Job.Operation, request.Job.Revision, preflightBudget, request.ApprovalID, request.Job.CorrelationID); err != nil {
			s.writeResearchError(w, err)
			return
		}
		request.Job.CampaignID = campaign.ID
		request.Job.ScopeID = campaign.ScopeID
		request.Job.TargetID = selectedTargetID
		request.Job.SourceDir = ""
		if selectedTarget.ID != "" {
			request.Job.SourceDir, err = acquisition.VerifiedCapture(
				pipeline.WorkRoot(s.research.store.Root()), campaign.ID, selectedTarget.ID, selectedTarget.SourceSHA256,
				acquisition.Limits{MaxFiles: campaign.Budget.MaxInodes, MaxBytes: campaign.Budget.MaxDiskBytes},
			)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
		}
		request.Job.BuildDir = ""
		if request.Job.BuildID != "" {
			request.Job.BuildDir, err = pipeline.VerifiedBuildDirectory(r.Context(), s.research.store, campaign.ID, request.Job.BuildID)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
		}
		request.Job.CorpusDir = ""
		if researchJobNeedsCorpus(request.Job) {
			request.Job.CorpusDir, err = pipeline.PrepareCorpus(s.research.store.Root(), campaign.ID, request.Job.Harness)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
		}
		request.Job.EvidenceDir = ""
		if request.Job.InputArtifactID != "" {
			request.Job.EvidenceDir, err = pipeline.PrepareEvidence(r.Context(), s.research.store, campaign.ID, request.Job.InputArtifactID, request.Job.Operation)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
		}
		request.Job.PatchDir = ""
		if request.Job.PatchArtifactID != "" {
			request.Job.PatchDir, err = pipeline.PrepareEvidence(r.Context(), s.research.store, campaign.ID, request.Job.PatchArtifactID, domain.OperationBuild)
			if err != nil {
				s.writeResearchError(w, err)
				return
			}
		}
		job, err := apparatus.NewJob(manifest, request.Job)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		run, err := s.research.service.Enqueue(r.Context(), principal, campaignID, job, request.ApprovalID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	default:
		writeError(w, http.StatusNotFound, "not_found", "campaign subresource not found")
	}
}

func (s *Server) handleResearchTargets(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.research.store.ListTargets(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case http.MethodPost:
		var request struct {
			service.TargetRequest
			FindingID string `json:"finding_id"`
		}
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		target, err := s.research.service.AcquireComparisonTarget(r.Context(), principal, campaignID, request.FindingID, request.TargetRequest)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, target)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchTarget(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		campaign, err := s.research.store.GetCampaign(r.Context(), campaignID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		if campaign.TargetID == "" {
			writeError(w, http.StatusNotFound, "not_found", "campaign target not acquired")
			return
		}
		target, err := s.research.store.GetTarget(r.Context(), campaign.TargetID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, target)
	case http.MethodPost:
		var request service.TargetRequest
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		target, err := s.research.service.AcquireTarget(r.Context(), principal, campaignID, request)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, target)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchTransition(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request struct {
		State domain.CampaignState `json:"state"`
	}
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	facts := domain.EvidenceFacts{}
	if request.State == domain.CampaignScoped {
		campaign, err := s.research.store.GetCampaign(r.Context(), campaignID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		scope, err := s.research.store.GetScope(r.Context(), campaign.ScopeID)
		facts.ScopeValid = err == nil && scope.Validate(time.Now().UTC()) == nil
	} else if request.State != domain.CampaignPaused && request.State != domain.CampaignCancelled && request.State != domain.CampaignFailed {
		writeError(w, http.StatusConflict, "evidence_managed_transition", "this state is advanced only by the evidence pipeline")
		return
	}
	campaign, err := s.research.service.Transition(r.Context(), principal, campaignID, request.State, facts)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, campaign)
}

func (s *Server) handleCampaignApprovals(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.research.store.ListApprovals(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case http.MethodPost:
		var approval domain.Approval
		if !decodeJSONRequest(w, r, &approval, maxResearchBodyBytes) {
			return
		}
		approval.CampaignID = campaignID
		campaign, err := s.research.store.GetCampaign(r.Context(), campaignID)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		approval.ScopeID = campaign.ScopeID
		created, err := s.research.service.RequestApproval(r.Context(), principal, approval)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchApproval(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) != 2 || rest[1] != "decision" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "approval decision route not found")
		return
	}
	var request struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	approval, err := s.research.service.DecideApproval(r.Context(), principal, rest[0], request.Approved, request.Reason)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) handleResearchRun(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) < 1 || len(rest) > 2 {
		writeError(w, http.StatusNotFound, "not_found", "run route not found")
		return
	}
	run, err := s.research.store.GetRun(r.Context(), rest[0])
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	allowed, err := s.research.service.CanReadCampaign(r.Context(), principal, run.CampaignID)
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "principal is not a campaign member")
		return
	}
	if len(rest) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, run)
		return
	}
	if len(rest) != 2 || rest[1] != "cancel" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "run route not found")
		return
	}
	if !principalHasAnyRole(principal, domain.RoleOperator) {
		writeError(w, http.StatusForbidden, "forbidden", "operator or admin role required")
		return
	}
	if s.research.broker == nil {
		writeError(w, http.StatusServiceUnavailable, "runner_unavailable", "research runner unavailable")
		return
	}
	if err := s.research.broker.CancelRun(r.Context(), run.ID); err != nil {
		s.writeResearchError(w, err)
		return
	}
	_, _ = s.research.store.AppendAudit(r.Context(), domain.AuditEvent{ActorID: principal.ID, Action: "run.cancel", ResourceType: "experiment_run", ResourceID: run.ID, Decision: "allowed", Details: map[string]string{"campaign_id": run.CampaignID}})
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": run.ID, "cancellation_requested": true})
}

func (s *Server) handleResearchArtifact(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) == 2 && rest[1] == "purge" && r.Method == http.MethodPost {
		var request struct {
			ApprovalID string `json:"approval_id"`
			Reason     string `json:"reason"`
		}
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		artifact, err := s.research.service.PurgeArtifact(r.Context(), principal, rest[0], request.ApprovalID, request.Reason)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		artifact.StoragePath = ""
		writeJSON(w, http.StatusOK, artifact)
		return
	}
	if len(rest) != 1 || r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "not_found", "artifact route not found")
		return
	}
	artifact, err := s.research.store.GetArtifact(r.Context(), rest[0])
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	allowed, err := s.research.service.CanReadCampaign(r.Context(), principal, artifact.CampaignID)
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "principal is not a campaign member")
		return
	}
	if artifact.Sensitivity == "private_disclosure" && !principalHasAnyRole(principal, domain.RoleOperator, domain.RoleReviewer) {
		writeError(w, http.StatusForbidden, "forbidden", "private disclosure artifact requires operator, reviewer, or admin role")
		return
	}
	if r.URL.Query().Get("download") != "1" {
		artifact.StoragePath = ""
		writeJSON(w, http.StatusOK, artifact)
		return
	}
	_, file, err := s.research.store.OpenArtifact(r.Context(), artifact.ID)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", artifact.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.bin"`, artifact.ID))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
	_, _ = io.Copy(w, file)
}

func (s *Server) handleResearchEvents(w http.ResponseWriter, r *http.Request, principal domain.Principal) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	campaignID := strings.TrimSpace(r.URL.Query().Get("campaign_id"))
	if campaignID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "campaign_id is required")
		return
	}
	allowed, err := s.research.service.CanReadCampaign(r.Context(), principal, campaignID)
	if err != nil || !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "principal is not a campaign member")
		return
	}
	after := int64(0)
	value := r.URL.Query().Get("after")
	if value == "" {
		value = r.Header.Get("Last-Event-ID")
	}
	if value != "" {
		after, _ = strconv.ParseInt(value, 10, 64)
	}
	events, err := s.research.store.ListAudit(r.Context(), after, queryLimit(r, 1000))
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	filtered := events[:0]
	for _, event := range events {
		if event.ResourceID == campaignID || event.Details["campaign_id"] == campaignID {
			filtered = append(filtered, event)
		}
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": filtered})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, event := range filtered {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, researchEventType(event.Action), data)
	}
}

func researchEventType(action string) string {
	switch {
	case action == "campaign.transition":
		return "campaign_state"
	case strings.HasPrefix(action, "run.") || action == "job.enqueue":
		return "run_updated"
	case action == "build.ingest":
		return "build_updated"
	case action == "observation.ingest":
		return "crash_observed"
	case strings.HasPrefix(action, "approval."):
		return "approval_updated"
	case strings.HasPrefix(action, "finding.") || action == "branch.complete" || action == "novelty.complete":
		return "finding_updated"
	case strings.HasPrefix(action, "revision."):
		return "revision_checked"
	case strings.HasPrefix(action, "source.") || action == "novelty.lookup":
		return "novelty_evidence"
	case strings.HasPrefix(action, "remediation."):
		return "remediation_validated"
	case strings.HasPrefix(action, "report."):
		return "report_ready"
	case strings.HasPrefix(action, "disclosure."):
		return "disclosure_recorded"
	default:
		return "audit"
	}
}

func (s *Server) handleResearchAudit(w http.ResponseWriter, r *http.Request, principal domain.Principal, rest []string) {
	if len(rest) != 1 || rest[0] != "verify" || r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "not_found", "audit route not found")
		return
	}
	if !principalHasAnyRole(principal, domain.RoleAdmin) {
		writeError(w, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if err := s.research.store.VerifyAuditChain(r.Context()); err != nil {
		writeError(w, http.StatusConflict, "audit_chain_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Server) writeResearchList(w http.ResponseWriter, value any, err error) {
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": value})
}

func (s *Server) writeResearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrArtifactPurged):
		writeError(w, http.StatusGone, "artifact_purged", err.Error())
	case errors.Is(err, store.ErrRetentionActive):
		writeError(w, http.StatusConflict, "retention_active", err.Error())
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case strings.Contains(err.Error(), "runner unavailable"), strings.Contains(err.Error(), "broker not running"):
		writeError(w, http.StatusServiceUnavailable, "runner_unavailable", err.Error())
	case errors.Is(err, service.ErrSourceUnavailable):
		writeError(w, http.StatusServiceUnavailable, "source_acquisition_unavailable", err.Error())
	case errors.Is(err, store.ErrVersionConflict):
		writeError(w, http.StatusConflict, "version_conflict", err.Error())
	default:
		writeError(w, http.StatusBadRequest, "research_error", err.Error())
	}
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 || value > 10000 {
		return fallback
	}
	return value
}

func containsID(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func researchJobNeedsCorpus(job apparatus.JobRequest) bool {
	if job.Operation == domain.OperationRegressionTest {
		return job.Arguments["validation-kind"] == "regression"
	}
	switch job.Operation {
	case domain.OperationSeed, domain.OperationFuzz, domain.OperationMergeCorpus, domain.OperationCoverage, domain.OperationRegressionTest:
		return true
	default:
		return false
	}
}
