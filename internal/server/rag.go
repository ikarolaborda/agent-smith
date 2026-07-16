package server

import (
	"net/http"

	"github.com/ikarolaborda/agent-smith/internal/rag"
)

/*
handleRAGCollections returns the catalog of loaded RAG collections in a
list envelope similar to /v1/models.
*/
func (s *Server) handleRAGCollections(w http.ResponseWriter, _ *http.Request) {
	if s.rag == nil {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}})
		return
	}
	type row struct {
		Name       string `json:"name"`
		EmbedderID string `json:"embedder_id"`
		Dim        int    `json:"dim"`
		Chunks     int    `json:"chunks"`
	}
	names := s.rag.Index.Names()
	out := make([]row, 0, len(names))
	for _, n := range names {
		c := s.rag.Index.Get(n)
		if c == nil {
			continue
		}
		out = append(out, row{
			Name:       c.Name,
			EmbedderID: c.EmbedderID,
			Dim:        c.Dim,
			Chunks:     len(c.Chunks),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

/*
handleRAGRemember writes one memory chunk for the given profile.
*/
func (s *Server) handleRAGRemember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.rag == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "RAG service is not configured")
		return
	}
	var req struct {
		ProfileID  string  `json:"profile_id"`
		Kind       string  `json:"kind"`
		Text       string  `json:"text"`
		Importance float32 `json:"importance,omitempty"`
		Pinned     bool    `json:"pinned,omitempty"`
	}
	if !decodeJSONRequest(w, r, &req, maxControlBodyBytes) {
		return
	}
	chunk, err := s.rag.Remember(r.Context(), rag.MemoryWrite{
		ProfileID:  req.ProfileID,
		Kind:       req.Kind,
		Text:       req.Text,
		Importance: req.Importance,
		Pinned:     req.Pinned,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "remember_failed", err.Error())
		return
	}
	chunk.Vector = nil
	writeJSON(w, http.StatusOK, map[string]any{"chunk": chunk})
}

/* handleRAGForget removes one memory chunk owned by the profile. */
func (s *Server) handleRAGForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.rag == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "RAG service is not configured")
		return
	}
	var req struct {
		ProfileID string `json:"profile_id"`
		ID        string `json:"id"`
	}
	if !decodeJSONRequest(w, r, &req, maxControlBodyBytes) {
		return
	}
	if err := s.rag.Forget(r.Context(), req.ProfileID, req.ID); err != nil {
		writeError(w, http.StatusNotFound, "forget_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": req.ID})
}

/* handleRAGMemory lists all memory chunks owned by the queried profile. */
func (s *Server) handleRAGMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if s.rag == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "RAG service is not configured")
		return
	}
	profileID := r.URL.Query().Get("profile_id")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile_id query parameter required")
		return
	}
	items, err := s.rag.ListMemory(profileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

/*
handleRAGCorrection wraps Remember with kind=correction, composing a
structured note from {question, wrong_answer, correct_answer}.
*/
func (s *Server) handleRAGCorrection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.rag == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "RAG service is not configured")
		return
	}
	var req struct {
		ProfileID     string `json:"profile_id"`
		Question      string `json:"question"`
		WrongAnswer   string `json:"wrong_answer"`
		CorrectAnswer string `json:"correct_answer"`
	}
	if !decodeJSONRequest(w, r, &req, maxControlBodyBytes) {
		return
	}
	text := "Question: " + req.Question +
		"\nWrong answer: " + req.WrongAnswer +
		"\nCorrect answer: " + req.CorrectAnswer
	chunk, err := s.rag.Remember(r.Context(), rag.MemoryWrite{
		ProfileID:  req.ProfileID,
		Kind:       rag.KindCorrection,
		Text:       text,
		Importance: 0.9,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "correction_failed", err.Error())
		return
	}
	chunk.Vector = nil
	writeJSON(w, http.StatusOK, map[string]any{"chunk": chunk})
}

/*
handleRAGSearch runs a debug query against the RAG index. It exists so the
UI and operators can sanity-check retrieval without invoking the model.
*/
func (s *Server) handleRAGSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if s.rag == nil {
		writeError(w, http.StatusServiceUnavailable, "rag_disabled", "RAG service is not configured")
		return
	}
	var req struct {
		Query      string   `json:"query"`
		Collection []string `json:"collection"`
		K          int      `json:"k"`
	}
	if !decodeJSONRequest(w, r, &req, maxControlBodyBytes) {
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query is required")
		return
	}
	if req.K > rag.MaxSearchResults {
		req.K = rag.MaxSearchResults
	}
	results, err := s.rag.Search(r.Context(), req.Query, rag.SearchOpts{Filter: req.Collection, K: req.K})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": rag.RedactSearchResults(results)})
}
