package domain

import (
	"errors"
	"fmt"
	"time"
)

/* ErrInvalidTransition is returned for a skipped or reversed evidence state. */
var ErrInvalidTransition = errors.New("research: invalid campaign transition")

/*
EvidenceFacts is the deterministic proof summary consumed by StateMachine. The
facts are set by parsers, stores, runners, and human approvals—not by model prose.
*/
type EvidenceFacts struct {
	ScopeValid               bool
	TargetID                 string
	SourceSHA256             string
	BuildID                  string
	HarnessCount             int
	Instrumented             bool
	FuzzRunID                string
	BudgetExhausted          bool
	CrashInputArtifactID     string
	CrashSignature           string
	CrashMachineParsed       bool
	ReproductionCount        int
	MinimizedArtifactID      string
	MinimizedSameSignature   bool
	SymbolizedFrameCount     int
	RootCause                string
	CrashGroupID             string
	PrimitiveAssessmentID    string
	PrimitiveEvidenceValid   bool
	BranchChecksRecorded     bool
	NoveltyChecksRecorded    bool
	FixBuildID               string
	RegressionRunID          string
	FixEliminatesCrash       bool
	HumanReviewApprovalID    string
	DisclosureApprovalID     string
	HumanPerformedDisclosure bool
}

/* StateMachine enforces monotonic, evidence-backed campaign states. */
type StateMachine struct {
	MinimumReproductions int
}

func (m StateMachine) minimumReproductions() int {
	if m.MinimumReproductions <= 0 {
		return 3
	}
	return m.MinimumReproductions
}

