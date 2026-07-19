package novelty

import (
	"sort"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

var RequiredKinds = []string{
	"nvd", "vendor_advisory", "upstream_history", "issue_tracker",
	"changelog", "regression_corpus", "duplicate_root_cause",
}

// Decision deliberately has no "novel" state: no-match remains unverified.
type Decision struct {
	Status   string
	Complete bool
	Known    bool
	Checks   []domain.GateCheck
}

// Evaluate converts lookup review statuses into conservative novelty gates.
// Each SourceEvidence status must be human/parser-reviewed as match, no_match,
// unavailable, or error; merely captured HTTP bytes are not a completed check.
func Evaluate(evidence []domain.SourceEvidence, reviews []domain.SourceReview) Decision {
	byID := map[string]domain.SourceEvidence{}
	for _, item := range evidence {
		byID[item.ID] = item
	}
	byKind := map[string]domain.SourceReview{}
	for _, review := range reviews {
		item, exists := byID[review.SourceEvidenceID]
		if !exists || item.CampaignID != review.CampaignID || item.Kind != review.Kind {
			continue
		}
		if _, required := requiredKind(review.Kind); required {
			if current, exists := byKind[review.Kind]; !exists || review.ReviewedAt.After(current.ReviewedAt) {
				byKind[review.Kind] = review
			}
		}
	}
	decision := Decision{Status: "novelty_unverified", Complete: true}
	for _, kind := range RequiredKinds {
		review, ok := byKind[kind]
		if !ok || !reviewStatus(review.Status) {
			decision.Complete = false
			decision.Checks = append(decision.Checks, domain.GateCheck{Name: kind, Status: "missing", Summary: "required lookup has not been reviewed"})
			continue
		}
		item := byID[review.SourceEvidenceID]
		check := domain.GateCheck{Name: kind, Status: review.Status, Summary: review.Summary, EvidenceIDs: []string{item.ID, review.ID}, CheckedAt: review.ReviewedAt}
		if item.ArtifactID != "" {
			check.EvidenceIDs = append(check.EvidenceIDs, item.ArtifactID)
		}
		decision.Checks = append(decision.Checks, check)
		if review.Status == "match" {
			decision.Known = true
			decision.Status = "known_or_duplicate"
		}
	}
	if decision.Complete && !decision.Known {
		// Even exhaustive no-match results do not prove novelty.
		decision.Status = "novelty_unverified"
	}
	return decision
}

// BranchFacts requires a recorded outcome or explicit untested reason per revision.
func BranchFacts(requiredRevisions []string, checks []domain.RevisionCheck) (domain.EvidenceFacts, []domain.GateCheck) {
	byRevision := map[string]domain.RevisionCheck{}
	for _, check := range checks {
		byRevision[check.Revision] = check
	}
	complete := len(requiredRevisions) > 0
	var gates []domain.GateCheck
	for _, revision := range requiredRevisions {
		check, ok := byRevision[revision]
		valid := ok && (check.Status == "affected" || check.Status == "unaffected" || check.Status == "untested")
		if valid && check.Status == "untested" && check.Reason == "" {
			valid = false
		}
		if valid && check.Status != "untested" && (check.BuildID == "" || check.RunID == "" || len(check.EvidenceIDs) == 0) {
			valid = false
		}
		if !valid {
			complete = false
			gates = append(gates, domain.GateCheck{Name: revision, Status: "missing", Summary: "revision result or explicit untested reason required"})
			continue
		}
		gates = append(gates, domain.GateCheck{Name: revision, Status: check.Status, Summary: check.Reason, EvidenceIDs: check.EvidenceIDs, CheckedAt: check.CheckedAt})
	}
	sort.Slice(gates, func(i, j int) bool { return gates[i].Name < gates[j].Name })
	return domain.EvidenceFacts{BranchChecksRecorded: complete}, gates
}

// NoveltyFacts permits review-state progression while preserving unverified status.
func NoveltyFacts(decision Decision) domain.EvidenceFacts {
	return domain.EvidenceFacts{NoveltyChecksRecorded: decision.Complete}
}

func requiredKind(kind string) (int, bool) {
	for index, candidate := range RequiredKinds {
		if candidate == kind {
			return index, true
		}
	}
	return 0, false
}

func reviewStatus(status string) bool {
	return status == "match" || status == "no_match" || status == "unavailable" || status == "error"
}
