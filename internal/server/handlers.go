package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/refine"
)

/* chatCompletionRequest is the OpenAI-shape request body accepted by /v1/chat/completions. */
type chatCompletionRequest struct {
	Model     string           `json:"model"`
	Messages  []inboundMessage `json:"messages"`
	Stream    bool             `json:"stream"`
	ProfileID string           `json:"profile_id,omitempty"`
	/*
		WebSearch overrides the provider-default web-grounding flag for
		this request. A nil pointer means "use the provider default";
		non-nil means the caller has chosen.
	*/
	WebSearch *bool `json:"web_search,omitempty"`
	/*
		Agentic overrides the operator default for agentic-RAG on this request. A
		nil pointer means "use the server default"; non-nil lets the UI toggle the
		reasoning-driven retrieval loop per conversation. Ignored when RAG is off.
	*/
	Agentic *bool `json:"agentic,omitempty"`
	/*
		Refine enables opt-in judge-in-the-loop mode: the answer is regenerated with
		the strict judge's critique until USABLE or the iteration budget is spent,
		streaming per-round progress. Evaluation-first and may take minutes; it never
		upgrades a non-usable result into a confirmed claim.
	*/
	Refine         bool `json:"refine,omitempty"`
	RefineMaxIters int  `json:"refine_max_iters,omitempty"`
	/*
		RefineTimeoutSeconds overrides the per-round generate+judge budget for refine
		mode (0 = server default). A large local model needs minutes per round; it is
		clamped server-side to a safe range.
	*/
	RefineTimeoutSeconds int `json:"refine_timeout_seconds,omitempty"`
}

