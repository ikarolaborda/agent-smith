// Package remediation validates fixes and regression evidence without applying
// patches on the control-plane host.
package remediation

import (
	"errors"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

// Inputs are deterministic runner/build outcomes for one candidate patch.
type Inputs struct {
	ID                    string
	CampaignID            string
	FindingID             string
	PatchArtifactID       string
	Approval              domain.Approval
	FixBuild              domain.Build
	ReproducerRun         domain.ExperimentRun
	RegressionRun         domain.ExperimentRun
	NegativeControlRun    domain.ExperimentRun
	OriginalSignatureSeen bool
	EvidenceIDs           []string
	Now                   time.Time
}

// Validate refuses prose-only fixes and returns transition facts only on a
// successful build, clean original reproducer, regression, and negative control.
func Validate(input Inputs) (domain.RemediationValidation, domain.EvidenceFacts, error) {
	validation := domain.RemediationValidation{
		SchemaVersion: 1, ID: input.ID, CampaignID: input.CampaignID, FindingID: input.FindingID,
		PatchArtifactID: input.PatchArtifactID, ApprovalID: input.Approval.ID, FixBuildID: input.FixBuild.ID,
		ReproducerRunID: input.ReproducerRun.ID, RegressionRunID: input.RegressionRun.ID,
		NegativeControlRunID: input.NegativeControlRun.ID, EvidenceIDs: append([]string(nil), input.EvidenceIDs...),
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	}
	validation.ValidatedAt = input.Now
	if validation.ID == "" || validation.CampaignID == "" || validation.FindingID == "" || validation.PatchArtifactID == "" {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: validation, campaign, finding, and patch identities required")
	}
	if input.Approval.Status != "approved" || input.Approval.Operation != domain.OperationRegressionTest || input.Approval.CampaignID != input.CampaignID {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: matching human approval required")
	}
	if input.FixBuild.ID == "" || input.FixBuild.CampaignID != input.CampaignID || input.FixBuild.Status != "completed" {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: successful fix build required")
	}
	if !completedClean(input.ReproducerRun) || !completedClean(input.RegressionRun) || !completedClean(input.NegativeControlRun) {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: completed clean reproducer, regression, and negative-control runs required")
	}
	if input.OriginalSignatureSeen {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: original crash signature remains")
	}
	if len(input.EvidenceIDs) < 4 {
		return validation, domain.EvidenceFacts{}, errors.New("remediation: retained build and run evidence required")
	}
	validation.OriginalSignalGone, validation.RegressionPassed, validation.NegativeControlClean = true, true, true
	facts := domain.EvidenceFacts{FixBuildID: input.FixBuild.ID, RegressionRunID: input.RegressionRun.ID, FixEliminatesCrash: true}
	return validation, facts, nil
}

func completedClean(run domain.ExperimentRun) bool {
	return run.ID != "" && run.Status == domain.RunCompleted && run.Exit.Code == 0 && run.Exit.Signal == ""
}
