package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/convomem"
)

/* MemoryCollectionName is the conventional name for the per-binary memory store. */
const MemoryCollectionName = "memory"

/* Allowed memory kinds; anything else is rejected at write time. */
const (
	KindPreference  = "preference"
	KindProjectFact = "project_fact"
	KindDecision    = "decision"
	KindCorrection  = "correction"
)

var validMemoryKinds = map[string]struct{}{
	KindPreference:  {},
	KindProjectFact: {},
	KindDecision:    {},
	KindCorrection:  {},
}

/*
instructionInjectionRe matches obvious prompt-injection patterns we refuse to
store on a per-fact basis. Corrections are exempt: a user reporting a bad
answer may legitimately quote system-prompt-style phrasing in the original
question.
*/
var instructionInjectionRe = regexp.MustCompile(
	`(?i)\b(?:` +
		`ignore (?:all |any )?(?:previous|prior|earlier|above) (?:instructions?|prompts?)` +
		`|disregard (?:the |all )?(?:previous|prior|system) (?:instructions?|prompts?)` +
		`|system prompt` +
		`|always (?:do|answer|respond)` +
		`|never (?:mention|tell|reveal)` +
		`|forget (?:everything|all|previous)` +
		`)\b`,
)

/*
MemoryWrite is the payload accepted by Service.Remember. ProfileID and Text
are required; Kind defaults to "project_fact"; Importance defaults to a
kind-specific value; Pinned defaults to false.
*/
type MemoryWrite struct {
	ProfileID  string
	Kind       string
	Text       string
	Importance float32
	Pinned     bool
}

/*
Remember validates, embeds, and appends one memory chunk to the per-binary
memory collection. The instruction-injection filter rejects non-correction
writes whose text matches obvious jailbreak patterns; corrections are
allowed because the wrong answer being corrected may legitimately contain
such text.
*/
func (s *Service) Remember(ctx context.Context, w MemoryWrite) (*Chunk, error) {
	if w.ProfileID == "" {
		return nil, errors.New("rag: ProfileID required")
	}
	if strings.TrimSpace(w.Text) == "" {
		return nil, errors.New("rag: empty memory text")
	}
	kind := w.Kind
	if kind == "" {
		kind = KindProjectFact
	}
	if _, ok := validMemoryKinds[kind]; !ok {
		return nil, fmt.Errorf("rag: invalid memory kind %q", kind)
	}

	if kind != KindCorrection && instructionInjectionRe.MatchString(w.Text) {
		return nil, errors.New("rag: memory text matches instruction-injection pattern; refused")
	}

	if s.memStore == nil {
		return nil, errors.New("rag: memory store unavailable")
	}
	/*
		Serialize memory writes: pick+enforce the embedder, embed, and INSERT as one
		critical section so a concurrent embedder-identity change cannot interleave.
	*/
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()

	embedder, err := s.pickAnyEmbedder(ctx)
	if err != nil {
		return nil, err
	}

	vectors, err := embedder.EmbedTexts(ctx, []string{w.Text})
	if err != nil {
		return nil, fmt.Errorf("rag: embed memory: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, errors.New("rag: embedder returned no vector for memory text")
	}
	if err := s.memStore.SetEmbedder(ctx, embedder.Identity(), len(vectors[0])); err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	imp := w.Importance
	if imp <= 0 {
		imp = defaultImportance(kind, w.Pinned)
	}

	/* Nanosecond CreatedAt keeps the deterministic ID unique without an ordinal. */
	chunk := Chunk{
		ID:           memoryChunkID(w.ProfileID, kind, now, w.Text, 0),
		Source:       "user-input",
		Heading:      kind,
		Text:         w.Text,
		Vector:       vectors[0],
		Kind:         kind,
		Subject:      w.ProfileID,
		Importance:   imp,
		Pinned:       w.Pinned,
		CreatedAt:    now,
		LastAccessed: now,
	}
	if err := s.memStore.Put(ctx, convomem.MemoryRecord{
		ID: chunk.ID, Subject: chunk.Subject, Kind: chunk.Kind, Text: chunk.Text,
		Vector: chunk.Vector, Importance: chunk.Importance, Pinned: chunk.Pinned,
		CreatedAt: chunk.CreatedAt, LastAccessed: chunk.LastAccessed,
	}); err != nil {
		return nil, err
	}
	s.Logger.Info("rag: memory stored",
		"profile", profileHash(w.ProfileID),
		"kind", kind,
		"importance", imp,
		"pinned", w.Pinned,
	)
	return &chunk, nil
}

/*
Forget removes one memory chunk by ID, scoped to the given profile so a
caller can never delete another profile's memory by guessing IDs.
*/
func (s *Service) Forget(ctx context.Context, profileID, chunkID string) error {
	if profileID == "" || chunkID == "" {
		return errors.New("rag: profileID and chunkID required")
	}
	if s.memStore == nil {
		return errors.New("rag: memory store unavailable")
	}
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	deleted, err := s.memStore.Delete(ctx, profileID, chunkID)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("rag: memory %s not found for profile", chunkID)
	}
	s.Logger.Info("rag: memory forgotten",
		"profile", profileHash(profileID),
		"id", chunkID,
	)
	return nil
}