/* Advance returns an updated campaign or a fail-closed validation error. */
func (m StateMachine) Advance(c Campaign, to CampaignState, facts EvidenceFacts, now time.Time) (Campaign, error) {
	if c.ID == "" {
		return c, errors.New("research: campaign id required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if c.State == to {
		return c, nil
	}
	if to == CampaignPaused || to == CampaignCancelled || to == CampaignFailed {
		if terminalCampaignState(c.State) {
			return c, fmt.Errorf("%w: terminal state %s cannot become %s", ErrInvalidTransition, c.State, to)
		}
		c.State, c.UpdatedAt, c.Version = to, now, c.Version+1
		return c, nil
	}
	if c.State == CampaignPaused {
		return c, fmt.Errorf("%w: paused campaign must be explicitly resumed to its prior state", ErrInvalidTransition)
	}

	valid := false
	reason := "required evidence is missing"
	switch {
	case c.State == CampaignDraft && to == CampaignScoped:
		valid = facts.ScopeValid
		reason = "valid authorization scope required"
	case c.State == CampaignScoped && to == CampaignAcquired:
		valid = facts.TargetID != "" && facts.SourceSHA256 != ""
		reason = "immutable target id and source hash required"
	case c.State == CampaignAcquired && to == CampaignBuildReady:
		valid = facts.BuildID != "" && facts.HarnessCount > 0 && facts.Instrumented
		reason = "instrumented build and at least one harness required"
	case c.State == CampaignBuildReady && to == CampaignFuzzing:
		valid = facts.FuzzRunID != ""
		reason = "durable fuzz run required"
	case c.State == CampaignFuzzing && to == CampaignCrashObserved:
		valid = facts.CrashInputArtifactID != "" && facts.CrashSignature != "" && facts.CrashMachineParsed
		reason = "saved crash input and machine-parsed signature required"
	case c.State == CampaignFuzzing && to == CampaignCompletedNoFinding:
		valid = facts.BudgetExhausted && facts.FuzzRunID != ""
		reason = "completed run and exhausted campaign budget required"
	case c.State == CampaignCrashObserved && to == CampaignReproduced:
		valid = facts.ReproductionCount >= m.minimumReproductions()
		reason = fmt.Sprintf("at least %d identical reproductions required", m.minimumReproductions())
	case c.State == CampaignReproduced && to == CampaignMinimized:
		valid = facts.MinimizedArtifactID != "" && facts.MinimizedSameSignature
		reason = "minimized input with identical crash signature required"
	case c.State == CampaignMinimized && to == CampaignRootCaused:
		valid = facts.SymbolizedFrameCount > 0 && facts.RootCause != "" && facts.CrashGroupID != ""
		reason = "symbolized trace, root cause, and crash group required"
	case c.State == CampaignRootCaused && to == CampaignPrimitiveAssessed:
		valid = facts.PrimitiveAssessmentID != "" && facts.PrimitiveEvidenceValid
		reason = "evidence-valid primitive assessment required"
	case c.State == CampaignPrimitiveAssessed && to == CampaignBranchChecked:
		valid = facts.BranchChecksRecorded
		reason = "supported-branch checks required"
	case c.State == CampaignBranchChecked && to == CampaignNoveltyReviewed:
		valid = facts.NoveltyChecksRecorded
		reason = "complete novelty records required"
	case c.State == CampaignNoveltyReviewed && to == CampaignRemediated:
		valid = facts.FixBuildID != "" && facts.RegressionRunID != "" && facts.FixEliminatesCrash
		reason = "validated fix build and regression run required"
	case c.State == CampaignRemediated && to == CampaignReportReady:
		valid = facts.HumanReviewApprovalID != ""
		reason = "human report review approval required"
	case c.State == CampaignReportReady && to == CampaignDisclosed:
		valid = facts.DisclosureApprovalID != "" && facts.HumanPerformedDisclosure
		reason = "human disclosure approval and action required"
	}
	if !valid {
		return c, fmt.Errorf("%w: %s -> %s: %s", ErrInvalidTransition, c.State, to, reason)
	}
	c.State, c.UpdatedAt, c.Version = to, now, c.Version+1
	if to == CampaignAcquired {
		c.TargetID = facts.TargetID
	}
	return c, nil
}

func terminalCampaignState(s CampaignState) bool {
	return s == CampaignDisclosed || s == CampaignCompletedNoFinding || s == CampaignCancelled || s == CampaignFailed
}

// IsTerminalCampaignState reports whether evidence collection has reached a
// state in which approval-gated custody cleanup may be considered.
func IsTerminalCampaignState(state CampaignState) bool { return terminalCampaignState(state) }

var findingRank = map[FindingLabel]int{
	FindingHypothesis:             0,
	FindingObservation:            1,
	FindingCrashObserved:          2,
	FindingReproducedMemoryIssue:  3,
	FindingPrimitiveCandidate:     4,
	FindingPrimitiveConfirmed:     5,
	FindingCandidateVulnerability: 6,
}

/* PromoteFinding enforces monotonic evidence labels independent of prose. */
func (m StateMachine) PromoteFinding(f Finding, to FindingLabel, facts EvidenceFacts, now time.Time) (Finding, error) {
	fromRank, ok := findingRank[f.Label]
	if !ok {
		return f, fmt.Errorf("research: unknown finding label %q", f.Label)
	}
	toRank, ok := findingRank[to]
	if !ok || toRank < fromRank || toRank > fromRank+1 {
		return f, fmt.Errorf("research: invalid finding promotion %s -> %s", f.Label, to)
	}
	valid := toRank == fromRank
	switch to {
	case FindingObservation:
		valid = len(f.EvidenceIDs) > 0
	case FindingCrashObserved:
		valid = facts.CrashInputArtifactID != "" && facts.CrashMachineParsed
	case FindingReproducedMemoryIssue:
		valid = facts.ReproductionCount >= m.minimumReproductions()
	case FindingPrimitiveCandidate:
		valid = facts.PrimitiveAssessmentID != ""
	case FindingPrimitiveConfirmed:
		valid = facts.PrimitiveAssessmentID != "" && facts.PrimitiveEvidenceValid
	case FindingCandidateVulnerability:
		valid = facts.BranchChecksRecorded && facts.NoveltyChecksRecorded && facts.HumanReviewApprovalID != ""
	}
	if !valid {
		return f, fmt.Errorf("research: evidence does not permit finding promotion %s -> %s", f.Label, to)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	f.Label, f.UpdatedAt = to, now
	return f, nil
}
