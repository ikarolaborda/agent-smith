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

	embedder, err := s.pickAnyEmbedder()
	if err != nil {
		return nil, err
	}

	col, err := s.openOrCreateMemoryCollection(embedder)
	if err != nil {
		return nil, err
	}

	vectors, err := embedder.EmbedTexts(ctx, []string{w.Text})
	if err != nil {
		return nil, fmt.Errorf("rag: embed memory: %w", err)
	}
	if len(vectors) != 1 {
		return nil, errors.New("rag: embedder returned no vector for memory text")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	imp := w.Importance
	if imp <= 0 {
		imp = defaultImportance(kind, w.Pinned)
	}

	chunk := Chunk{
		ID:           memoryChunkID(w.ProfileID, kind, now, w.Text),
		Source:       "user-input",
		Heading:      kind,
		Ordinal:      len(col.Chunks),
		Text:         w.Text,
		Vector:       vectors[0],
		Kind:         kind,
		Subject:      w.ProfileID,
		Importance:   imp,
		Pinned:       w.Pinned,
		CreatedAt:    now,
		LastAccessed: now,
	}
	col.Chunks = append(col.Chunks, chunk)

	if err := s.Store.Save(col); err != nil {
		return nil, err
	}
	s.Index.Replace(col)
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
	col, err := s.Store.Load(MemoryCollectionName)
	if err != nil {
		return err
	}
	idx := -1
	for i, c := range col.Chunks {
		if c.ID == chunkID && c.Subject == profileID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("rag: memory %s not found for profile", chunkID)
	}
	col.Chunks = append(col.Chunks[:idx], col.Chunks[idx+1:]...)
	if err := s.Store.Save(col); err != nil {
		return err
	}
	s.Index.Replace(col)
	s.Logger.Info("rag: memory forgotten",
		"profile", profileHash(profileID),
		"id", chunkID,
	)
	_ = ctx
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
	col := s.Index.Get(MemoryCollectionName)
	if col == nil {
		return nil, nil
	}
	out := make([]Chunk, 0, len(col.Chunks))
	for _, c := range col.Chunks {
		if c.Subject != profileID {
			continue
		}
		copy := c
		copy.Vector = nil
		out = append(out, copy)
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
	col := s.Index.Get(MemoryCollectionName)
	if col == nil || len(col.Chunks) == 0 {
		return nil, nil
	}
	embedder, err := s.pickAnyEmbedder()
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
		idx   int
		score float32
	}
	var pool []scored
	for i, c := range col.Chunks {
		if c.Subject != profileID {
			continue
		}
		if len(c.Vector) != len(qv) {
			continue
		}
		base := cosine(qv, c.Vector, qNormSq)
		if base < s.Threshold {
			continue
		}
		score := base + 0.10*c.Importance + 0.05*recencyFactor(c.LastAccessed, now)
		pool = append(pool, scored{idx: i, score: score})
	}
	sort.SliceStable(pool, func(a, b int) bool { return pool[a].score > pool[b].score })
	if len(pool) > k {
		pool = pool[:k]
	}
	out := make([]SearchResult, 0, len(pool))
	for _, s := range pool {
		out = append(out, SearchResult{
			Collection: MemoryCollectionName,
			Chunk:      col.Chunks[s.idx],
			Score:      s.score,
		})
	}
	return out, nil
}

/* openOrCreateMemoryCollection returns the memory collection, creating it on first call. */
func (s *Service) openOrCreateMemoryCollection(embedder embedderShim) (*Collection, error) {
	existing, err := s.Store.Load(MemoryCollectionName)
	if err == nil {
		if existing.Kind == "" {
			existing.Kind = CollectionKindMemory
		}
		if existing.EmbedderID != embedder.Identity() {
			return nil, fmt.Errorf(
				"rag: memory collection embedder %q != requested %q",
				existing.EmbedderID, embedder.Identity(),
			)
		}
		return existing, nil
	}
	now := time.Now().UTC()
	return &Collection{
		Version:    CollectionVersion,
		Name:       MemoryCollectionName,
		Kind:       CollectionKindMemory,
		EmbedderID: embedder.Identity(),
		Dim:        embedder.Dim(),
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

/* embedderShim is the subset of llm.Embedder this file needs. */
type embedderShim interface {
	Identity() string
	Dim() int
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

/* pickAnyEmbedder returns one registered embedder, preferring the one that matches the memory collection. */
func (s *Service) pickAnyEmbedder() (embedderShim, error) {
	if existing, err := s.Store.Load(MemoryCollectionName); err == nil {
		if e, ok := s.Embedders[existing.EmbedderID]; ok {
			return e, nil
		}
	}
	for _, e := range s.Embedders {
		return e, nil
	}
	return nil, errors.New("rag: no embedders registered")
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

/* memoryChunkID is deterministic from profile + kind + created_at + text. */
func memoryChunkID(profile, kind, createdAt, text string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(profile))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(createdAt))
	_, _ = h.Write([]byte("\x00"))
	_, _ = h.Write([]byte(text))
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
