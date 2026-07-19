/*
Package domain defines the durable, provider-independent research model. The
types in this package are evidence records, not chat messages: model output may
propose them, but deterministic services validate and persist every state change.
*/
package domain

import (
	"encoding/json"
	"time"
)

/* Operation is a closed research action vocabulary used by scope policy. */
type Operation string

const (
	OperationInspect        Operation = "inspect"
	OperationAcquire        Operation = "acquire"
	OperationBuild          Operation = "build"
	OperationListHarnesses  Operation = "list_harnesses"
	OperationSmokeTest      Operation = "smoke_test"
	OperationSeed           Operation = "seed"
	OperationFuzz           Operation = "fuzz"
	OperationReproduce      Operation = "reproduce"
	OperationMinimize       Operation = "minimize"
	OperationMergeCorpus    Operation = "merge_corpus"
	OperationCoverage       Operation = "coverage"
	OperationSymbolize      Operation = "symbolize"
	OperationCompareBranch  Operation = "compare_revision"
	OperationNoveltyLookup  Operation = "novelty_lookup"
	OperationRegressionTest Operation = "regression_test"
	OperationStaticAnalyze  Operation = "static_analyze"
	OperationDraftReport    Operation = "draft_report"
	OperationDisclose       Operation = "disclose"
	OperationPurgeArtifact  Operation = "purge_artifact"
)

// IsKnownOperation keeps policy, manifests, and runner dispatch on the same
// closed vocabulary. Unknown JSON strings never become operator-authorized
// worker actions.
func IsKnownOperation(operation Operation) bool {
	switch operation {
	case OperationInspect, OperationAcquire, OperationBuild, OperationListHarnesses,
		OperationSmokeTest, OperationSeed, OperationFuzz, OperationReproduce,
		OperationMinimize, OperationMergeCorpus, OperationCoverage, OperationSymbolize,
		OperationCompareBranch, OperationNoveltyLookup, OperationRegressionTest, OperationStaticAnalyze,
		OperationDraftReport, OperationDisclose, OperationPurgeArtifact:
		return true
	default:
		return false
	}
}

/* Role is an authenticated research-control-plane role. */
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleAnalyst  Role = "analyst"
	RoleOperator Role = "operator"
	RoleReviewer Role = "reviewer"
	RoleAdmin    Role = "admin"
)

/* Principal is the authenticated actor attached to every mutation and audit event. */
type Principal struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Roles []Role `json:"roles"`
}

/* ResourceBudget is the maximum resource envelope authorized for a scope. */
type ResourceBudget struct {
	MaxWallSeconds int64 `json:"max_wall_seconds"`
	MaxMemoryBytes int64 `json:"max_memory_bytes"`
	MaxCPUSeconds  int64 `json:"max_cpu_seconds"`
	MaxDiskBytes   int64 `json:"max_disk_bytes"`
	MaxInodes      int64 `json:"max_inodes"`
	MaxPIDs        int64 `json:"max_pids"`
	MaxConcurrent  int   `json:"max_concurrent"`
}

/*
AuthorizationScope is the machine-enforced permission envelope for a campaign.
Repository/revision and workspace roots are constraints, not descriptive notes.
*/
type AuthorizationScope struct {
	SchemaVersion       int            `json:"schema_version"`
	ID                  string         `json:"id"`
	OperatorID          string         `json:"operator_id"`
	MemberIDs           []string       `json:"member_ids,omitempty"`
	Purpose             string         `json:"purpose"`
	TargetRepository    string         `json:"target_repository"`
	AllowedRevisions    []string       `json:"allowed_revisions"`
	WorkspaceRoots      []string       `json:"workspace_roots"`
	AllowedOperations   []Operation    `json:"allowed_operations"`
	AllowedApparatusIDs []string       `json:"allowed_apparatus_ids,omitempty"`
	ApprovalOperations  []Operation    `json:"approval_operations"`
	AllowedDomains      []string       `json:"allowed_domains"`
	Budget              ResourceBudget `json:"budget"`
	DisclosureContact   string         `json:"disclosure_contact"`
	CreatedAt           time.Time      `json:"created_at"`
	ExpiresAt           time.Time      `json:"expires_at"`
	RevokedAt           *time.Time     `json:"revoked_at,omitempty"`
}

/* CampaignState is advanced only by StateMachine. */
type CampaignState string

