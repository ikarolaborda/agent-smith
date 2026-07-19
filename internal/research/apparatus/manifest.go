// Package apparatus validates versioned research adapters and creates typed jobs.
package apparatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// LoadManifest reads a bounded JSON apparatus manifest.
func LoadManifest(path string) (domain.ApparatusManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.ApparatusManifest{}, err
	}
	defer file.Close()
	const maxManifestBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return domain.ApparatusManifest{}, fmt.Errorf("apparatus: read manifest: %w", err)
	}
	if len(body) > maxManifestBytes {
		return domain.ApparatusManifest{}, errors.New("apparatus: manifest exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest domain.ApparatusManifest
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("apparatus: decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return manifest, errors.New("apparatus: trailing manifest data")
	}
	return manifest, ValidateManifest(manifest)
}

// ValidateManifest fails closed on incomplete provenance or unsafe harness data.
func ValidateManifest(manifest domain.ApparatusManifest) error {
	if manifest.SchemaVersion != 1 || manifest.ID == "" || manifest.Name == "" || manifest.Version == "" || manifest.Engine == "" {
		return errors.New("apparatus: version-one identity and engine required")
	}
	if !digestPattern.MatchString(manifest.ImageDigest) {
		return errors.New("apparatus: exact sha256 image digest required")
	}
	if len(manifest.Harnesses) == 0 || len(manifest.Operations) == 0 || len(manifest.Architectures) == 0 || len(manifest.Sanitizers) == 0 {
		return errors.New("apparatus: harnesses, operations, architectures, and sanitizers required")
	}
	seen := map[string]bool{}
	for _, harness := range manifest.Harnesses {
		if !safeName(harness.Name) || !strings.HasPrefix(harness.Binary, "/build/") || seen[harness.Name] {
			return errors.New("apparatus: unique safe harness name and /build binary required")
		}
		seen[harness.Name] = true
	}
	seenOperations := make(map[domain.Operation]bool, len(manifest.Operations))
	for _, operation := range manifest.Operations {
		if !domain.IsKnownOperation(operation) || seenOperations[operation] {
			return errors.New("apparatus: unknown or duplicate operation")
		}
		seenOperations[operation] = true
	}
	for key, value := range manifest.Environment {
		if !allowedEnvironment(key) || strings.ContainsRune(value, 0) {
			return errors.New("apparatus: invalid environment allowlist")
		}
	}
	if err := manifest.Limits.Validate(); err != nil {
		return fmt.Errorf("apparatus: %w", err)
	}
	return nil
}

// JobRequest contains broker paths and authorization already chosen by policy.
type JobRequest struct {
	ID              string                `json:"-"`
	RunID           string                `json:"-"`
	CampaignID      string                `json:"-"`
	ScopeID         string                `json:"-"`
	TargetID        string                `json:"target_id,omitempty"`
	BuildID         string                `json:"build_id,omitempty"`
	InputArtifactID string                `json:"input_artifact_id,omitempty"`
	PatchArtifactID string                `json:"patch_artifact_id,omitempty"`
	Operation       domain.Operation      `json:"operation"`
	Harness         string                `json:"harness"`
	Revision        string                `json:"revision"`
	Sanitizer       string                `json:"sanitizer,omitempty"`
	SourceDir       string                `json:"source_dir"`
	BuildDir        string                `json:"build_dir,omitempty"`
	CorpusDir       string                `json:"corpus_dir,omitempty"`
	EvidenceDir     string                `json:"-"`
	PatchDir        string                `json:"-"`
	Arguments       map[string]string     `json:"arguments,omitempty"`
	Budget          domain.ResourceBudget `json:"budget"`
	CorrelationID   string                `json:"correlation_id"`
}

// NewJob converts a manifest request into a fixed-mount worker envelope.
func NewJob(manifest domain.ApparatusManifest, request JobRequest) (domain.WorkerJob, error) {
	if err := ValidateManifest(manifest); err != nil {
		return domain.WorkerJob{}, err
	}
	if !containsOperation(manifest.Operations, request.Operation) {
		return domain.WorkerJob{}, errors.New("apparatus: operation not supported by manifest")
	}
	var harness domain.HarnessManifest
	for _, candidate := range manifest.Harnesses {
		if candidate.Name == request.Harness {
			harness = candidate
			break
		}
	}
	if harness.Name == "" {
		return domain.WorkerJob{}, errors.New("apparatus: unknown harness")
	}
	budget := request.Budget
	if budget == (domain.ResourceBudget{}) {
		budget = manifest.Limits
	}
	if err := budget.Validate(); err != nil {
		return domain.WorkerJob{}, fmt.Errorf("apparatus: %w", err)
	}
	if exceedsBudget(budget, manifest.Limits) {
		return domain.WorkerJob{}, errors.New("apparatus: requested budget exceeds manifest limits")
	}
	if requestNeedsEvidence(request.Operation, request.Arguments) {
		if request.InputArtifactID == "" || request.EvidenceDir == "" {
			return domain.WorkerJob{}, errors.New("apparatus: operation requires a verified evidence artifact")
		}
	} else if request.InputArtifactID != "" || request.EvidenceDir != "" {
		return domain.WorkerJob{}, errors.New("apparatus: operation does not accept an evidence artifact")
	}
	if request.Operation == domain.OperationBuild && request.PatchArtifactID != "" {
		if request.PatchDir == "" {
			return domain.WorkerJob{}, errors.New("apparatus: patched build requires verified patch evidence")
		}
	} else if request.PatchArtifactID != "" || request.PatchDir != "" {
		return domain.WorkerJob{}, errors.New("apparatus: operation does not accept a patch artifact")
	}
	arguments := map[string]string{"manifest": manifest.ID, "harness": harness.Name, "revision": request.Revision}
	sanitizer := request.Sanitizer
	if sanitizer == "" && len(manifest.Sanitizers) > 0 {
		sanitizer = manifest.Sanitizers[0]
	}
	if sanitizer == "" || !containsString(manifest.Sanitizers, sanitizer) {
		return domain.WorkerJob{}, errors.New("apparatus: unsupported sanitizer")
	}
	arguments["sanitizer"] = sanitizer
	for key, value := range request.Arguments {
		if key == "max-total-time" && request.Operation == domain.OperationFuzz {
			arguments[key] = value
			continue
		}
		if key == "validation-kind" && request.Operation == domain.OperationRegressionTest && (value == "reproducer" || value == "regression" || value == "negative_control") {
			arguments[key] = value
			continue
		}
		return domain.WorkerJob{}, fmt.Errorf("apparatus: unsupported job argument %q", key)
	}
	if request.Operation == domain.OperationRegressionTest && arguments["validation-kind"] == "" {
		return domain.WorkerJob{}, errors.New("apparatus: regression validation kind required")
	}
	mounts := []domain.JobMount{{Name: "source", HostPath: request.SourceDir, ContainerPath: "/source", ReadOnly: true}}
	if request.BuildDir != "" {
		mounts = append(mounts, domain.JobMount{Name: "build", HostPath: request.BuildDir, ContainerPath: "/build", ReadOnly: true})
	}
	if request.CorpusDir != "" {
		mounts = append(mounts, domain.JobMount{Name: "corpus", HostPath: request.CorpusDir, ContainerPath: "/corpus", ReadOnly: false})
	}
	if request.EvidenceDir != "" {
		mounts = append(mounts, domain.JobMount{Name: "evidence", HostPath: request.EvidenceDir, ContainerPath: "/evidence", ReadOnly: true})
	}
	if request.PatchDir != "" {
		mounts = append(mounts, domain.JobMount{Name: "patch", HostPath: request.PatchDir, ContainerPath: "/patch", ReadOnly: true})
	}
	return domain.WorkerJob{
		ID: request.ID, RunID: request.RunID, CampaignID: request.CampaignID, ScopeID: request.ScopeID, TargetID: request.TargetID, BuildID: request.BuildID, InputArtifactID: request.InputArtifactID, PatchArtifactID: request.PatchArtifactID, Operation: request.Operation,
		Arguments: arguments, ImageDigest: manifest.ImageDigest, Runtime: "rootless-docker", Mounts: mounts,
		Environment:   manifest.Environment,
		ArtifactRules: ArtifactRules(request.Operation), Budget: budget, AuditCorrelationID: request.CorrelationID,
	}, nil
}

// ArtifactRules returns the adapter-owned output schema for an operation.
func ArtifactRules(operation domain.Operation) []domain.ArtifactRule {
	switch operation {
	case domain.OperationInspect:
		return []domain.ArtifactRule{{Role: "apparatus_inspection", MediaType: "application/json", Glob: "apparatus-inspection.json", MaxCount: 1, MaxBytes: 1 << 20}}
	case domain.OperationBuild:
		return []domain.ArtifactRule{
			{Role: "harness_binary", MediaType: "application/x-executable", Glob: "fuzz_target", MaxCount: 1, MaxBytes: 128 << 20},
			{Role: "build_provenance", MediaType: "application/json", Glob: "build-provenance.json", MaxCount: 1, MaxBytes: 1 << 20},
		}
	case domain.OperationFuzz, domain.OperationReproduce:
		return []domain.ArtifactRule{{Role: "crashing_input", MediaType: "application/octet-stream", Glob: "crash-*", MaxCount: 100, MaxBytes: 256 << 20}}
	case domain.OperationMinimize:
		return []domain.ArtifactRule{{Role: "minimized_input", MediaType: "application/octet-stream", Glob: "minimized", MaxCount: 1, MaxBytes: 16 << 20}}
	case domain.OperationCoverage:
		return []domain.ArtifactRule{{Role: "coverage", MediaType: "application/json", Glob: "coverage.json", MaxCount: 1, MaxBytes: 16 << 20}}
	case domain.OperationSymbolize:
		return []domain.ArtifactRule{{Role: "symbolized_stack", MediaType: "text/plain; charset=utf-8", Glob: "symbolized.txt", MaxCount: 1, MaxBytes: 4 << 20}}
	case domain.OperationCompareBranch:
		return []domain.ArtifactRule{
			{Role: "revision_comparison", MediaType: "application/json", Glob: "comparison.json", MaxCount: 1, MaxBytes: 1 << 20},
			{Role: "revision_comparison_log", MediaType: "text/plain; charset=utf-8", Glob: "comparison.log", MaxCount: 1, MaxBytes: 4 << 20},
		}
	case domain.OperationRegressionTest:
		return []domain.ArtifactRule{
			{Role: "regression_result", MediaType: "application/json", Glob: "regression.json", MaxCount: 1, MaxBytes: 1 << 20},
			{Role: "regression_log", MediaType: "text/plain; charset=utf-8", Glob: "regression.log", MaxCount: 1, MaxBytes: 4 << 20},
		}
	case domain.OperationSeed:
		return []domain.ArtifactRule{{Role: "corpus_seed", MediaType: "application/octet-stream", Glob: "seed-*", MaxCount: 100, MaxBytes: 16 << 20}}
	case domain.OperationMergeCorpus:
		return []domain.ArtifactRule{{Role: "corpus_entry", MediaType: "application/octet-stream", Glob: "merged/*", MaxCount: 10000, MaxBytes: 1 << 30}}
	default:
		return nil
	}
}

func safeName(value string) bool {
	if value == "" || len(value) > 64 || filepath.Base(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func containsOperation(values []domain.Operation, want domain.Operation) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func exceedsBudget(request, limit domain.ResourceBudget) bool {
	return over(request.MaxWallSeconds, limit.MaxWallSeconds) || over(request.MaxMemoryBytes, limit.MaxMemoryBytes) || over(request.MaxCPUSeconds, limit.MaxCPUSeconds) || over(request.MaxDiskBytes, limit.MaxDiskBytes) || over(request.MaxInodes, limit.MaxInodes) || over(request.MaxPIDs, limit.MaxPIDs) || request.MaxConcurrent < 0 || limit.MaxConcurrent > 0 && request.MaxConcurrent > limit.MaxConcurrent
}

func over(request, limit int64) bool { return request < 0 || limit > 0 && request > limit }

func allowedEnvironment(key string) bool {
	return key == "ASAN_OPTIONS" || key == "UBSAN_OPTIONS" || key == "MSAN_OPTIONS"
}

func operationNeedsEvidence(operation domain.Operation) bool {
	switch operation {
	case domain.OperationReproduce, domain.OperationMinimize, domain.OperationSymbolize, domain.OperationCompareBranch:
		return true
	default:
		return false
	}
}

func requestNeedsEvidence(operation domain.Operation, arguments map[string]string) bool {
	if operation == domain.OperationRegressionTest {
		return arguments["validation-kind"] == "reproducer" || arguments["validation-kind"] == "negative_control"
	}
	return operationNeedsEvidence(operation)
}
