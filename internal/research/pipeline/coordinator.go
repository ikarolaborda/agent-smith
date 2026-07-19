// Package pipeline turns typed worker results into durable evidence without
// trusting a client or model to assign evidence labels.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
	"github.com/ikarolaborda/agent-smith/internal/research/triage"
)

var safeSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
var addressPattern = regexp.MustCompile(`0x[0-9a-fA-F]+`)
var sourceLinePattern = regexp.MustCompile(`(?m)(?:^|\s)(?:/[^\s:]+|[A-Za-z0-9_.-]+):[1-9][0-9]*(?::[0-9]+)?(?:$|\s)`)

// Coordinator is the deterministic post-run evidence ingestion boundary.
type Coordinator struct {
	store                *store.Store
	root                 string
	machine              domain.StateMachine
	minimumReproductions int
}

// New creates private campaign-scoped worker-input storage.
func New(repository *store.Store, configuredMinimum ...int) (*Coordinator, error) {
	if repository == nil {
		return nil, errors.New("research pipeline: store required")
	}
	root := WorkRoot(repository.Root())
	if err := privateDir(root); err != nil {
		return nil, err
	}
	minimum := 3
	if len(configuredMinimum) > 0 && configuredMinimum[0] > 0 {
		minimum = configuredMinimum[0]
	}
	return &Coordinator{store: repository, root: root, machine: domain.StateMachine{MinimumReproductions: minimum}, minimumReproductions: minimum}, nil
}

// WorkRoot is the only server-owned root accepted for materialized builds and
// durable campaign corpora.
func WorkRoot(storeRoot string) string { return filepath.Join(storeRoot, "worker-inputs") }

// BuildDirectory resolves an immutable build view without creating it.
func BuildDirectory(storeRoot, campaignID, buildID string) (string, error) {
	if !safeSegmentPattern.MatchString(campaignID) || !safeSegmentPattern.MatchString(buildID) {
		return "", errors.New("research pipeline: unsafe campaign or build id")
	}
	return filepath.Join(WorkRoot(storeRoot), campaignID, "builds", buildID), nil
}

// PrepareCorpus creates one durable writable corpus per campaign and harness.
func PrepareCorpus(storeRoot, campaignID, harness string) (string, error) {
	if !safeSegmentPattern.MatchString(campaignID) || !safeSegmentPattern.MatchString(harness) {
		return "", errors.New("research pipeline: unsafe campaign or harness id")
	}
	campaignRoot := filepath.Join(WorkRoot(storeRoot), campaignID)
	if err := privateDir(campaignRoot); err != nil {
		return "", err
	}
	corpusRoot := filepath.Join(campaignRoot, "corpora")
	if err := privateDir(corpusRoot); err != nil {
		return "", err
	}
	path := filepath.Join(corpusRoot, harness)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("research pipeline: corpus path is not a real directory")
	}
	// The parent is private; the bind-mounted leaf must be writable by a
	// rootless/userns-remapped container identity.
	if err := os.Chmod(path, 0o777); err != nil {
		return "", err
	}
	return path, nil
}

// EvidenceDirectory resolves a campaign-owned, read-only worker view of one
// verified CAS artifact. The operation is part of the path so an artifact
// cannot be reinterpreted under a different input contract.
func EvidenceDirectory(storeRoot, campaignID, artifactID string, operation domain.Operation) (string, error) {
	if !safeSegmentPattern.MatchString(campaignID) || !safeSegmentPattern.MatchString(artifactID) || !domain.IsKnownOperation(operation) {
		return "", errors.New("research pipeline: unsafe evidence identity")
	}
	return filepath.Join(WorkRoot(storeRoot), campaignID, "evidence", string(operation)+"-"+artifactID), nil
}