const (
	CampaignDraft              CampaignState = "draft"
	CampaignScoped             CampaignState = "scoped"
	CampaignAcquired           CampaignState = "acquired"
	CampaignBuildReady         CampaignState = "build_ready"
	CampaignFuzzing            CampaignState = "fuzzing"
	CampaignCrashObserved      CampaignState = "crash_observed"
	CampaignReproduced         CampaignState = "reproduced"
	CampaignMinimized          CampaignState = "minimized"
	CampaignRootCaused         CampaignState = "root_caused"
	CampaignPrimitiveAssessed  CampaignState = "primitive_assessed"
	CampaignBranchChecked      CampaignState = "branch_checked"
	CampaignNoveltyReviewed    CampaignState = "novelty_reviewed"
	CampaignRemediated         CampaignState = "remediated"
	CampaignReportReady        CampaignState = "report_ready"
	CampaignDisclosed          CampaignState = "disclosed"
	CampaignCompletedNoFinding CampaignState = "completed_no_finding"
	CampaignPaused             CampaignState = "paused"
	CampaignCancelled          CampaignState = "cancelled"
	CampaignFailed             CampaignState = "failed"
)

/* Campaign is the aggregate root for one authorized research effort. */
type Campaign struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	ScopeID       string            `json:"scope_id"`
	Name          string            `json:"name"`
	Goal          string            `json:"goal"`
	State         CampaignState     `json:"state"`
	TargetID      string            `json:"target_id,omitempty"`
	Plan          []PlannedStep     `json:"plan,omitempty"`
	Budget        ResourceBudget    `json:"budget"`
	Labels        map[string]string `json:"labels,omitempty"`
	Version       int64             `json:"version"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

/* PlannedStep records an analyst/model proposal without treating it as evidence. */
type PlannedStep struct {
	ID         string    `json:"id"`
	Operation  Operation `json:"operation"`
	Rationale  string    `json:"rationale"`
	Status     string    `json:"status"`
	EvidenceID []string  `json:"evidence_ids,omitempty"`
}

/* TargetRevision is an immutable acquired source identity. */
type TargetRevision struct {
	SchemaVersion int                   `json:"schema_version"`
	ID            string                `json:"id"`
	CampaignID    string                `json:"campaign_id"`
	Repository    string                `json:"repository"`
	RequestedRef  string                `json:"requested_ref"`
	Commit        string                `json:"commit"`
	SourceSHA256  string                `json:"source_sha256"`
	Language      string                `json:"language"`
	Architecture  string                `json:"architecture"`
	Acquisition   AcquisitionProvenance `json:"acquisition"`
	AcquiredAt    time.Time             `json:"acquired_at"`
}

// AcquisitionProvenance records how target bytes crossed the acquisition
// boundary. Bundle URLs are fixed operator configuration and never contain
// credentials or caller-controlled query values.
type AcquisitionProvenance struct {
	Method            string    `json:"method"`
	SourceName        string    `json:"source_name,omitempty"`
	SourceURL         string    `json:"source_url,omitempty"`
	BundleSHA256      string    `json:"bundle_sha256,omitempty"`
	BundleBytes       int64     `json:"bundle_bytes,omitempty"`
	ManifestKeyID     string    `json:"manifest_key_id,omitempty"`
	ManifestExpiresAt time.Time `json:"manifest_expires_at,omitempty"`
	FetchedAt         time.Time `json:"fetched_at,omitempty"`
}

/* ApparatusManifest describes a deterministic, versioned research adapter. */
type ApparatusManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Version       string                `json:"version"`
	ImageDigest   string                `json:"image_digest"`
	Engine        string                `json:"engine"`
	Sanitizers    []string              `json:"sanitizers"`
	Architectures []string              `json:"architectures"`
	Harnesses     []HarnessManifest     `json:"harnesses"`
	Operations    []Operation           `json:"operations"`
	Environment   map[string]string     `json:"environment,omitempty"`
	Limits        ResourceBudget        `json:"limits"`
	SupplyChain   *ApparatusSupplyChain `json:"supply_chain,omitempty"`
}

/* ApparatusSupplyChain is populated only by verified signed admission. */
type ApparatusSupplyChain struct {
	SchemaVersion      int             `json:"schema_version"`
	SBOMSHA256         string          `json:"sbom_sha256"`
	ProvenanceSHA256   string          `json:"provenance_sha256"`
	AdmissionKeyID     string          `json:"admission_key_id"`
	AdmissionExpiresAt time.Time       `json:"admission_expires_at"`
	BuilderID          string          `json:"builder_id"`
	DependencyCount    int             `json:"dependency_count"`
	SBOM               json.RawMessage `json:"sbom,omitempty"`
	Provenance         json.RawMessage `json:"provenance,omitempty"`
}

/* HarnessManifest is one named, typed fuzz entrypoint. */
type HarnessManifest struct {
	Name        string   `json:"name"`
	Binary      string   `json:"binary"`
	SeedCorpus  string   `json:"seed_corpus,omitempty"`
	Dictionary  string   `json:"dictionary,omitempty"`
	Options     []string `json:"options,omitempty"`
	MaxInputLen int64    `json:"max_input_len,omitempty"`
}

/* Build is the exact, provenance-carrying result of an isolated target build. */
type Build struct {
	SchemaVersion   int               `json:"schema_version"`
	ID              string            `json:"id"`
	CampaignID      string            `json:"campaign_id"`
	TargetID        string            `json:"target_id"`
	ManifestID      string            `json:"manifest_id"`
	ImageDigest     string            `json:"image_digest"`
	Toolchain       map[string]string `json:"toolchain"`
	Sanitizer       string            `json:"sanitizer"`
	Architecture    string            `json:"architecture"`
	OutputArtifacts []string          `json:"output_artifacts"`
	LogArtifacts    []string          `json:"log_artifacts"`
	Provenance      map[string]string `json:"provenance"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
}

