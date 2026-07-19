package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
)

/* modelSearchClient bounds the Hugging Face catalog passthrough. */
var modelSearchClient = &http.Client{Timeout: 10 * time.Second}

/*
handleSystem reports the detected host resources — CPU memory, free disk, and
the accelerator (GPU vendor/name/VRAM/backend) — so the UI can show what the
machine has and what the app will make of it. It is read-only and always
available; GPU detection is best-effort and fails open to "none".
*/
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		dir = "."
	}
	host, err := llamacpp.SystemProfiler{}.Profile(r.Context(), dir)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_profile_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":         host,
		"capabilities": s.capabilityStatus(),
	})
}

/*
capabilityStatus is the authoritative, machine-readable view of features the
running server can actually execute. It deliberately distinguishes containment
from a complete fuzzing apparatus so clients and models cannot infer capability
from stale documentation or a registered placeholder.
*/
func (s *Server) capabilityStatus() map[string]any {
	workspace := s.getWorkspace()
	execEnabled := s.allowExec && workspace != ""
	execBackend := "disabled"
	apparatus := "none"
	if execEnabled {
		execBackend = "docker"
		apparatus = "external_php_driver"
	}
	researchEnabled := s.research != nil
	researchRunner := researchEnabled && s.research.broker != nil
	coverageGuided := false
	researchApparatus := "none"
	isolationAssurance := "none"
	dockerResourceLimits := false
	if researchRunner {
		assurance := s.research.broker.Assurance()
		isolationAssurance = assurance.Isolation
		dockerResourceLimits = assurance.Backend == "docker"
		if manifests, err := s.research.store.ListApparatus(context.Background(), 1000); err == nil {
			for _, manifest := range manifests {
				if manifest.Engine == "libfuzzer" {
					coverageGuided = true
					researchApparatus = manifest.ID
					break
				}
			}
		}
	}
	return map[string]any{
		"file_read":                     true,
		"file_mutation":                 workspace != "",
		"host_shell":                    false,
		"arbitrary_http":                false,
		"contained_execution":           execEnabled,
		"execution_backend":             execBackend,
		"execution_image_pinned":        execEnabled && s.execImageDigest != "",
		"apparatus":                     apparatus,
		"coverage_guided_fuzzing":       coverageGuided,
		"artifact_persistence":          researchEnabled,
		"research_persistence":          researchEnabled,
		"authentication":                researchEnabled,
		"research_mode":                 researchEnabled,
		"research_runner":               researchRunner,
		"research_apparatus":            researchApparatus,
		"research_isolation_assurance":  isolationAssurance,
		"research_images_pinned":        researchRunner,
		"research_query_tool":           researchEnabled,
		"research_cpu_rate_limit":       dockerResourceLimits,
		"research_writable_monitor":     researchRunner,
		"research_kernel_storage_quota": false,
		"research_full_accounting":      false,
		"research_branch_workflow":      researchEnabled,
		"research_novelty_workflow":     researchEnabled,
		"research_novelty_egress":       researchEnabled && s.research.noveltyBroker != nil,
		"research_source_acquisition":   researchEnabled && s.research.sourceBroker != nil,
		"research_remediation":          researchEnabled,
		"research_private_reporting":    researchEnabled,
		"research_remote_transport":     false,
		"automatic_disclosure":          false,
		"rag":                           s.rag != nil && !s.disableRAG,
		"cve_verifier":                  s.answerVerifier != nil,
	}
}

/* hfModel is the trimmed shape of a Hugging Face model search row. */
type hfModel struct {
	ID        string   `json:"id"`
	Downloads int      `json:"downloads"`
	Likes     int      `json:"likes"`
	GGUF      bool     `json:"gguf"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

/* maxModelSearchResults bounds the HF passthrough so the UI list stays small. */
const maxModelSearchResults = 25

/*
handleModelSearch proxies a bounded Hugging Face model search so the UI can let
the operator browse downloadable GGUF models without leaving the app. It filters
to GGUF-tagged repos (the only kind the llama.cpp downloader can run) and never
forwards credentials — this is a public catalog read.
*/
func (s *Server) handleModelSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameter q is required")
		return
	}
	limit := maxModelSearchResults
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < limit {
			limit = n
		}
	}

	u := "https://huggingface.co/api/models?" + url.Values{
		"search": {q},
		"filter": {"gguf"},
		"sort":   {"downloads"},
		"limit":  {strconv.Itoa(limit)},
		"full":   {"false"},
	}.Encode()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search_failed", err.Error())
		return
	}
	resp, err := modelSearchClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "search_upstream", "hugging face search unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "search_upstream", "hugging face returned "+resp.Status)
		return
	}

	var raw []struct {
		ID        string   `json:"id"`
		Downloads int      `json:"downloads"`
		Likes     int      `json:"likes"`
		Tags      []string `json:"tags"`
		LastMod   string   `json:"lastModified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		writeError(w, http.StatusBadGateway, "search_upstream", "malformed hugging face response")
		return
	}

	out := make([]hfModel, 0, len(raw))
	for _, m := range raw {
		gguf := false
		for _, t := range m.Tags {
			if t == "gguf" {
				gguf = true
				break
			}
		}
		out = append(out, hfModel{ID: m.ID, Downloads: m.Downloads, Likes: m.Likes, GGUF: gguf, Tags: m.Tags, UpdatedAt: m.LastMod})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}
