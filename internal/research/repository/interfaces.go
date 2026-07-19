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
	ListWorkerJobsByStatus(context.Context, ...domain.RunStatus) ([]domain.WorkerJob, error)
	SaveRun(context.Context, domain.ExperimentRun) error
	GetRun(context.Context, string) (domain.ExperimentRun, error)
}

// ControlPlane is the minimal deterministic service persistence boundary.
type ControlPlane interface {
	Scopes
	Campaigns
	Evidence
	Audit
}