/* RunStatus is a worker job lifecycle status. */
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunTimedOut  RunStatus = "timed_out"
)

/* ResourceUsage is measured worker consumption, never a model estimate. */
type ResourceUsage struct {
	WallMillis       int64 `json:"wall_ms"`
	CPUMillis        int64 `json:"cpu_ms"`
	MaxRSSBytes      int64 `json:"max_rss_bytes"`
	DiskWrittenBytes int64 `json:"disk_written_bytes"`
	InodesCreated    int64 `json:"inodes_created"`
}

/* RunExit captures deterministic termination semantics. */
type RunExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Reason string `json:"reason,omitempty"`
}

/* ExperimentRun is a durable structured worker invocation. */
type ExperimentRun struct {
	SchemaVersion      int               `json:"schema_version"`
	ID                 string            `json:"id"`
	CampaignID         string            `json:"campaign_id"`
	ScopeID            string            `json:"scope_id"`
	TargetID           string            `json:"target_id,omitempty"`
	BuildID            string            `json:"build_id,omitempty"`
	InputArtifactID    string            `json:"input_artifact_id,omitempty"`
	PatchArtifactID    string            `json:"patch_artifact_id,omitempty"`
	Operation          Operation         `json:"operation"`
	Arguments          map[string]string `json:"arguments,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	Status             RunStatus         `json:"status"`
	WorkerID           string            `json:"worker_id,omitempty"`
	IsolationAssurance string            `json:"isolation_assurance,omitempty"`
	Exit               RunExit           `json:"exit"`
	Usage              ResourceUsage     `json:"resource_usage"`
	ArtifactIDs        []string          `json:"artifact_ids,omitempty"`
	AuditCorrelationID string            `json:"audit_correlation_id"`
	CreatedAt          time.Time         `json:"created_at"`
	StartedAt          *time.Time        `json:"started_at,omitempty"`
	CompletedAt        *time.Time        `json:"completed_at,omitempty"`
}

/* JobMount is a broker-validated worker filesystem input/output boundary. */
type JobMount struct {
	Name          string `json:"name"`
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only"`
}

/* ArtifactRule is an allowlisted worker output role and ingestion ceiling. */
type ArtifactRule struct {
	Role      string `json:"role"`
	MediaType string `json:"media_type"`
	Glob      string `json:"glob"`
	MaxCount  int    `json:"max_count"`
	MaxBytes  int64  `json:"max_bytes"`
}

