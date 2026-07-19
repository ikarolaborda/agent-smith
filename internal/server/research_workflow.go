package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/service"
)

func (s *Server) handleResearchLookups(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.research.noveltyBroker == nil {
		writeError(w, http.StatusServiceUnavailable, "lookup_unavailable", "no fixed novelty lookup sources are configured")
		return
	}
	var request struct {
		FindingID  string `json:"finding_id"`
		SourceName string `json:"source_name"`
		Query      string `json:"query"`
		ApprovalID string `json:"approval_id"`
	}
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	source, ok := s.research.noveltySources[strings.TrimSpace(request.SourceName)]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_source", "lookup source is not configured")
		return
	}
	parsed, err := url.Parse(source.BaseURL)
	if err != nil || parsed.Hostname() == "" {
		writeError(w, http.StatusInternalServerError, "invalid_source", "configured lookup source is invalid")
		return
	}
	if _, err := s.research.service.AuthorizeNoveltyLookup(r.Context(), principal, campaignID, request.FindingID, source.Name, parsed.Hostname(), request.ApprovalID); err != nil {
		s.writeResearchError(w, err)
		return
	}
	evidence, err := s.research.noveltyBroker.Lookup(r.Context(), campaignID, request.FindingID, source.Name, request.Query)
	if err != nil {
		if evidence.ID != "" {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "evidence": evidence})
			return
		}
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, evidence)
}

func (s *Server) handleResearchRevisionChecks(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.research.store.ListRevisionChecks(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case http.MethodPost:
		var request struct {
			FindingID string `json:"finding_id"`
			Revision  string `json:"revision"`
			Reason    string `json:"reason"`
		}
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		check, err := s.research.service.RecordUntestedRevision(r.Context(), principal, campaignID, request.FindingID, request.Revision, request.Reason)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, check)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchBranchReview(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request struct {
		FindingID string `json:"finding_id"`
	}
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	finding, err := s.research.service.CompleteBranchReview(r.Context(), principal, campaignID, request.FindingID)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, finding)
}

func (s *Server) handleResearchSourceEvidence(w http.ResponseWriter, r *http.Request, campaignID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	values, err := s.research.store.ListSourceEvidence(r.Context(), campaignID, queryLimit(r, 1000))
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	findingID := strings.TrimSpace(r.URL.Query().Get("finding_id"))
	if findingID != "" {
		filtered := values[:0]
		for _, value := range values {
			if value.FindingID == findingID {
				filtered = append(filtered, value)
			}
		}
		values = filtered
	}
	s.writeResearchList(w, values, nil)
}

func (s *Server) handleResearchSourceReviews(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.research.store.ListSourceReviews(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case http.MethodPost:
		var request service.SourceReviewRequest
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		review, err := s.research.service.ReviewSourceEvidence(r.Context(), principal, campaignID, request)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, review)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchNoveltyReview(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request struct {
		FindingID string `json:"finding_id"`
	}
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	finding, err := s.research.service.CompleteNoveltyReview(r.Context(), principal, campaignID, request.FindingID)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, finding)
}

func (s *Server) handleResearchCandidatePatches(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request service.CandidatePatchRequest
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	artifact, err := s.research.service.CreateCandidatePatch(r.Context(), principal, campaignID, request)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	artifact.StoragePath = ""
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) handleResearchRemediations(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.research.store.ListRemediations(r.Context(), campaignID, queryLimit(r, 1000))
		s.writeResearchList(w, values, err)
	case http.MethodPost:
		var request service.RemediationRequest
		if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
			return
		}
		validation, err := s.research.service.ValidateRemediation(r.Context(), principal, campaignID, request)
		if err != nil {
			s.writeResearchError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, validation)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
	}
}

func (s *Server) handleResearchReports(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request service.ReportDraftRequest
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	artifact, err := s.research.service.CreatePrivateReport(r.Context(), principal, campaignID, request)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	artifact.StoragePath = ""
	writeJSON(w, http.StatusCreated, artifact)
}

func (s *Server) handleResearchDisclosures(w http.ResponseWriter, r *http.Request, principal domain.Principal, campaignID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var request service.DisclosureRecordRequest
	if !decodeJSONRequest(w, r, &request, maxResearchBodyBytes) {
		return
	}
	finding, err := s.research.service.RecordDisclosure(r.Context(), principal, campaignID, request)
	if err != nil {
		s.writeResearchError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, finding)
}
