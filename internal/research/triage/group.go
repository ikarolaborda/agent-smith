package triage

import (
	"errors"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

// AddToGroup creates or updates exactly one signature-compatible crash group.
func AddToGroup(group domain.CrashGroup, observation domain.CrashObservation, groupID string, now time.Time) (domain.CrashGroup, error) {
	if observation.Signature == "" || observation.ID == "" || observation.CampaignID == "" {
		return group, errors.New("research triage: signed observation required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if group.ID == "" {
		if groupID == "" {
			return group, errors.New("research triage: group id required")
		}
		return domain.CrashGroup{
			SchemaVersion: 1, ID: groupID, CampaignID: observation.CampaignID, Signature: observation.Signature,
			ObservationIDs: []string{observation.ID}, CanonicalInputID: observation.InputArtifactID, CreatedAt: now, UpdatedAt: now,
		}, nil
	}
	if group.CampaignID != observation.CampaignID || group.Signature != observation.Signature {
		return group, errors.New("research triage: observation does not match crash group")
	}
	for _, id := range group.ObservationIDs {
		if id == observation.ID {
			return group, nil
		}
	}
	group.ObservationIDs = append(group.ObservationIDs, observation.ID)
	group.UpdatedAt = now
	if group.CanonicalInputID == "" {
		group.CanonicalInputID = observation.InputArtifactID
	}
	return group, nil
}

// ReproductionFacts requires identical signatures across independent attempts.
func ReproductionFacts(attempts []domain.CrashObservation, minimum int) domain.EvidenceFacts {
	if minimum <= 0 {
		minimum = 3
	}
	facts := domain.EvidenceFacts{}
	if len(attempts) == 0 || attempts[0].Signature == "" {
		return facts
	}
	signature := attempts[0].Signature
	for _, attempt := range attempts {
		if attempt.Signature != signature || attempt.InputArtifactID == "" {
			return domain.EvidenceFacts{}
		}
		facts.ReproductionCount++
	}
	if facts.ReproductionCount < minimum {
		return facts
	}
	facts.CrashSignature = signature
	facts.CrashInputArtifactID = attempts[0].InputArtifactID
	facts.CrashMachineParsed = true
	return facts
}

// MinimizationFacts accepts only a no-larger input preserving the signature.
func MinimizationFacts(original, minimized domain.Artifact, originalObservation, minimizedObservation domain.CrashObservation) domain.EvidenceFacts {
	return domain.EvidenceFacts{
		MinimizedArtifactID: minimized.ID,
		MinimizedSameSignature: original.ID != "" && minimized.ID != "" && minimized.Size <= original.Size &&
			originalObservation.Signature != "" && originalObservation.Signature == minimizedObservation.Signature,
	}
}