/* WorkerJob is a durable, provider-independent runner invocation envelope. */
type WorkerJob struct {
	SchemaVersion      int               `json:"schema_version"`
	ID                 string            `json:"id"`
	RunID              string            `json:"run_id"`
	CampaignID         string            `json:"campaign_id"`
	ScopeID            string            `json:"scope_id"`
	TargetID           string            `json:"target_id,omitempty"`
	BuildID            string            `json:"build_id,omitempty"`
	InputArtifactID    string            `json:"input_artifact_id,omitempty"`
	PatchArtifactID    string            `json:"patch_artifact_id,omitempty"`
	Operation          Operation         `json:"operation"`
	Arguments          map[string]string `json:"arguments,omitempty"`
	Environment        map[string]string `json:"environment,omitempty"`
	ImageDigest        string            `json:"image_digest"`
	Runtime            string            `json:"runtime"`
	Mounts             []JobMount        `json:"mounts"`
	ArtifactRules      []ArtifactRule    `json:"artifact_rules,omitempty"`
	Budget             ResourceBudget    `json:"budget"`
	AuditCorrelationID string            `json:"audit_correlation_id"`
	Status             RunStatus         `json:"status"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

/* CapturedOutput references bounded stdout/stderr retained as artifacts. */
type CapturedOutput struct {
	StdoutArtifactID string `json:"stdout_artifact_id,omitempty"`
	StderrArtifactID string `json:"stderr_artifact_id,omitempty"`
	StdoutTruncated  bool   `json:"stdout_truncated"`
	StderrTruncated  bool   `json:"stderr_truncated"`
	BytesDropped     int64  `json:"bytes_dropped"`
}

/* ApparatusIdentity is exact execution provenance attached to a result. */
type ApparatusIdentity struct {
	ManifestID     string `json:"manifest_id"`
	ImageDigest    string `json:"image_digest"`
	TargetRevision string `json:"target_revision"`
	Harness        string `json:"harness"`
	Sanitizer      string `json:"sanitizer"`
	Runtime        string `json:"runtime"`
}

/* RunResult is the versioned internal contract rendered only at API/model edges. */
type RunResult struct {
	SchemaVersion      int               `json:"schema_version"`
	RunID              string            `json:"run_id"`
	Operation          Operation         `json:"operation"`
	Status             RunStatus         `json:"status"`
	Exit               RunExit           `json:"exit"`
	Output             CapturedOutput    `json:"output"`
	ArtifactIDs        []string          `json:"artifact_ids,omitempty"`
	ResourceUsage      ResourceUsage     `json:"resource_usage"`
	Apparatus          ApparatusIdentity `json:"apparatus"`
	IsolationAssurance string            `json:"isolation_assurance"`
	AuditCorrelationID string            `json:"audit_correlation_id"`
}

/* Artifact has immutable content identity and monotonic custody lifecycle metadata. */
type Artifact struct {
	SchemaVersion   int        `json:"schema_version"`
	ID              string     `json:"id"`
	ContentID       string     `json:"content_id"`
	CampaignID      string     `json:"campaign_id"`
	RunID           string     `json:"run_id,omitempty"`
	ParentIDs       []string   `json:"parent_ids,omitempty"`
	Role            string     `json:"role"`
	MediaType       string     `json:"media_type"`
	Size            int64      `json:"size"`
	Sensitivity     string     `json:"sensitivity"`
	StoragePath     string     `json:"storage_path"`
	Encryption      string     `json:"encryption,omitempty"`
	EncryptionKeyID string     `json:"encryption_key_id,omitempty"`
	RetainUntil     time.Time  `json:"retain_until"`
	PurgedAt        *time.Time `json:"purged_at,omitempty"`
	PurgeApprovalID string     `json:"purge_approval_id,omitempty"`
	PurgeReason     string     `json:"purge_reason,omitempty"`
	BlobDeletedAt   *time.Time `json:"blob_deleted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

/* ObservationClass separates security signals from resource/test observations. */
type ObservationClass string

const (
	ObservationASanMemory   ObservationClass = "asan_memory"
	ObservationMSanMemory   ObservationClass = "msan_memory"
	ObservationUBSan        ObservationClass = "ubsan"
	ObservationSignal       ObservationClass = "signal"
	ObservationAssertion    ObservationClass = "assertion"
	ObservationTimeout      ObservationClass = "timeout"
	ObservationOOM          ObservationClass = "oom"
	ObservationLeak         ObservationClass = "leak"
	ObservationUnclassified ObservationClass = "unclassified"
)

/* StackFrame is a normalized, symbolized frame. */
type StackFrame struct {
	Index    int    `json:"index"`
	Function string `json:"function"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Module   string `json:"module,omitempty"`
	Address  string `json:"address,omitempty"`
}

/* CrashObservation is a machine-parsed runtime signal with an input artifact. */
type CrashObservation struct {
	SchemaVersion    int              `json:"schema_version"`
	ID               string           `json:"id"`
	CampaignID       string           `json:"campaign_id"`
	RunID            string           `json:"run_id"`
	BuildID          string           `json:"build_id"`
	InputArtifactID  string           `json:"input_artifact_id"`
	Class            ObservationClass `json:"class"`
	Sanitizer        string           `json:"sanitizer,omitempty"`
	BugType          string           `json:"bug_type,omitempty"`
	Access           string           `json:"access,omitempty"`
	AccessSize       int64            `json:"access_size,omitempty"`
	Signal           string           `json:"signal,omitempty"`
	Summary          string           `json:"summary"`
	Frames           []StackFrame     `json:"frames,omitempty"`
	Signature        string           `json:"signature"`
	Deterministic    bool             `json:"deterministic"`
	ReproCount       int              `json:"repro_count"`
	SecurityRelevant bool             `json:"security_relevant"`
	CreatedAt        time.Time        `json:"created_at"`
}

/* CrashGroup deduplicates observations by normalized root-cause signature. */
type CrashGroup struct {
	SchemaVersion       int       `json:"schema_version"`
	ID                  string    `json:"id"`
	CampaignID          string    `json:"campaign_id"`
	Signature           string    `json:"signature"`
	ObservationIDs      []string  `json:"observation_ids"`
	CanonicalInputID    string    `json:"canonical_input_id,omitempty"`
	MinimizedArtifactID string    `json:"minimized_artifact_id,omitempty"`
	RootCause           string    `json:"root_cause,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

/* PrimitiveOperation is the smallest demonstrated low-level capability. */
type PrimitiveOperation string

const (
	PrimitiveCrash         PrimitiveOperation = "crash"
	PrimitiveOOBRead       PrimitiveOperation = "out_of_bounds_read"
	PrimitiveOOBWrite      PrimitiveOperation = "out_of_bounds_write"
	PrimitiveUseAfterFree  PrimitiveOperation = "use_after_free"
	PrimitiveTypeConfusion PrimitiveOperation = "type_confusion"
	PrimitiveInvalidFree   PrimitiveOperation = "invalid_free"
	PrimitiveControlData   PrimitiveOperation = "control_data_influence"
	PrimitiveOther         PrimitiveOperation = "other"
)

/* EvidenceValue makes unknown explicit and binds known values to artifacts/runs. */
type EvidenceValue struct {
	Value       string   `json:"value,omitempty"`
	Known       bool     `json:"known"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

/* PrimitiveAssessment is the evidence matrix for one root cause. */
type PrimitiveAssessment struct {
	SchemaVersion     int                `json:"schema_version"`
	ID                string             `json:"id"`
	CampaignID        string             `json:"campaign_id"`
	CrashGroupID      string             `json:"crash_group_id"`
	Operation         PrimitiveOperation `json:"operation"`
	OperationEvidence []string           `json:"operation_evidence_ids"`
	AttackerControl   EvidenceValue      `json:"attacker_control"`
	AccessWidth       EvidenceValue      `json:"access_width"`
	ValueControl      EvidenceValue      `json:"value_control"`
	TargetRelation    EvidenceValue      `json:"target_relation"`
	Repeatability     EvidenceValue      `json:"repeatability"`
	Reachability      EvidenceValue      `json:"reachability"`
	Mitigations       EvidenceValue      `json:"mitigations"`
	ExploitabilityGap EvidenceValue      `json:"exploitability_gap"`
	CreatedAt         time.Time          `json:"created_at"`
	ReviewedAt        *time.Time         `json:"reviewed_at,omitempty"`
}

/* FindingLabel is monotonic and deliberately separates primitives from novelty. */
type FindingLabel string

const (
	FindingHypothesis             FindingLabel = "hypothesis"
	FindingObservation            FindingLabel = "observation"
	FindingCrashObserved          FindingLabel = "crash_observed"
	FindingReproducedMemoryIssue  FindingLabel = "reproduced_memory_safety_issue"
	FindingPrimitiveCandidate     FindingLabel = "primitive_candidate"
	FindingPrimitiveConfirmed     FindingLabel = "primitive_confirmed"
	FindingCandidateVulnerability FindingLabel = "candidate_vulnerability"
)

/* GateCheck is one captured branch/novelty/security-relevance decision. */
type GateCheck struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
}

/* SourceEvidence captures one bounded external or repository lookup response. */
type SourceEvidence struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	CampaignID    string            `json:"campaign_id"`
	FindingID     string            `json:"finding_id"`
	Kind          string            `json:"kind"`
	SourceName    string            `json:"source_name"`
	Query         string            `json:"query"`
	RequestURL    string            `json:"request_url"`
	Status        string            `json:"status"`
	Summary       string            `json:"summary"`
	ResponseHash  string            `json:"response_hash,omitempty"`
	ArtifactID    string            `json:"artifact_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CheckedAt     time.Time         `json:"checked_at"`
}

/* SourceReview is an immutable human/parser interpretation of captured lookup evidence. */
type SourceReview struct {
	SchemaVersion    int       `json:"schema_version"`
	ID               string    `json:"id"`
	CampaignID       string    `json:"campaign_id"`
	FindingID        string    `json:"finding_id"`
	SourceEvidenceID string    `json:"source_evidence_id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Summary          string    `json:"summary"`
	ReviewedBy       string    `json:"reviewed_by"`
	ReviewedAt       time.Time `json:"reviewed_at"`
}

/* RevisionCheck records an executed supported-branch comparison. */
type RevisionCheck struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CampaignID    string    `json:"campaign_id"`
	FindingID     string    `json:"finding_id"`
	Revision      string    `json:"revision"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	BuildID       string    `json:"build_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	EvidenceIDs   []string  `json:"evidence_ids,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

/* RemediationValidation retains evidence that a candidate fix is effective. */
type RemediationValidation struct {
	SchemaVersion        int       `json:"schema_version"`
	ID                   string    `json:"id"`
	CampaignID           string    `json:"campaign_id"`
	FindingID            string    `json:"finding_id"`
	PatchArtifactID      string    `json:"patch_artifact_id"`
	ApprovalID           string    `json:"approval_id"`
	FixBuildID           string    `json:"fix_build_id"`
	ReproducerRunID      string    `json:"reproducer_run_id"`
	RegressionRunID      string    `json:"regression_run_id"`
	NegativeControlRunID string    `json:"negative_control_run_id"`
	OriginalSignalGone   bool      `json:"original_signal_gone"`
	RegressionPassed     bool      `json:"regression_passed"`
	NegativeControlClean bool      `json:"negative_control_clean"`
	EvidenceIDs          []string  `json:"evidence_ids"`
	ValidatedAt          time.Time `json:"validated_at"`
}

/* Finding is the reportable aggregate; disclosure remains human-gated. */
type Finding struct {
	SchemaVersion       int          `json:"schema_version"`
	ID                  string       `json:"id"`
	CampaignID          string       `json:"campaign_id"`
	CrashGroupID        string       `json:"crash_group_id"`
	PrimitiveID         string       `json:"primitive_id,omitempty"`
	Title               string       `json:"title"`
	Label               FindingLabel `json:"label"`
	CWE                 string       `json:"cwe,omitempty"`
	RootCause           string       `json:"root_cause,omitempty"`
	AffectedRevisions   []string     `json:"affected_revisions,omitempty"`
	EvidenceIDs         []string     `json:"evidence_ids,omitempty"`
	BranchChecks        []GateCheck  `json:"branch_checks,omitempty"`
	NoveltyChecks       []GateCheck  `json:"novelty_checks,omitempty"`
	NoveltyStatus       string       `json:"novelty_status,omitempty"`
	FixArtifactID       string       `json:"fix_artifact_id,omitempty"`
	RegressionRunID     string       `json:"regression_run_id,omitempty"`
	ReportArtifactID    string       `json:"report_artifact_id,omitempty"`
	HumanReviewApproval string       `json:"human_review_approval,omitempty"`
	DisclosureApproval  string       `json:"disclosure_approval,omitempty"`
	DisclosureStatus    string       `json:"disclosure_status"`
	DisclosureReference string       `json:"disclosure_reference,omitempty"`
	DisclosedAt         *time.Time   `json:"disclosed_at,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

/* Approval is a human policy decision for a concrete action/correlation ID. */
type Approval struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"id"`
	ScopeID       string     `json:"scope_id"`
	CampaignID    string     `json:"campaign_id"`
	CorrelationID string     `json:"correlation_id"`
	Operation     Operation  `json:"operation"`
	Status        string     `json:"status"`
	RequestedBy   string     `json:"requested_by"`
	DecidedBy     string     `json:"decided_by,omitempty"`
	Reason        string     `json:"reason"`
	CreatedAt     time.Time  `json:"created_at"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
}

/* AuditEvent is append-only and hash-chained by the persistence layer. */
type AuditEvent struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Sequence      int64             `json:"sequence"`
	PreviousHash  string            `json:"previous_hash,omitempty"`
	Hash          string            `json:"hash"`
	ActorID       string            `json:"actor_id"`
	Action        string            `json:"action"`
	ResourceType  string            `json:"resource_type"`
	ResourceID    string            `json:"resource_id"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	Decision      string            `json:"decision,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}