// PrepareEvidence verifies ownership and CAS integrity before atomically
// materializing the exact input shape expected by the apparatus dispatcher.
func PrepareEvidence(ctx context.Context, repository *store.Store, campaignID, artifactID string, operation domain.Operation) (string, error) {
	if repository == nil {
		return "", errors.New("research pipeline: store required")
	}
	directory, err := EvidenceDirectory(repository.Root(), campaignID, artifactID, operation)
	if err != nil {
		return "", err
	}
	artifact, source, err := repository.OpenArtifact(ctx, artifactID)
	if err != nil {
		return "", err
	}
	defer source.Close()
	if artifact.CampaignID != campaignID {
		return "", errors.New("research pipeline: evidence artifact belongs to another campaign")
	}
	leaf := "reproducer"
	allowed := artifact.Role == "crashing_input" || artifact.Role == "minimized_input"
	var transformed []byte
	if operation == domain.OperationSymbolize {
		leaf = "addresses.txt"
		allowed = artifact.Role == "stderr_log" || artifact.Role == "revision_comparison_log" || artifact.Role == "regression_log"
		if allowed {
			log, readErr := io.ReadAll(io.LimitReader(source, 4<<20+1))
			if readErr != nil {
				return "", readErr
			}
			if len(log) > 4<<20 {
				return "", errors.New("research pipeline: symbolization input exceeds limit")
			}
			matches := addressPattern.FindAll(log, -1)
			seen := map[string]bool{}
			for _, match := range matches {
				value := string(match)
				if !seen[value] {
					seen[value] = true
					transformed = append(transformed, match...)
					transformed = append(transformed, '\n')
				}
			}
			if len(transformed) == 0 {
				return "", errors.New("research pipeline: symbolization log contains no addresses")
			}
		}
	} else if operation != domain.OperationReproduce && operation != domain.OperationMinimize && operation != domain.OperationCompareBranch {
		return "", errors.New("research pipeline: operation does not accept evidence materialization")
	}
	if !allowed {
		return "", errors.New("research pipeline: artifact role is invalid for operation")
	}
	target := filepath.Join(directory, leaf)
	if _, statErr := os.Lstat(target); statErr == nil {
		if transformed != nil {
			return directory, verifyBytes(target, transformed)
		}
		return directory, verifyMaterialized(target, artifact)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parent := filepath.Dir(directory)
	if err := privateDir(filepath.Dir(parent)); err != nil {
		return "", err
	}
	if err := privateDir(parent); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(parent, ".evidence-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	output, err := os.OpenFile(filepath.Join(temporary, leaf), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", err
	}
	if transformed != nil {
		_, err = output.Write(transformed)
	} else {
		_, err = io.Copy(output, source)
	}
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(temporary, directory); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			if transformed != nil {
				return directory, verifyBytes(target, transformed)
			}
			return directory, verifyMaterialized(target, artifact)
		}
		return "", err
	}
	if transformed != nil {
		return directory, verifyBytes(target, transformed)
	}
	return directory, verifyMaterialized(target, artifact)
}

// VerifiedBuildDirectory resolves a completed campaign build and rehashes the
// materialized executable before it becomes a worker input.
func VerifiedBuildDirectory(ctx context.Context, repository *store.Store, campaignID, buildID string) (string, error) {
	if repository == nil {
		return "", errors.New("research pipeline: store required")
	}
	build, err := repository.GetBuild(ctx, buildID)
	if err != nil {
		return "", err
	}
	if build.CampaignID != campaignID || build.Status != string(domain.RunCompleted) {
		return "", errors.New("research pipeline: build is not a completed campaign build")
	}
	var binary domain.Artifact
	for _, artifactID := range build.OutputArtifacts {
		artifact, err := repository.GetArtifact(ctx, artifactID)
		if err != nil {
			return "", err
		}
		if artifact.Role == "harness_binary" {
			binary = artifact
			break
		}
	}
	if binary.ID == "" {
		return "", errors.New("research pipeline: build has no harness artifact")
	}
	path, err := BuildDirectory(repository.Root(), campaignID, buildID)
	if err != nil {
		return "", err
	}
	if err := verifyMaterialized(filepath.Join(path, "fuzz_target"), binary); err != nil {
		return "", err
	}
	return path, nil
}

// Ingest converts a completed run into build or crash evidence. It is safe to
// call again for the same run; object IDs are derived from durable run IDs.
func (c *Coordinator) Ingest(ctx context.Context, job domain.WorkerJob, result domain.RunResult) error {
	if result.RunID == "" || result.RunID != job.RunID || job.CampaignID == "" {
		return errors.New("research pipeline: mismatched result identity")
	}
	switch job.Operation {
	case domain.OperationBuild:
		return c.ingestBuild(ctx, job, result)
	case domain.OperationFuzz, domain.OperationReproduce, domain.OperationMinimize, domain.OperationRegressionTest, domain.OperationCompareBranch:
		return c.ingestObservation(ctx, job, result)
	case domain.OperationSymbolize:
		return c.ingestSymbolization(ctx, job, result)
	default:
		return nil
	}
}

