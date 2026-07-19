// Package repository defines persistence ports so the control plane is not
// coupled to SQLite or a particular object-store implementation.
package repository

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

type Scopes interface {
	CreateScope(context.Context, domain.AuthorizationScope) error
	GetScope(context.Context, string) (domain.AuthorizationScope, error)
	RevokeScope(context.Context, string, time.Time) error
}

type Campaigns interface {
	CreateCampaign(context.Context, domain.Campaign) (domain.Campaign, error)
	GetCampaign(context.Context, string) (domain.Campaign, error)
	UpdateCampaign(context.Context, domain.Campaign, int64) error
}

type Evidence interface {
	SaveTarget(context.Context, domain.TargetRevision) error
	GetTarget(context.Context, string) (domain.TargetRevision, error)
	SaveBuild(context.Context, domain.Build) error
	GetBuild(context.Context, string) (domain.Build, error)
	SaveApparatus(context.Context, domain.ApparatusManifest) error
	GetApparatus(context.Context, string) (domain.ApparatusManifest, error)
	SaveFinding(context.Context, domain.Finding) error
	GetFinding(context.Context, string) (domain.Finding, error)
	SaveApproval(context.Context, domain.Approval) error
	GetApproval(context.Context, string) (domain.Approval, error)
}

type Audit interface {
	AppendAudit(context.Context, domain.AuditEvent) (domain.AuditEvent, error)
	ListAudit(context.Context, int64, int) ([]domain.AuditEvent, error)
	VerifyAuditChain(context.Context) error
}

type Artifacts interface {
	PutArtifact(context.Context, domain.Artifact, io.Reader) (domain.Artifact, error)
	GetArtifact(context.Context, string) (domain.Artifact, error)
	OpenArtifact(context.Context, string) (domain.Artifact, *os.File, error)
}

type Jobs interface {
	CreateJobAndRun(context.Context, domain.WorkerJob, domain.ExperimentRun) error
	SaveJobAndRun(context.Context, domain.WorkerJob, domain.ExperimentRun) error
	SaveWorkerJob(context.Context, domain.WorkerJob) error
	GetWorkerJob(context.Context, string) (domain.WorkerJob, error)
	GetWorkerJobByRunID(context.Context, string) (domain.WorkerJob, error)
	ListWorkerJobsByStatus(context.Context, ...domain.RunStatus) ([]domain.WorkerJob, error)
	SaveRun(context.Context, domain.ExperimentRun) error
	GetRun(context.Context, string) (domain.ExperimentRun, error)
}

// WorkflowEvidence is the durable boundary for the post-triage research
// workflow. It is intentionally separate from ControlPlane so lightweight
// policy/service adapters need not implement query methods they never use.
type WorkflowEvidence interface {
	SaveSourceEvidence(context.Context, domain.SourceEvidence) error
	GetSourceEvidence(context.Context, string) (domain.SourceEvidence, error)
	ListSourceEvidence(context.Context, string, int) ([]domain.SourceEvidence, error)
	SaveSourceReview(context.Context, domain.SourceReview) error
	GetSourceReview(context.Context, string) (domain.SourceReview, error)
	ListSourceReviews(context.Context, string, int) ([]domain.SourceReview, error)
	SaveRevisionCheck(context.Context, domain.RevisionCheck) error
	GetRevisionCheck(context.Context, string) (domain.RevisionCheck, error)
	ListRevisionChecks(context.Context, string, int) ([]domain.RevisionCheck, error)
	SaveRemediation(context.Context, domain.RemediationValidation) error
	GetRemediation(context.Context, string) (domain.RemediationValidation, error)
	ListRemediations(context.Context, string, int) ([]domain.RemediationValidation, error)
	GetRun(context.Context, string) (domain.ExperimentRun, error)
	ListCrashes(context.Context, string, int) ([]domain.CrashObservation, error)
	GetCrashGroup(context.Context, string) (domain.CrashGroup, error)
	GetPrimitive(context.Context, string) (domain.PrimitiveAssessment, error)
	ListArtifacts(context.Context, string, int) ([]domain.Artifact, error)
	ListTargets(context.Context, string, int) ([]domain.TargetRevision, error)
}

// ControlPlane is the minimal deterministic service persistence boundary.
type ControlPlane interface {
	Scopes
	Campaigns
	Evidence
	Artifacts
	Audit
}
