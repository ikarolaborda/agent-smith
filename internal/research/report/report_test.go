package report

import (
	"strings"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestPrivateDraftIsEvidenceLinkedAndNeverClaimsNovelty(t *testing.T) {
	unknown := domain.EvidenceValue{}
	input := DraftInputs{
		Campaign: domain.Campaign{ID: "campaign"}, Target: domain.TargetRevision{ID: "target", Commit: "abc"},
		Finding: domain.Finding{
			ID: "finding", Title: "Parser out-of-bounds write", Label: domain.FindingCandidateVulnerability, RootCause: "Unchecked length reaches a fixed write.",
			EvidenceIDs: []string{"observation"}, BranchChecks: []domain.GateCheck{{Name: "main", Status: "affected", EvidenceIDs: []string{"run"}}},
			NoveltyChecks: []domain.GateCheck{{Name: "nvd", Status: "no_match", EvidenceIDs: []string{"lookup"}}},
		},
		Group: domain.CrashGroup{ID: "group"},
		Primitive: domain.PrimitiveAssessment{ID: "primitive", CampaignID: "campaign", CrashGroupID: "group", Operation: domain.PrimitiveOOBWrite, OperationEvidence: []string{"observation"},
			AttackerControl: unknown, AccessWidth: unknown, ValueControl: unknown, TargetRelation: unknown, Repeatability: unknown, Reachability: unknown, Mitigations: unknown, ExploitabilityGap: unknown},
		Reviewer:      domain.Approval{ID: "approval", CampaignID: "campaign", Operation: domain.OperationDraftReport, Status: "approved"},
		NoveltyStatus: "novel",
		Artifacts:     []domain.Artifact{{ID: "artifact", Role: "crashing_input", ContentID: "sha256:abc", Size: 4}},
	}
	draft, err := RenderPrivateDraft(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Novelty: `novelty_unverified`", "Disclosure: not performed", "`artifact`", "authorized human"} {
		if !strings.Contains(draft, expected) {
			t.Fatalf("draft missing %q:\n%s", expected, draft)
		}
	}
	input.Reviewer.Status = "pending"
	if _, err := RenderPrivateDraft(input); err == nil {
		t.Fatal("accepted draft without review approval")
	}
}
