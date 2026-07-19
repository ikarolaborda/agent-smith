// Package report renders private evidence-linked finding drafts. It has no
// network or disclosure capability by design.
package report

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

// DraftInputs are durable objects already loaded by the control plane.
type DraftInputs struct {
	Campaign      domain.Campaign
	Target        domain.TargetRevision
	Finding       domain.Finding
	Group         domain.CrashGroup
	Primitive     domain.PrimitiveAssessment
	Artifacts     []domain.Artifact
	Reviewer      domain.Approval
	NoveltyStatus string
}

// RenderPrivateDraft creates a conservative Markdown report and never labels a
// no-match search result as novel.
func RenderPrivateDraft(input DraftInputs) (string, error) {
	if input.Campaign.ID == "" || input.Target.ID == "" || input.Finding.ID == "" || input.Group.ID == "" {
		return "", errors.New("report: campaign, target, finding, and crash group required")
	}
	if input.Finding.Label != domain.FindingCandidateVulnerability {
		return "", errors.New("report: candidate-vulnerability evidence label required")
	}
	if err := domain.ValidatePrimitive(input.Primitive); err != nil {
		return "", err
	}
	if input.Reviewer.Status != "approved" || input.Reviewer.Operation != domain.OperationDraftReport || input.Reviewer.CampaignID != input.Campaign.ID {
		return "", errors.New("report: matching human report approval required")
	}
	if len(input.Finding.BranchChecks) == 0 || len(input.Finding.NoveltyChecks) == 0 || len(input.Finding.EvidenceIDs) == 0 {
		return "", errors.New("report: branch, novelty, and finding evidence required")
	}
	novelty := input.NoveltyStatus
	if novelty == "" || novelty == "novel" {
		novelty = "novelty_unverified"
	}
	artifacts := append([]domain.Artifact(nil), input.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	var output strings.Builder
	fmt.Fprintf(&output, "# Private vulnerability research draft: %s\n\n", clean(input.Finding.Title))
	fmt.Fprintf(&output, "- Campaign: `%s`\n- Target commit: `%s`\n- Evidence label: `%s`\n- Novelty: `%s`\n- Disclosure: not performed\n\n",
		input.Campaign.ID, input.Target.Commit, input.Finding.Label, novelty)
	fmt.Fprintf(&output, "## Summary\n\n%s\n\n## Root cause\n\n%s\n\n", clean(input.Finding.Title), clean(input.Finding.RootCause))
	fmt.Fprintf(&output, "## Demonstrated primitive\n\n- Operation: `%s`\n", input.Primitive.Operation)
	for _, field := range []struct {
		name  string
		value domain.EvidenceValue
	}{
		{"Attacker control", input.Primitive.AttackerControl}, {"Access width", input.Primitive.AccessWidth},
		{"Value control", input.Primitive.ValueControl}, {"Target relation", input.Primitive.TargetRelation},
		{"Repeatability", input.Primitive.Repeatability}, {"Reachability", input.Primitive.Reachability},
		{"Mitigations", input.Primitive.Mitigations}, {"Exploitability gap", input.Primitive.ExploitabilityGap},
	} {
		value := "unknown"
		if field.value.Known {
			value = clean(field.value.Value) + " (evidence: " + strings.Join(field.value.EvidenceIDs, ", ") + ")"
		}
		fmt.Fprintf(&output, "- %s: %s\n", field.name, value)
	}
	fmt.Fprint(&output, "\n## Affected-revision checks\n\n")
	writeChecks(&output, input.Finding.BranchChecks)
	fmt.Fprint(&output, "\n## Novelty checks\n\n")
	writeChecks(&output, input.Finding.NoveltyChecks)
	fmt.Fprint(&output, "\n## Evidence inventory\n\n")
	for _, artifact := range artifacts {
		fmt.Fprintf(&output, "- `%s` — %s, `%s`, %d bytes\n", artifact.ID, clean(artifact.Role), artifact.ContentID, artifact.Size)
	}
	fmt.Fprint(&output, "\n## Disclosure control\n\nThis is a private draft. Agent Smith does not transmit or publish it; an authorized human must make and record any external disclosure decision.\n")
	return output.String(), nil
}

func writeChecks(output *strings.Builder, checks []domain.GateCheck) {
	for _, check := range checks {
		fmt.Fprintf(output, "- %s: `%s` — %s", clean(check.Name), check.Status, clean(check.Summary))
		if len(check.EvidenceIDs) > 0 {
			fmt.Fprintf(output, " (evidence: %s)", strings.Join(check.EvidenceIDs, ", "))
		}
		output.WriteByte('\n')
	}
}

func clean(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\x00", ""), "\r", ""))
}