func (c *Coordinator) ingestBuild(ctx context.Context, job domain.WorkerJob, result domain.RunResult) error {
	campaign, err := c.store.GetCampaign(ctx, job.CampaignID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	created := job.CreatedAt
	if created.IsZero() {
		created = now
	}
	build := domain.Build{
		SchemaVersion: 1, ID: result.RunID, CampaignID: job.CampaignID, TargetID: campaign.TargetID,
		ManifestID: job.Arguments["manifest"], ImageDigest: job.ImageDigest, Sanitizer: job.Arguments["sanitizer"],
		Architecture: "unknown", Status: string(result.Status), CreatedAt: created, CompletedAt: &now,
		Toolchain: map[string]string{}, Provenance: map[string]string{
			"harness": job.Arguments["harness"], "target_revision": job.Arguments["revision"],
			"isolation_assurance": result.IsolationAssurance,
		},
	}
	var binary domain.Artifact
	for _, artifactID := range result.ArtifactIDs {
		artifact, err := c.store.GetArtifact(ctx, artifactID)
		if err != nil {
			return err
		}
		if artifact.CampaignID != job.CampaignID || artifact.RunID != result.RunID {
			return errors.New("research pipeline: build artifact identity mismatch")
		}
		switch artifact.Role {
		case "harness_binary":
			binary = artifact
			build.OutputArtifacts = append(build.OutputArtifacts, artifact.ID)
		case "build_provenance":
			build.OutputArtifacts = append(build.OutputArtifacts, artifact.ID)
			if err := c.readBuildProvenance(ctx, artifact, &build); err != nil {
				return err
			}
		case "stdout_log", "stderr_log":
			build.LogArtifacts = append(build.LogArtifacts, artifact.ID)
		}
	}
	if result.Status == domain.RunCompleted {
		if binary.ID == "" {
			return errors.New("research pipeline: completed build has no harness binary")
		}
		if err := c.materializeBuild(ctx, job.CampaignID, result.RunID, binary); err != nil {
			return err
		}
	}
	if err := c.store.SaveBuild(ctx, build); err != nil {
		return err
	}
	if _, err = c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "build.ingest", ResourceType: "build", ResourceID: build.ID, CorrelationID: job.AuditCorrelationID, Decision: "deterministic", Details: map[string]string{"campaign_id": job.CampaignID, "status": build.Status}}); err != nil {
		return err
	}
	if result.Status == domain.RunCompleted {
		return c.advanceCampaign(ctx, job.CampaignID, domain.CampaignAcquired, domain.CampaignBuildReady,
			domain.EvidenceFacts{BuildID: build.ID, HarnessCount: 1, Instrumented: build.Sanitizer != ""}, job.AuditCorrelationID)
	}
	return nil
}

func (c *Coordinator) readBuildProvenance(ctx context.Context, artifact domain.Artifact, build *domain.Build) error {
	_, file, err := c.store.OpenArtifact(ctx, artifact.ID)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	var values map[string]any
	if err := decoder.Decode(&values); err != nil {
		return fmt.Errorf("research pipeline: decode build provenance: %w", err)
	}
	for key, value := range values {
		if text, ok := value.(string); ok {
			build.Provenance[key] = text
			if key == "compiler" {
				build.Toolchain["compiler"] = text
			}
		}
	}
	return nil
}

