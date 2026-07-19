// Package novelty performs fixed-destination, bounded advisory/history lookups
// and conservative novelty/branch decisions.
package novelty

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const defaultResponseLimit int64 = 2 << 20

// Source is an operator-defined fixed lookup protocol, never a model URL.
type Source struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	BaseURL    string            `json:"base_url"`
	QueryParam string            `json:"query_param"`
	Headers    map[string]string `json:"headers,omitempty"`
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type EvidenceStore interface {
	SaveSourceEvidence(context.Context, domain.SourceEvidence) error
	PutArtifact(context.Context, domain.Artifact, io.Reader) (domain.Artifact, error)
}

type cacheEntry struct {
	body       []byte
	statusCode int
	checkedAt  time.Time
}

// Broker constrains lookups to configured HTTPS endpoints and response caps.
type Broker struct {
	doer     HTTPDoer
	store    EvidenceStore
	sources  map[string]Source
	maxBytes int64
	cacheTTL time.Duration
	mu       sync.Mutex
	cache    map[string]cacheEntry
}

func NewBroker(doer HTTPDoer, evidence EvidenceStore, sources []Source, maxBytes int64, cacheTTL time.Duration) (*Broker, error) {
	if doer == nil || evidence == nil {
		return nil, errors.New("novelty: HTTP client and evidence store required")
	}
	if maxBytes <= 0 {
		maxBytes = defaultResponseLimit
	}
	if cacheTTL <= 0 {
		cacheTTL = 15 * time.Minute
	}
	broker := &Broker{doer: doer, store: evidence, sources: map[string]Source{}, maxBytes: maxBytes, cacheTTL: cacheTTL, cache: map[string]cacheEntry{}}
	for _, source := range sources {
		parsed, err := url.Parse(source.BaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || source.Name == "" || !IsRequiredKind(source.Kind) || source.QueryParam == "" {
			return nil, fmt.Errorf("novelty: invalid fixed HTTPS source %q", source.Name)
		}
		if _, exists := broker.sources[source.Name]; exists {
			return nil, fmt.Errorf("novelty: duplicate source %q", source.Name)
		}
		for key := range source.Headers {
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
				return nil, errors.New("novelty: credentials require a dedicated transport")
			}
		}
		broker.sources[source.Name] = source
	}
	return broker, nil
}

// Lookup captures a bounded response as content-addressed evidence.
func (b *Broker) Lookup(ctx context.Context, campaignID, findingID, sourceName, query string) (domain.SourceEvidence, error) {
	source, ok := b.sources[sourceName]
	if !ok {
		return domain.SourceEvidence{}, errors.New("novelty: unknown lookup source")
	}
	query = strings.TrimSpace(query)
	if campaignID == "" || findingID == "" || query == "" || len(query) > 2048 {
		return domain.SourceEvidence{}, errors.New("novelty: campaign, finding, and bounded query required")
	}
	requestURL, err := url.Parse(source.BaseURL)
	if err != nil {
		return domain.SourceEvidence{}, err
	}
	values := requestURL.Query()
	values.Set(source.QueryParam, query)
	requestURL.RawQuery = values.Encode()
	cacheKey := sourceName + "\x00" + query
	entry, cached := b.cached(cacheKey)
	if !cached {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
		if err != nil {
			return domain.SourceEvidence{}, err
		}
		request.Header.Set("Accept", "application/json, text/plain;q=0.8")
		request.Header.Set("User-Agent", "agent-smith-research/1")
		for key, value := range source.Headers {
			if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") {
				return domain.SourceEvidence{}, errors.New("novelty: credentials must be configured on a dedicated transport, not source metadata")
			}
			request.Header.Set(key, value)
		}
		response, err := b.doer.Do(request)
		if err != nil {
			record := newEvidence(campaignID, findingID, source, query, requestURL.String(), "unavailable", time.Now().UTC())
			record.Summary = err.Error()
			if saveErr := b.store.SaveSourceEvidence(ctx, record); saveErr != nil {
				return record, saveErr
			}
			return record, err
		}
		if response == nil || response.Body == nil || response.Request == nil || response.Request.URL == nil || !sameOrigin(requestURL, response.Request.URL) {
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			return domain.SourceEvidence{}, errors.New("novelty: lookup redirected outside its fixed origin")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, b.maxBytes+1))
		if err != nil {
			return domain.SourceEvidence{}, err
		}
		if int64(len(body)) > b.maxBytes {
			return domain.SourceEvidence{}, errors.New("novelty: lookup response exceeds limit")
		}
		entry = cacheEntry{body: body, statusCode: response.StatusCode, checkedAt: time.Now().UTC()}
		b.mu.Lock()
		b.cache[cacheKey] = entry
		b.mu.Unlock()
	}
	status := "captured"
	if entry.statusCode < 200 || entry.statusCode >= 300 {
		status = "error"
	}
	record := newEvidence(campaignID, findingID, source, query, requestURL.String(), status, entry.checkedAt)
	digest := sha256.Sum256(entry.body)
	record.ResponseHash = "sha256:" + hex.EncodeToString(digest[:])
	record.Metadata = map[string]string{"http_status": fmt.Sprint(entry.statusCode), "cache": fmt.Sprint(cached)}
	artifact, err := b.store.PutArtifact(ctx, domain.Artifact{
		SchemaVersion: 1, CampaignID: campaignID, ParentIDs: []string{findingID}, Role: "lookup_response", MediaType: "application/octet-stream", Sensitivity: "embargoed",
	}, bytes.NewReader(entry.body))
	if err != nil {
		return record, err
	}
	record.ArtifactID = artifact.ID
	record.Summary = fmt.Sprintf("captured %d-byte response with HTTP status %d", len(entry.body), entry.statusCode)
	if err := b.store.SaveSourceEvidence(ctx, record); err != nil {
		return record, err
	}
	if status == "error" {
		return record, fmt.Errorf("novelty: source returned HTTP %d", entry.statusCode)
	}
	return record, nil
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func (b *Broker) cached(key string) (cacheEntry, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.cache[key]
	return entry, ok && time.Since(entry.checkedAt) <= b.cacheTTL
}

func newEvidence(campaignID, findingID string, source Source, query, requestURL, status string, checked time.Time) domain.SourceEvidence {
	return domain.SourceEvidence{SchemaVersion: 1, ID: randomID("source"), CampaignID: campaignID, FindingID: findingID, Kind: source.Kind, SourceName: source.Name, Query: query, RequestURL: requestURL, Status: status, CheckedAt: checked}
}

func randomID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
