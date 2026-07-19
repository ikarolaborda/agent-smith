package apparatus

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const fixtureDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestValidateManifestAndBuildTypedJob(t *testing.T) {
	source, build, corpus := t.TempDir(), t.TempDir(), t.TempDir()
	manifest := validManifest()
	job, err := NewJob(manifest, JobRequest{
		CampaignID: "campaign", ScopeID: "scope", Operation: domain.OperationFuzz, Harness: "parser", Revision: "abc",
		SourceDir: source, BuildDir: build, CorpusDir: corpus, CorrelationID: "correlation", Arguments: map[string]string{"max-total-time": "30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.ImageDigest != fixtureDigest || job.Arguments["harness"] != "parser" || len(job.Mounts) != 3 || job.Mounts[1].ReadOnly != true || job.Mounts[2].ReadOnly {
		t.Fatalf("job=%#v", job)
	}
	if len(job.ArtifactRules) != 1 || job.ArtifactRules[0].Role != "crashing_input" {
		t.Fatalf("rules=%#v", job.ArtifactRules)
	}
}

func TestManifestFailsClosed(t *testing.T) {
	manifest := validManifest()
	manifest.ImageDigest = "latest"
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("accepted unpinned image")
	}
	manifest = validManifest()
	manifest.Environment["PATH"] = "/host/bin"
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("accepted arbitrary environment")
	}
	manifest = validManifest()
	if _, err := NewJob(manifest, JobRequest{Operation: domain.OperationFuzz, Harness: "missing"}); err == nil {
		t.Fatal("accepted unknown harness")
	}
	if _, err := NewJob(manifest, JobRequest{Operation: domain.OperationFuzz, Harness: "parser", Arguments: map[string]string{"harness": "override"}}); err == nil {
		t.Fatal("accepted unsupported/reserved argument")
	}
}

func TestLoadManifestRejectsUnknownAndTrailingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("accepted unknown manifest field")
	}
	if err := os.WriteFile(path, []byte(`{} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("accepted trailing manifest")
	}
}

func validManifest() domain.ApparatusManifest {
	return domain.ApparatusManifest{
		SchemaVersion: 1, ID: "apparatus", Name: "fixture", Version: "1", ImageDigest: fixtureDigest, Engine: "libfuzzer",
		Sanitizers: []string{"address"}, Architectures: []string{"amd64"},
		Harnesses:  []domain.HarnessManifest{{Name: "parser", Binary: "/build/fuzz_target"}},
		Operations: []domain.Operation{domain.OperationBuild, domain.OperationFuzz}, Environment: map[string]string{"ASAN_OPTIONS": "abort_on_error=1"},
		Limits: domain.ResourceBudget{MaxWallSeconds: 60, MaxMemoryBytes: 1 << 30, MaxCPUSeconds: 60, MaxDiskBytes: 1 << 30, MaxPIDs: 32, MaxConcurrent: 1},
	}
}