func (c *Coordinator) materializeBuild(ctx context.Context, campaignID, buildID string, artifact domain.Artifact) error {
	path, err := BuildDirectory(c.store.Root(), campaignID, buildID)
	if err != nil {
		return err
	}
	target := filepath.Join(path, "fuzz_target")
	if _, statErr := os.Lstat(target); statErr == nil {
		return verifyMaterialized(target, artifact)
	}
	if err := privateDir(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	_, source, err := c.store.OpenArtifact(ctx, artifact.ID)
	if err != nil {
		return err
	}
	defer source.Close()
	tmp, err := os.CreateTemp(path, ".fuzz-target-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, source); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o555); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	// The bind mount is read-only inside workers. Keep owner write permission on
	// the private host directory so retention and test cleanup can remove it.
	return os.Chmod(path, 0o700)
}

func verifyMaterialized(path string, artifact domain.Artifact) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != artifact.Size {
		return errors.New("research pipeline: materialized build metadata mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(digest.Sum(nil)) != artifact.ContentID {
		return errors.New("research pipeline: materialized build content hash mismatch")
	}
	return nil
}

func verifyBytes(path string, expected []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != int64(len(expected)) {
		return errors.New("research pipeline: materialized evidence metadata mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	expectedDigest := sha256.Sum256(expected)
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), hex.EncodeToString(expectedDigest[:])) {
		return errors.New("research pipeline: materialized evidence content hash mismatch")
	}
	return nil
}

func (c *Coordinator) ingestObservation(ctx context.Context, job domain.WorkerJob, result domain.RunResult) error {
	if job.BuildID == "" {
		return nil
	}
	if job.Operation == domain.OperationFuzz {
		if err := c.advanceCampaign(ctx, job.CampaignID, domain.CampaignBuildReady, domain.CampaignFuzzing,
			domain.EvidenceFacts{FuzzRunID: result.RunID}, job.AuditCorrelationID); err != nil {
			return err
		}
	}
	var stderr, input domain.Artifact
	for _, artifactID := range result.ArtifactIDs {
		artifact, err := c.store.GetArtifact(ctx, artifactID)
		if err != nil {
			return err
		}
		if artifact.CampaignID != job.CampaignID || artifact.RunID != result.RunID {
			return errors.New("research pipeline: observation artifact identity mismatch")
		}
		switch artifact.Role {
		case "stderr_log", "revision_comparison_log", "regression_log":
			if stderr.ID == "" {
				stderr = artifact
			}
		case "crashing_input":
			if input.ID == "" {
				input = artifact
			}
		case "minimized_input":
			if input.ID == "" {
				input = artifact
			}
		}
	}
	if input.ID == "" && job.InputArtifactID != "" {
		candidate, getErr := c.store.GetArtifact(ctx, job.InputArtifactID)
		if getErr != nil {
			return getErr
		}
		input = candidate
		if input.CampaignID != job.CampaignID {
			return errors.New("research pipeline: input artifact belongs to another campaign")
		}
	}
	if stderr.ID == "" {
		return nil
	}
	_, file, err := c.store.OpenArtifact(ctx, stderr.ID)
	if err != nil {
		return err
	}
	log, err := io.ReadAll(io.LimitReader(file, triage.MaxLogBytes+1))
	file.Close()
	if err != nil {
		return err
	}
	observation, err := triage.Parse(log, triage.ParseOptions{
		ID: "observation_" + result.RunID, CampaignID: job.CampaignID, RunID: result.RunID,
		BuildID: job.BuildID, InputArtifactID: input.ID, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	if observation.Class == domain.ObservationUnclassified {
		return nil
	}
	if err := c.store.SaveCrash(ctx, observation); err != nil {
		return err
	}
	var crashGroup domain.CrashGroup
	if observation.SecurityRelevant {
		groups, err := c.store.ListCrashGroups(ctx, job.CampaignID, 10000)
		if err != nil {
			return err
		}
		var group domain.CrashGroup
		for _, candidate := range groups {
			if candidate.Signature == observation.Signature {
				group = candidate
				break
			}
		}
		group, err = triage.AddToGroup(group, observation, groupID(job.CampaignID, observation.Signature), time.Now().UTC())
		if err != nil {
			return err
		}
		if err := c.store.SaveCrashGroup(ctx, group); err != nil {
			return err
		}
		crashGroup = group
	}
	if _, err = c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "observation.ingest", ResourceType: "crash_observation", ResourceID: observation.ID, CorrelationID: job.AuditCorrelationID, Decision: "machine_parsed", Details: map[string]string{"campaign_id": job.CampaignID, "class": string(observation.Class), "signature": observation.Signature}}); err != nil {
		return err
	}
	if job.Operation == domain.OperationFuzz && observation.SecurityRelevant && observation.InputArtifactID != "" {
		return c.advanceCampaign(ctx, job.CampaignID, domain.CampaignFuzzing, domain.CampaignCrashObserved,
			domain.EvidenceFacts{CrashInputArtifactID: observation.InputArtifactID, CrashSignature: observation.Signature, CrashMachineParsed: true}, job.AuditCorrelationID)
	}
	if job.Operation == domain.OperationReproduce && observation.SecurityRelevant {
		return c.advanceReproduction(ctx, job, crashGroup)
	}
	if job.Operation == domain.OperationMinimize && observation.SecurityRelevant {
		return c.advanceMinimization(ctx, job, observation, crashGroup)
	}
	return nil
}

func (c *Coordinator) advanceReproduction(ctx context.Context, job domain.WorkerJob, group domain.CrashGroup) error {
	if group.ID == "" || job.InputArtifactID == "" {
		return nil
	}
	attempts := make([]domain.CrashObservation, 0, len(group.ObservationIDs))
	for _, observationID := range group.ObservationIDs {
		observation, err := c.store.GetCrash(ctx, observationID)
		if err != nil {
			return err
		}
		run, err := c.store.GetRun(ctx, observation.RunID)
		if err != nil {
			return err
		}
		if run.Operation == domain.OperationReproduce && observation.InputArtifactID == job.InputArtifactID {
			attempts = append(attempts, observation)
		}
	}
	facts := triage.ReproductionFacts(attempts, c.minimumReproductions)
	if !facts.CrashMachineParsed {
		return nil
	}
	return c.advanceCampaign(ctx, job.CampaignID, domain.CampaignCrashObserved, domain.CampaignReproduced, facts, job.AuditCorrelationID)
}

func (c *Coordinator) advanceMinimization(ctx context.Context, job domain.WorkerJob, minimizedObservation domain.CrashObservation, group domain.CrashGroup) error {
	if group.ID == "" || job.InputArtifactID == "" || minimizedObservation.InputArtifactID == "" || minimizedObservation.InputArtifactID == job.InputArtifactID {
		return nil
	}
	original, err := c.store.GetArtifact(ctx, job.InputArtifactID)
	if err != nil {
		return err
	}
	minimized, err := c.store.GetArtifact(ctx, minimizedObservation.InputArtifactID)
	if err != nil {
		return err
	}
	var originalObservation domain.CrashObservation
	for _, observationID := range group.ObservationIDs {
		candidate, getErr := c.store.GetCrash(ctx, observationID)
		if getErr != nil {
			return getErr
		}
		if candidate.InputArtifactID == original.ID && candidate.Signature == minimizedObservation.Signature {
			originalObservation = candidate
			break
		}
	}
	facts := triage.MinimizationFacts(original, minimized, originalObservation, minimizedObservation)
	if !facts.MinimizedSameSignature {
		return nil
	}
	group.MinimizedArtifactID = minimized.ID
	group.UpdatedAt = time.Now().UTC()
	if err := c.store.SaveCrashGroup(ctx, group); err != nil {
		return err
	}
	return c.advanceCampaign(ctx, job.CampaignID, domain.CampaignReproduced, domain.CampaignMinimized, facts, job.AuditCorrelationID)
}

func (c *Coordinator) ingestSymbolization(ctx context.Context, job domain.WorkerJob, result domain.RunResult) error {
	if job.InputArtifactID == "" {
		return errors.New("research pipeline: symbolization requires an input log")
	}
	input, err := c.store.GetArtifact(ctx, job.InputArtifactID)
	if err != nil {
		return err
	}
	if input.CampaignID != job.CampaignID {
		return errors.New("research pipeline: symbolization input belongs to another campaign")
	}
	var symbolized domain.Artifact
	for _, artifactID := range result.ArtifactIDs {
		artifact, getErr := c.store.GetArtifact(ctx, artifactID)
		if getErr != nil {
			return getErr
		}
		if artifact.CampaignID != job.CampaignID || artifact.RunID != result.RunID {
			return errors.New("research pipeline: symbolization artifact identity mismatch")
		}
		if artifact.Role == "symbolized_stack" {
			symbolized = artifact
		}
	}
	if result.Status != domain.RunCompleted || symbolized.ID == "" {
		return nil
	}
	_, output, err := c.store.OpenArtifact(ctx, symbolized.ID)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(output, 4<<20+1))
	output.Close()
	if readErr != nil {
		return readErr
	}
	if len(data) > 4<<20 {
		return errors.New("research pipeline: symbolized stack exceeds limit")
	}
	frameCount := len(sourceLinePattern.FindAll(data, -1))
	if frameCount == 0 {
		return errors.New("research pipeline: symbolized stack has no source locations")
	}
	observations, err := c.store.ListCrashes(ctx, job.CampaignID, 10000)
	if err != nil {
		return err
	}
	var observation domain.CrashObservation
	for _, candidate := range observations {
		if candidate.RunID == input.RunID && candidate.SecurityRelevant && candidate.Signature != "" {
			observation = candidate
			break
		}
	}
	if observation.ID == "" {
		return errors.New("research pipeline: symbolization input is not linked to a security observation")
	}
	group, err := c.store.GetCrashGroup(ctx, groupID(job.CampaignID, observation.Signature))
	if err != nil {
		return err
	}
	group.RootCause = observation.Summary
	group.UpdatedAt = time.Now().UTC()
	if err := c.store.SaveCrashGroup(ctx, group); err != nil {
		return err
	}
	if _, err := c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "root_cause.ingest", ResourceType: "crash_group", ResourceID: group.ID, CorrelationID: job.AuditCorrelationID, Decision: "machine_parsed", Details: map[string]string{"campaign_id": job.CampaignID, "symbolized_artifact_id": symbolized.ID}}); err != nil {
		return err
	}
	if err := c.advanceCampaign(ctx, job.CampaignID, domain.CampaignMinimized, domain.CampaignRootCaused,
		domain.EvidenceFacts{SymbolizedFrameCount: frameCount, RootCause: group.RootCause, CrashGroupID: group.ID}, job.AuditCorrelationID); err != nil {
		return err
	}
	primitive := primitiveFromObservation(job.CampaignID, group, observation, symbolized.ID, time.Now().UTC())
	if err := domain.ValidatePrimitive(primitive); err != nil {
		return err
	}
	if err := c.store.SavePrimitive(ctx, primitive); err != nil {
		return err
	}
	if _, err := c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "primitive.assess", ResourceType: "primitive_assessment", ResourceID: primitive.ID, CorrelationID: job.AuditCorrelationID, Decision: "evidence_bounded", Details: map[string]string{"campaign_id": job.CampaignID, "crash_group_id": group.ID}}); err != nil {
		return err
	}
	if err := c.savePrimitiveFinding(ctx, group, observation, primitive, symbolized.ID, job.AuditCorrelationID); err != nil {
		return err
	}
	return c.advanceCampaign(ctx, job.CampaignID, domain.CampaignRootCaused, domain.CampaignPrimitiveAssessed,
		domain.EvidenceFacts{PrimitiveAssessmentID: primitive.ID, PrimitiveEvidenceValid: true}, job.AuditCorrelationID)
}