/*
ListMemory returns the memory chunks owned by the given profile, with
vectors stripped so the client can list them without leaking embeddings.
*/
func (s *Service) ListMemory(profileID string) ([]Chunk, error) {
	if profileID == "" {
		return nil, errors.New("rag: profileID required")
	}
	if s.memStore == nil {
		return nil, nil
	}
	recs, err := s.memStore.Candidates(context.Background(), profileID, false)
	if err != nil {
		return nil, err
	}
	out := make([]Chunk, 0, len(recs))
	for _, r := range recs {
		out = append(out, Chunk{
			ID: r.ID, Source: "user-input", Heading: r.Kind, Text: r.Text,
			Kind: r.Kind, Subject: r.Subject, Importance: r.Importance,
			Pinned: r.Pinned, CreatedAt: r.CreatedAt, LastAccessed: r.LastAccessed,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

/*
SearchMemory runs cosine top-K over the memory collection filtered to the
given profile, applying a soft-decay boost based on importance and recency.
Returns at most k hits above the configured threshold.
*/
func (s *Service) SearchMemory(ctx context.Context, query, profileID string, k int) ([]SearchResult, error) {
	if profileID == "" || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if s.memStore == nil {
		return nil, nil
	}
	candidates, err := s.memStore.Candidates(ctx, profileID, true)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	embedder, err := s.pickAnyEmbedder(ctx)
	if err != nil {
		return nil, err
	}
	vecs, err := embedder.EmbedTexts(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	qv := vecs[0]
	qNormSq := normSquared(qv)
	if qNormSq == 0 {
		return nil, errors.New("rag: zero-norm memory query vector")
	}
	if k <= 0 {
		k = 4
	}
	now := time.Now().UTC()
	type scored struct {
		rec   convomem.MemoryRecord
		score float32
	}
	var pool []scored
	for _, c := range candidates {
		if len(c.Vector) != len(qv) {
			continue
		}
		base := cosine(qv, c.Vector, qNormSq)
		if base < s.Threshold {
			continue
		}
		score := base + 0.10*c.Importance + 0.05*recencyFactor(c.LastAccessed, now)
		pool = append(pool, scored{rec: c, score: score})
	}
	sort.SliceStable(pool, func(a, b int) bool { return pool[a].score > pool[b].score })
	if len(pool) > k {
		pool = pool[:k]
	}
	out := make([]SearchResult, 0, len(pool))
	for _, sc := range pool {
		r := sc.rec
		out = append(out, SearchResult{
			Collection: MemoryCollectionName,
			Chunk: Chunk{
				ID: r.ID, Source: "user-input", Heading: r.Kind, Text: r.Text,
				Vector: r.Vector, Kind: r.Kind, Subject: r.Subject,
				Importance: r.Importance, Pinned: r.Pinned,
				CreatedAt: r.CreatedAt, LastAccessed: r.LastAccessed,
			},
			Score: sc.score,
		})
	}
	return out, nil
}

/* embedderShim is the subset of llm.Embedder this file needs. */
type embedderShim interface {
	Identity() string
	Dim() int
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

/*
pickAnyEmbedder resolves the private-memory embedding backend. Once the SQLite
store has pinned an embedder (first write or migration), that space is
authoritative and any explicit operator preference must agree. Before anything is
pinned, MemoryEmbedderID chooses; without one, exactly one registered embedder is
required — multiple candidates fail closed so memory text is never sent to a
random local or remote backend.
*/
func (s *Service) pickAnyEmbedder(ctx context.Context) (embedderShim, error) {
	if s.memStore != nil {
		id, _, ok, err := s.memStore.Embedder(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			if s.MemoryEmbedderID != "" && id != s.MemoryEmbedderID {
				return nil, fmt.Errorf(
					"rag: memory embedder %q != configured memory embedder %q",
					id, s.MemoryEmbedderID,
				)
			}
			e, avail := s.Embedders[id]
			if !avail {
				return nil, fmt.Errorf("rag: memory requires unavailable embedder %q", id)
			}
			return e, nil
		}
	}

	if s.MemoryEmbedderID != "" {
		e, ok := s.Embedders[s.MemoryEmbedderID]
		if !ok {
			return nil, fmt.Errorf("rag: configured memory embedder %q is unavailable", s.MemoryEmbedderID)
		}
		return e, nil
	}
	if len(s.Embedders) == 1 {
		for _, e := range s.Embedders {
			return e, nil
		}
	}
	if len(s.Embedders) == 0 {
		return nil, errors.New("rag: no embedders registered")
	}
	ids := make([]string, 0, len(s.Embedders))
	for id := range s.Embedders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return nil, fmt.Errorf("rag: ambiguous memory embedder across %v; configure one explicitly", ids)
}

/* defaultImportance returns a kind-specific default boost weight. */
func defaultImportance(kind string, pinned bool) float32 {
	if pinned {
		return 1.0
	}
	switch kind {
	case KindCorrection:
		return 0.8
	case KindDecision:
		return 0.6
	case KindPreference:
		return 0.5
	default:
		return 0.4
	}
}

/* recencyFactor decays from 1.0 toward 0 as a memory ages. */
func recencyFactor(lastAccessedRFC3339 string, now time.Time) float32 {
	if lastAccessedRFC3339 == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, lastAccessedRFC3339)
	if err != nil {
		return 0
	}
	days := now.Sub(t).Hours() / 24
	if days < 0 {
		days = 0
	}
	return float32(1.0 / (1.0 + 0.05*days))
}

/* memoryChunkID is deterministic from profile + kind + created_at + text + ordinal. */
func memoryChunkID(profile, kind, createdAt, text string, ordinal int) string {
	h := sha256.New()
	_, _ = h.Write([]byte(profile))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(createdAt))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(text))
	_, _ = h.Write([]byte("\x00"))
	_, _ = fmt.Fprintf(h, "%d", ordinal)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

/* profileHash returns a privacy-friendly fingerprint of a profile ID for logs. */
func profileHash(profileID string) string {
	if profileID == "" {
		return ""
	}
	h := sha256.Sum256([]byte(profileID))
	return hex.EncodeToString(h[:])[:8]
}

/* unused math import shim — math is used by recencyFactor through float arithmetic */
var _ = math.Sqrt
