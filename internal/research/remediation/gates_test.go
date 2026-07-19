package remediation

import (
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestValidateRequiresApprovalAndEveryCleanRun(t *testing.T) {
	now := time.Now().UTC()
	clean := func(id string) domain.ExperimentRun {
		return domain.ExperimentRun{ID: id, Status: domain.RunCompleted, Exit: domain.RunExit{Code: 0}}
	}
	input := Inputs{
		ID: "validation", CampaignID: "campaign", FindingID: "finding", PatchArtifactID: "patch",
		Approval:      domain.Approval{ID: "approval", CampaignID: "campaign", Operation: domain.OperationRegressionTest, Status: "approved"},
		FixBuild:      domain.Build{ID: "fix-build", CampaignID: "campaign", Status: "completed"},
		ReproducerRun: clean("reproducer"), RegressionRun: clean("regression"), NegativeControlRun: clean("negative"),
		EvidenceIDs: []string{"build-log", "reproducer-log", "regression-log", "negative-log"}, Now: now,
	}
	validation, facts, err := Validate(input)
	if err != nil || !validation.OriginalSignalGone || !facts.FixEliminatesCrash || facts.FixBuildID != "fix-build" {
		t.Fatalf("validation=%#v facts=%#v err=%v", validation, facts, err)
	}
	input.OriginalSignatureSeen = true
	if _, facts, err := Validate(input); err == nil || facts.FixEliminatesCrash {
		t.Fatalf("signature-present facts=%#v err=%v", facts, err)
	}
	input.OriginalSignatureSeen = false
	input.Approval.Status = "pending"
	if _, _, err := Validate(input); err == nil {
		t.Fatal("accepted unapproved fix validation")
	}
}