func primitiveFromObservation(campaignID string, group domain.CrashGroup, observation domain.CrashObservation, symbolizedArtifactID string, now time.Time) domain.PrimitiveAssessment {
	operation := domain.PrimitiveCrash
	bug := strings.ToLower(observation.BugType)
	access := strings.ToLower(observation.Access)
	switch {
	case strings.Contains(bug, "use-after-free"):
		operation = domain.PrimitiveUseAfterFree
	case strings.Contains(bug, "double-free") || strings.Contains(bug, "invalid-free"):
		operation = domain.PrimitiveInvalidFree
	case strings.Contains(bug, "buffer-overflow") && strings.Contains(access, "write"):
		operation = domain.PrimitiveOOBWrite
	case strings.Contains(bug, "buffer-overflow") && strings.Contains(access, "read"):
		operation = domain.PrimitiveOOBRead
	}
	primitive := domain.PrimitiveAssessment{SchemaVersion: 1, ID: "primitive_" + strings.TrimPrefix(group.ID, "group_"), CampaignID: campaignID, CrashGroupID: group.ID, Operation: operation, OperationEvidence: []string{observation.ID, symbolizedArtifactID}, CreatedAt: now}
	if observation.AccessSize > 0 {
		primitive.AccessWidth = domain.EvidenceValue{Known: true, Value: fmt.Sprintf("%d byte(s)", observation.AccessSize), EvidenceIDs: []string{observation.ID, symbolizedArtifactID}}
	}
	if len(group.ObservationIDs) > 1 {
		primitive.Repeatability = domain.EvidenceValue{Known: true, Value: fmt.Sprintf("%d same-signature observations", len(group.ObservationIDs)), EvidenceIDs: append([]string(nil), group.ObservationIDs...)}
	}
	return primitive
}

