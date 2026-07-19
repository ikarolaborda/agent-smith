package domain

import (
	"errors"
	"testing"
	"time"
)

func campaignFixture() Campaign {
	return Campaign{ID: "campaign-1", State: CampaignDraft, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func TestStateMachineRejectsSkippedState(t *testing.T) {
	m := StateMachine{}
	c := campaignFixture()
	if _, err := m.Advance(c, CampaignBuildReady, EvidenceFacts{BuildID: "b", HarnessCount: 1, Instrumented: true}, time.Now()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped state accepted: %v", err)
	}
}

func TestStateMachineRequiresEvidenceAtEveryGate(t *testing.T) {
	m := StateMachine{MinimumReproductions: 3}
	now := time.Now().UTC()
	c := campaignFixture()
	steps := []struct {
		to    CampaignState
		facts EvidenceFacts
	}{
		{CampaignScoped, EvidenceFacts{ScopeValid: true}},
		{CampaignAcquired, EvidenceFacts{TargetID: "target", SourceSHA256: "sha256:source"}},
		{CampaignBuildReady, EvidenceFacts{BuildID: "build", HarnessCount: 1, Instrumented: true}},
		{CampaignFuzzing, EvidenceFacts{FuzzRunID: "run-fuzz"}},
		{CampaignCrashObserved, EvidenceFacts{CrashInputArtifactID: "artifact", CrashSignature: "sig", CrashMachineParsed: true}},
		{CampaignReproduced, EvidenceFacts{ReproductionCount: 3}},
		{CampaignMinimized, EvidenceFacts{MinimizedArtifactID: "min", MinimizedSameSignature: true}},
		{CampaignRootCaused, EvidenceFacts{SymbolizedFrameCount: 2, RootCause: "off by one", CrashGroupID: "group"}},
		{CampaignPrimitiveAssessed, EvidenceFacts{PrimitiveAssessmentID: "primitive", PrimitiveEvidenceValid: true}},
		{CampaignBranchChecked, EvidenceFacts{BranchChecksRecorded: true}},
		{CampaignNoveltyReviewed, EvidenceFacts{NoveltyChecksRecorded: true}},
		{CampaignRemediated, EvidenceFacts{FixBuildID: "fixed", RegressionRunID: "regression", FixEliminatesCrash: true}},
		{CampaignReportReady, EvidenceFacts{HumanReviewApprovalID: "review"}},
		{CampaignDisclosed, EvidenceFacts{DisclosureApprovalID: "disclose", HumanPerformedDisclosure: true}},
	}
	for _, step := range steps {
		var err error
		c, err = m.Advance(c, step.to, step.facts, now)
		if err != nil {
			t.Fatalf("advance to %s: %v", step.to, err)
		}
	}
	if c.State != CampaignDisclosed || c.Version != int64(1+len(steps)) {
		t.Fatalf("final campaign = %#v", c)
	}
}

func TestFindingPromotionCannotSkipOrInventEvidence(t *testing.T) {
	m := StateMachine{}
	f := Finding{ID: "f", Label: FindingHypothesis, EvidenceIDs: []string{"source"}}
	if _, err := m.PromoteFinding(f, FindingCrashObserved, EvidenceFacts{CrashInputArtifactID: "x", CrashMachineParsed: true}, time.Now()); err == nil {
		t.Fatal("finding skipped observation label")
	}
	var err error
	f, err = m.PromoteFinding(f, FindingObservation, EvidenceFacts{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.PromoteFinding(f, FindingCrashObserved, EvidenceFacts{}, time.Now()); err == nil {
		t.Fatal("crash promotion without artifact accepted")
	}
}

func TestValidatePrimitiveRejectsUnsupportedKnownClaim(t *testing.T) {
	p := PrimitiveAssessment{ID: "p", CampaignID: "c", CrashGroupID: "g", Operation: PrimitiveOOBWrite, OperationEvidence: []string{"observation"}, AccessWidth: EvidenceValue{Known: true, Value: "1 byte"}}
	if err := ValidatePrimitive(p); err == nil {
		t.Fatal("known value without evidence accepted")
	}
	p.AccessWidth.EvidenceIDs = []string{"run-1"}
	if err := ValidatePrimitive(p); err != nil {
		t.Fatalf("evidence-backed primitive rejected: %v", err)
	}
}
