package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
titleSystemPrompt steers a model to distill a chat's first message into a short
label. It deliberately goes through the raw provider (not the agent loop) so the
title is not contaminated by the RAG augmentation, web grounding, or the
baseline coding-paradigm directive that agent.composeMessages injects.
*/
const titleSystemPrompt = "You generate a very short title for a chat conversation from the user's first message. Reply with ONLY the title: at most 6 words, no surrounding quotes, no trailing punctuation, no preamble, no explanation. Capture the subject, not the phrasing."

/* thinkBlockRe strips reasoning models' <think>…</think> spans before extracting the title. */
var thinkBlockRe = regexp.MustCompile(`(?is)<think>.*?</think>`)

const (
	titleInputCap = 2000
	titleMaxWords = 8
	titleMaxRunes = 60
)

type titleRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

/*
handleTitle distills the first user message of a conversation into a concise
title using the conversation's provider. It is best-effort: on any model or
input problem it returns an empty title and the client keeps its own fallback.
*/
func (s *Server) handleTitle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}

	var req titleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body: "+err.Error())
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "prompt must not be empty")
		return
	}
	if runes := []rune(prompt); len(runes) > titleInputCap {
		prompt = string(runes[:titleInputCap])
	}

	provName, modelID := s.splitModelID(req.Model)
	prov, ok := s.providers[provName]
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no_provider", "no provider available for title generation")
		return
	}

	/*
		The timeout is generous because this is a background, best-effort call:
		a large chat model (e.g. a coder model) can take ~30s+ to cold-load into
		Ollama on first use, and we would rather wait than abandon the title.
	*/
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	temperature := 0.3
	/*
		maxTokens is deliberately generous, not tight. think=false makes
		Qwen3/Josie answer directly in a handful of tokens, but always-reasoning
		families (gpt-oss) ignore the flag and spend tokens thinking first; too
		small a budget cuts them off mid-thought and yields empty content. With
		headroom they finish reasoning and still emit the title, while models
		that need no reasoning stop early — so the larger cap costs them nothing.
		sanitizeTitle trims any over-long or sentence-shaped output down to a
		label.
	*/
	maxTokens := 300
	/*
		think=false makes reasoning models (Qwen3/Josie) answer directly instead
		of spending tokens on a thinking span. Families that always reason
		(gpt-oss) ignore it and rely on the token headroom above; providers
		without reasoning control ignore it too.
	*/
	noThink := false
	resp, err := prov.Chat(ctx, llm.ChatRequest{
		Model: modelID,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: titleSystemPrompt},
			{Role: llm.RoleUser, Content: "First message:\n\n" + prompt},
		},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Think:       &noThink,
	})
	if err != nil {
		s.logger.Warn("title generation failed", "provider", provName, "err", err)
		writeError(w, http.StatusBadGateway, "title_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"title": sanitizeTitle(resp.Message.Content)})
}

/*
sanitizeTitle turns a model's raw reply into a clean one-line label: it drops
reasoning spans, keeps the first non-empty line, strips a leading "Title:" label
and surrounding quotes/markup, collapses whitespace, and caps the length. It
returns "" when nothing usable remains so the caller can fall back.
*/
func sanitizeTitle(raw string) string {
	s := thinkBlockRe.ReplaceAllString(raw, " ")
	s = strings.TrimSpace(s)

	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			s = t
			break
		}
	}

	if strings.HasPrefix(strings.ToLower(s), "title:") {
		s = strings.TrimSpace(s[len("title:"):])
	}

	s = strings.Trim(s, " \t\"'`*#")
	s = strings.Join(strings.Fields(s), " ")

	if words := strings.Fields(s); len(words) > titleMaxWords {
		s = strings.Join(words[:titleMaxWords], " ")
	}
	if runes := []rune(s); len(runes) > titleMaxRunes {
		capped := string(runes[:titleMaxRunes])
		/*
			Prefer cutting at a word boundary so a long sentence-shaped reply is
			not chopped mid-word ("…scalability and m"). Fall back to the hard
			cut only when the first titleMaxRunes runes contain no space.
		*/
		if i := strings.LastIndexByte(capped, ' '); i > 0 {
			capped = capped[:i]
		}
		s = strings.TrimSpace(capped)
	}

	return strings.TrimRight(s, " .,:;!?-—")
}