func (c *Coordinator) savePrimitiveFinding(ctx context.Context, group domain.CrashGroup, observation domain.CrashObservation, primitive domain.PrimitiveAssessment, symbolizedArtifactID, correlationID string) error {
	now := time.Now().UTC()
	findingID := "finding_" + strings.TrimPrefix(group.ID, "group_")
	finding, err := c.store.GetFinding(ctx, findingID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		finding = domain.Finding{
			SchemaVersion: 1, ID: findingID, CampaignID: group.CampaignID, CrashGroupID: group.ID, PrimitiveID: primitive.ID,
			Title: observation.Summary, Label: domain.FindingHypothesis, RootCause: group.RootCause,
			EvidenceIDs:      append(append([]string(nil), group.ObservationIDs...), group.MinimizedArtifactID, symbolizedArtifactID, primitive.ID),
			DisclosureStatus: "not_disclosed", CreatedAt: now, UpdatedAt: now,
		}
		finding.CWE = cweForPrimitive(primitive.Operation)
	}
	steps := []struct {
		label domain.FindingLabel
		facts domain.EvidenceFacts
	}{
		{domain.FindingObservation, domain.EvidenceFacts{}},
		{domain.FindingCrashObserved, domain.EvidenceFacts{CrashInputArtifactID: observation.InputArtifactID, CrashMachineParsed: true}},
		{domain.FindingReproducedMemoryIssue, domain.EvidenceFacts{ReproductionCount: c.minimumReproductions}},
		{domain.FindingPrimitiveCandidate, domain.EvidenceFacts{PrimitiveAssessmentID: primitive.ID}},
		{domain.FindingPrimitiveConfirmed, domain.EvidenceFacts{PrimitiveAssessmentID: primitive.ID, PrimitiveEvidenceValid: true}},
	}
	for _, step := range steps {
		if finding.Label == step.label {
			continue
		}
		promoted, promoteErr := c.machine.PromoteFinding(finding, step.label, step.facts, now)
		if promoteErr != nil {
			return promoteErr
		}
		finding = promoted
	}
	finding.UpdatedAt = now
	if err := c.store.SaveFinding(ctx, finding); err != nil {
		return err
	}
	_, err = c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "finding.promote", ResourceType: "finding", ResourceID: finding.ID, CorrelationID: correlationID, Decision: "evidence_bounded", Details: map[string]string{"campaign_id": finding.CampaignID, "label": string(finding.Label)}})
	return err
}

