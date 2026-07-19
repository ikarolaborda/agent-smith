package novelty

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestBrokerUsesFixedBoundedEndpointAndCache(t *testing.T) {
	doer := &fakeDoer{body: `{"result":"none"}`, status: http.StatusOK}
	evidenceStore := &fakeEvidenceStore{}
	broker, err := NewBroker(doer, evidenceStore, []Source{{Name: "nvd", Kind: "nvd", BaseURL: "https://services.nvd.nist.gov/rest/json/cves/2.0", QueryParam: "keywordSearch"}}, 1024, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first, err := broker.Lookup(context.Background(), "campaign", "nvd", "parser overflow")
	if err != nil {
		t.Fatal(err)
	}
	second, err := broker.Lookup(context.Background(), "campaign", "nvd", "parser overflow")
	if err != nil {
		t.Fatal(err)
	}
	if doer.calls != 1 || first.ResponseHash != second.ResponseHash || first.ArtifactID == second.ArtifactID {
		t.Fatalf("calls=%d first=%#v second=%#v", doer.calls, first, second)
	}
	if !strings.HasPrefix(doer.lastURL, "https://services.nvd.nist.gov/rest/json/cves/2.0?") || !strings.Contains(doer.lastURL, "keywordSearch=parser+overflow") {
		t.Fatalf("request URL=%s", doer.lastURL)
	}
}

func TestBrokerRejectsArbitraryOrOversizedSources(t *testing.T) {
	if _, err := NewBroker(&fakeDoer{}, &fakeEvidenceStore{}, []Source{{Name: "bad", Kind: "nvd", BaseURL: "http://127.0.0.1/admin", QueryParam: "q"}}, 10, time.Minute); err == nil {
		t.Fatal("accepted non-HTTPS source")
	}
	broker, err := NewBroker(&fakeDoer{body: strings.Repeat("x", 11), status: 200}, &fakeEvidenceStore{}, []Source{{Name: "fixed", Kind: "nvd", BaseURL: "https://example.test/search", QueryParam: "q"}}, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Lookup(context.Background(), "campaign", "fixed", "query"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error=%v", err)
	}
}

func TestNoveltyRemainsUnverifiedAfterEveryNoMatch(t *testing.T) {
	var evidence []domain.SourceEvidence
	var reviews []domain.SourceReview
	now := time.Now().UTC()
	for index, kind := range RequiredKinds {
		id := "evidence-" + kind
		evidence = append(evidence, domain.SourceEvidence{ID: id, CampaignID: "campaign", Kind: kind, ArtifactID: "artifact-" + kind})
		reviews = append(reviews, domain.SourceReview{ID: "review-" + kind, CampaignID: "campaign", SourceEvidenceID: id, Kind: kind, Status: "no_match", Summary: "no matching record found", ReviewedBy: "reviewer", ReviewedAt: now.Add(time.Duration(index) * time.Second)})
	}
	decision := Evaluate(evidence, reviews)
	if !decision.Complete || decision.Known || decision.Status != "novelty_unverified" || !NoveltyFacts(decision).NoveltyChecksRecorded {
		t.Fatalf("decision=%#v", decision)
	}
	reviews[0].Status = "match"
	decision = Evaluate(evidence, reviews)
	if !decision.Known || decision.Status != "known_or_duplicate" {
		t.Fatalf("known decision=%#v", decision)
	}
}

func TestBranchFactsRequireEvidenceOrUntestedReason(t *testing.T) {
	now := time.Now().UTC()
	checks := []domain.RevisionCheck{
		{Revision: "main", Status: "affected", BuildID: "build", RunID: "run", EvidenceIDs: []string{"input"}, CheckedAt: now},
		{Revision: "stable", Status: "untested", Reason: "toolchain no longer supported", CheckedAt: now},
	}
	facts, gates := BranchFacts([]string{"main", "stable"}, checks)
	if !facts.BranchChecksRecorded || len(gates) != 2 {
		t.Fatalf("facts=%#v gates=%#v", facts, gates)
	}
	checks[1].Reason = ""
	if facts, _ := BranchFacts([]string{"main", "stable"}, checks); facts.BranchChecksRecorded {
		t.Fatal("accepted unexplained untested revision")
	}
}

type fakeDoer struct {
	body    string
	status  int
	calls   int
	lastURL string
}

func (f *fakeDoer) Do(request *http.Request) (*http.Response, error) {
	f.calls++
	f.lastURL = request.URL.String()
	return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(f.body)), Header: http.Header{}}, nil
}

type fakeEvidenceStore struct {
	evidence []domain.SourceEvidence
	nextID   int
}

func (f *fakeEvidenceStore) SaveSourceEvidence(_ context.Context, evidence domain.SourceEvidence) error {
	f.evidence = append(f.evidence, evidence)
	return nil
}

func (f *fakeEvidenceStore) PutArtifact(_ context.Context, artifact domain.Artifact, body io.Reader) (domain.Artifact, error) {
	f.nextID++
	artifact.ID = fmt.Sprintf("artifact-%d", f.nextID)
	_, _ = io.ReadAll(body)
	return artifact, nil
}
