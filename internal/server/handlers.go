package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* chatCompletionRequest is the OpenAI-shape request body accepted by /v1/chat/completions. */
type chatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []llm.Message `json:"messages"`
	Stream    bool          `json:"stream"`
	ProfileID string        `json:"profile_id,omitempty"`
	/*
		WebSearch overrides the provider-default web-grounding flag for
		this request. A nil pointer means "use the provider default";
		non-nil means the caller has chosen.
	*/
	WebSearch *bool `json:"web_search,omitempty"`
}

/* errorEnvelope mimics the OpenAI error envelope. */
type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

/* handleHealth answers GET /healthz with a tiny JSON document. */
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

/*
handleModels returns the list of available model IDs in OpenAI list-envelope
shape: {object:"list", data:[{id,object,owned_by,...}]}.
*/
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   s.listModels(r.Context()),
	})
}

/*
handleProviders returns one row per configured provider with its default
model. The UI uses this to populate a provider selector and validate model
IDs without parsing the larger /v1/models payload.
*/
func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	out := make([]row, 0, len(s.providers))
	for name := range s.providers {
		cfg, ok := s.cfg.Providers[name]
		if !ok {
			continue
		}
		out = append(out, row{ID: name, Model: cfg.Model})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":  "list",
		"data":    out,
		"default": s.cfg.DefaultProvider,
	})
}

/*
handleChatCompletions implements the OpenAI-compatible streaming chat
endpoint. Non-streaming requests are rejected with 400 because the first cut
deliberately supports streaming only; this matches the OpenAI semantics
closely enough for existing SDKs while keeping the implementation small.
*/
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body: "+err.Error())
		return
	}
	if !req.Stream {
		writeError(w, http.StatusBadRequest, "unsupported_mode", "only streaming requests are supported in this build")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages must not be empty")
		return
	}
	if len(s.providers) == 0 {
		writeError(w, http.StatusServiceUnavailable, "no_provider", "no providers configured on this server")
		return
	}

	provName, modelID := s.splitModelID(req.Model)
	if provName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "no provider available and no default configured")
		return
	}

	a, err := s.newAgent(provName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a.ProfileID = req.ProfileID
	a.Model = modelID
	a.WebSearch = s.shouldWebSearch(provName, req.WebSearch)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flush", "streaming not supported by underlying ResponseWriter")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := newSSEEncoder(w, flusher)
	completionID := newCompletionID()
	createdAt := time.Now().Unix()
	displayModel := req.Model
	if displayModel == "" {
		displayModel = provName + "/" + modelID
	}

	/*
		Replay the prior conversation into a fresh Session minus the last user
		message; the agent loop will append it again. This keeps the existing
		Agent.RunStream contract intact.
	*/
	session := agent.NewSession()
	history, lastUser, sErr := splitHistory(req.Messages)
	if sErr != nil {
		enc.writeOpenAIDelta(completionID, displayModel, createdAt, map[string]any{
			"role":    "assistant",
			"content": "request rejected: " + sErr.Error(),
		}, "stop")
		enc.writeDone()
		return
	}
	for _, m := range history {
		session.Append(m)
	}

	emittedRole := false
	sink := agent.SinkFunc(func(ev agent.StreamEvent) error {
		switch ev.Kind {
		case agent.StreamEventTextDelta:
			delta := map[string]any{"content": ev.Delta}
			if !emittedRole {
				delta["role"] = "assistant"
				emittedRole = true
			}
			return enc.writeOpenAIDelta(completionID, displayModel, createdAt, delta, "")
		case agent.StreamEventToolCallStart:
			delta := map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    ev.ToolCallID,
					"type":  "function",
					"function": map[string]any{
						"name": ev.ToolCallName,
					},
				}},
			}
			if !emittedRole {
				delta["role"] = "assistant"
				emittedRole = true
			}
			return enc.writeOpenAIDelta(completionID, displayModel, createdAt, delta, "")
		case agent.StreamEventToolCallArgDelta:
			delta := map[string]any{
				"tool_calls": []map[string]any{{
					"index": 0,
					"id":    ev.ToolCallID,
					"function": map[string]any{
						"arguments": string(ev.ToolCallArgs),
					},
				}},
			}
			return enc.writeOpenAIDelta(completionID, displayModel, createdAt, delta, "")
		case agent.StreamEventToolCallEnd:
			/* no-op on the wire — the end marker is implied by the next tool_result */
			return nil
		case agent.StreamEventToolResult:
			return enc.writeNamedEvent("tool_result", map[string]any{
				"tool_call_id": ev.ToolCallID,
				"name":         ev.ToolCallName,
				"is_error":     ev.ToolError,
				"content":      ev.ToolResult,
			})
		case agent.StreamEventDone:
			return enc.writeOpenAIDelta(completionID, displayModel, createdAt, map[string]any{}, "stop")
		case agent.StreamEventError:
			return enc.writeNamedEvent("error", map[string]any{"message": ev.Error})
		}
		return nil
	})

	if _, runErr := a.RunStream(r.Context(), session, lastUser, sink); runErr != nil {
		_ = enc.writeNamedEvent("error", map[string]any{"message": runErr.Error()})
	}
	enc.writeDone()
}

/*
splitHistory returns all messages except the trailing user message, and the
trailing user message itself. It enforces "last message must be from user"
because the agent loop appends a single user input per Run.
*/
func splitHistory(messages []llm.Message) ([]llm.Message, string, error) {
	if len(messages) == 0 {
		return nil, "", errors.New("no messages")
	}
	last := messages[len(messages)-1]
	if last.Role != llm.RoleUser {
		return nil, "", fmt.Errorf("last message must have role=user, got %q", last.Role)
	}
	return messages[:len(messages)-1], last.Content, nil
}

/* writeJSON serializes v as JSON to w, setting the content-type. */
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

/* writeError serializes an errorEnvelope. */
func writeError(w http.ResponseWriter, status int, code, msg string) {
	var env errorEnvelope
	env.Error.Message = msg
	env.Error.Type = code
	writeJSON(w, status, env)
}

/* newCompletionID mirrors OpenAI's `chatcmpl-...` prefix using the request time. */
func newCompletionID() string {
	return "chatcmpl-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000"), ".", "")
}