func cweForPrimitive(operation domain.PrimitiveOperation) string {
	switch operation {
	case domain.PrimitiveOOBWrite:
		return "CWE-787"
	case domain.PrimitiveOOBRead:
		return "CWE-125"
	case domain.PrimitiveUseAfterFree:
		return "CWE-416"
	case domain.PrimitiveInvalidFree:
		return "CWE-415"
	default:
		return ""
	}
}

func (c *Coordinator) advanceCampaign(ctx context.Context, campaignID string, from, to domain.CampaignState, facts domain.EvidenceFacts, correlationID string) error {
	campaign, err := c.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return err
	}
	if campaign.State != from {
		return nil
	}
	updated, err := c.machine.Advance(campaign, to, facts, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := c.store.UpdateCampaign(ctx, updated, campaign.Version); err != nil {
		if !errors.Is(err, store.ErrVersionConflict) {
			return err
		}
		current, getErr := c.store.GetCampaign(ctx, campaignID)
		if getErr != nil {
			return getErr
		}
		if current.State != to {
			return err
		}
		return nil
	}
	_, err = c.store.AppendAudit(ctx, domain.AuditEvent{ActorID: "evidence-pipeline", Action: "campaign.transition", ResourceType: "campaign", ResourceID: campaignID, CorrelationID: correlationID, Decision: "deterministic", Details: map[string]string{"from": string(from), "to": string(to)}})
	return err
}

func groupID(campaignID, signature string) string {
	digest := sha256.Sum256([]byte(campaignID + "\x00" + signature))
	return "group_" + hex.EncodeToString(digest[:16])
}

func privateDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("research pipeline: private path required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