/*
inboundMessage decodes one OpenAI-shape request message. Content is left raw so
UnmarshalJSON can accept both the plain-string form and the multimodal parts
array ([{type:"text",...},{type:"image_url",image_url:{url:"data:..."}}]) used
for image input. It flattens to a canonical llm.Message via toMessage.
*/
type inboundMessage struct {
	Role       llm.Role        `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []llm.ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type inboundImageURL struct {
	URL string `json:"url"`
}

/* UnmarshalJSON accepts image_url as either {"url":...} (OpenAI) or a bare string (Ollama /v1). */
func (u *inboundImageURL) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		u.URL = s
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	u.URL = obj.URL
	return nil
}

type inboundContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL inboundImageURL `json:"image_url"`
}

/*
toMessage flattens an inbound message to the canonical llm.Message. String
content maps straight to Content; an array of parts concatenates text parts
into Content and decodes image_url data URLs into Images. Non-data image URLs
are rejected because this build only forwards inline pasted images.
*/
func (im inboundMessage) toMessage() (llm.Message, error) {
	msg := llm.Message{
		Role:       im.Role,
		ToolCalls:  im.ToolCalls,
		ToolCallID: im.ToolCallID,
		Name:       im.Name,
	}
	if len(im.Content) == 0 || string(im.Content) == "null" {
		return msg, nil
	}

	var s string
	if json.Unmarshal(im.Content, &s) == nil {
		msg.Content = s
		return msg, nil
	}

	var parts []inboundContentPart
	if err := json.Unmarshal(im.Content, &parts); err != nil {
		return msg, fmt.Errorf("invalid message content: must be a string or an array of content parts")
	}
	for _, p := range parts {
		switch p.Type {
		case "text":
			msg.Content += p.Text

		case "image_url":
			mime, data, err := parseDataURL(p.ImageURL.URL)
			if err != nil {
				return msg, err
			}
			msg.Images = append(msg.Images, llm.ImagePart{MimeType: mime, Data: data})
		}
	}
	return msg, nil
}

/*
parseDataURL splits a "data:<mime>;base64,<payload>" URL into its media type
and base64 payload, validating that the payload decodes. It returns an error
for non-data URLs (e.g. remote http links), which this build does not fetch.
*/
func parseDataURL(u string) (mime string, data string, err error) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", fmt.Errorf("image_url must be a base64 data URL")
	}
	rest := strings.TrimPrefix(u, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", "", fmt.Errorf("malformed data URL")
	}
	meta, payload := rest[:comma], rest[comma+1:]
	if !strings.Contains(meta, ";base64") {
		return "", "", fmt.Errorf("data URL must be base64-encoded")
	}
	mime = strings.TrimSuffix(meta, ";base64")
	mime = strings.SplitN(mime, ";", 2)[0]
	if mime == "" {
		mime = "image/png"
	}
	if _, derr := base64.StdEncoding.DecodeString(payload); derr != nil {
		return "", "", fmt.Errorf("data URL payload is not valid base64")
	}
	return mime, payload, nil
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
handleClusterStatus answers GET /v1/cluster with the cluster control-plane view
(mode, the backend that served the most recent request, per-node reachability,
and the served model) so the web UI can show whether chat is running clustered.
When no cluster provider is registered it returns {enabled:false}, which the UI
treats as single-node and renders nothing.
*/
func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if p, ok := s.providers["cluster"]; ok {
		if cs, ok := p.(interface {
			Status(context.Context) *cluster.ClusterStatus
		}); ok {
			writeJSON(w, http.StatusOK, cs.Status(r.Context()))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
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
	if !decodeJSONRequest(w, r, &req, maxChatBodyBytes) {
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

	/* Flatten OpenAI-shape messages (string or multimodal parts) to canonical messages before headers are written. */
	messages := make([]llm.Message, 0, len(req.Messages))
	for _, im := range req.Messages {
		m, cErr := im.toMessage()
		if cErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", cErr.Error())
			return
		}
		messages = append(messages, m)
	}

	provName, modelID, ok := s.resolveProviderModel(req.Model)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no_provider", "no chat provider is configured")
		return
	}

	a, err := s.newAgent(provName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "provider_error", err.Error())
		return
	}
	a.ProfileID = req.ProfileID
	a.Model = modelID
	a.WebSearch = s.shouldWebSearch(provName, req.WebSearch)
	a.Agentic = s.shouldAgentic(req.Agentic)

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
	history, lastUser, sErr := splitHistory(messages)
	if sErr != nil {
		_ = enc.writeOpenAIDelta(completionID, displayModel, createdAt, map[string]any{
			"role":    "assistant",
			"content": "request rejected: " + sErr.Error(),
		}, "stop")
		enc.writeDone()
		return
	}
	for _, m := range history {
		session.Append(m)
	}

	/*
		Refine mode: regenerate-and-judge until USABLE or the budget is spent,
		streaming per-round progress so the web user watches the evaluation. It
		runs off the normal token-streaming path (each round is non-streaming) and
		takes the latest user message as the task, regenerating in a fresh session
		each round. The advisory verifier is disabled here because the judge IS the
		validation.
	*/
	if req.Refine {
		a.Verifier = nil
		/* Bound a degenerate/repeating local model so a runaway self-terminates and is judged, not hung. */
		a.MaxTokens = refineGenMaxTokens
		task := lastUser.Content
		gen := func(gctx context.Context, t, brief string) (string, error) {
			prompt := t
			if brief != "" {
				prompt = t + "\n\n[Refinement brief — improve grounding, scoping, and labelling ONLY; do NOT fabricate]\n" + brief
			}
			return a.Run(gctx, agent.NewSession(), prompt)
		}
		streamRefine(r.Context(), enc, completionID, displayModel, createdAt, task, gen, s.refineJudge, refine.LoopConfig{
			MaxIters:     clampRefineIters(req.RefineMaxIters),
			RoundTimeout: clampRefineTimeout(req.RefineTimeoutSeconds),
		})
		return
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

	if _, runErr := a.RunStreamMessage(r.Context(), session, lastUser, sink); runErr != nil {
		_ = enc.writeNamedEvent("error", map[string]any{"message": runErr.Error()})
	}
	enc.writeDone()
}

/*
splitHistory returns all messages except the trailing user message, and the
trailing user message itself. It enforces "last message must be from user"
because the agent loop appends a single user input per Run.
*/
func splitHistory(messages []llm.Message) ([]llm.Message, llm.Message, error) {
	if len(messages) == 0 {
		return nil, llm.Message{}, errors.New("no messages")
	}
	last := messages[len(messages)-1]
	if last.Role != llm.RoleUser {
		return nil, llm.Message{}, fmt.Errorf("last message must have role=user, got %q", last.Role)
	}
	return messages[:len(messages)-1], last, nil
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

/* Body-size caps for JSON request decoding. Chat carries base64 image parts. */
const (
	maxChatBodyBytes    = 24 << 20 /* 24 MiB */
	maxControlBodyBytes = 1 << 20  /* 1 MiB for small control/mutation bodies */
)

/*
decodeJSONRequest enforces the request hardening every mutating JSON endpoint
needs and that was previously missing everywhere: it requires a JSON
Content-Type (so a cross-origin "simple request" with text/plain — the shape a
CSRF/DNS-rebinding page can send without a preflight — is rejected before it can
cause a side effect) and caps the body with http.MaxBytesReader (so one large
body cannot exhaust memory on an unauthenticated call). It writes the error
response and returns false on any failure.
*/
func decodeJSONRequest(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	ct := r.Header.Get("Content-Type")
	if ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
			return false
		}
	} else {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed or oversized JSON body: "+err.Error())
		return false
	}
	return true
}

/* newCompletionID mirrors OpenAI's `chatcmpl-...` prefix using the request time. */
func newCompletionID() string {
	return "chatcmpl-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000"), ".", "")
}
