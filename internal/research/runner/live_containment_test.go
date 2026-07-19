package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

func TestLiveDockerContainment(t *testing.T) {
	imageDigest := strings.TrimSpace(os.Getenv("AGENT_SMITH_LIVE_CONTAINMENT_IMAGE"))
	if imageDigest == "" {
		t.Skip("set AGENT_SMITH_LIVE_CONTAINMENT_IMAGE to an exact locally built containment-probe image ID")
	}
	if !digestPattern.MatchString(imageDigest) {
		t.Fatal("AGENT_SMITH_LIVE_CONTAINMENT_IMAGE must be an exact sha256 image ID")
	}
	ctx := context.Background()
	repository, campaign := runnerStore(t)
	defer repository.Close()
	backend := NewDockerBackend(DockerOptions{RequireRootless: false})
	broker, err := NewBroker(Options{Backend: backend, Journal: repository, Artifacts: repository, StagingRoot: filepath.Join(repository.Root(), "live-staging")})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Start(ctx); err != nil {
		t.Fatalf("live Docker preflight: %v", err)
	}
	defer broker.Close()

	t.Setenv("OPENAI_API_KEY", "must-not-enter-worker")
	tests := []struct {
		harness       string
		maxDiskBytes  int64
		maxInodes     int64
		wantCompleted bool
	}{
		{"containment_network", 1 << 20, 128, true},
		{"containment_mount", 1 << 20, 128, true},
		{"containment_secrets", 1 << 20, 128, true},
		{"containment_cross_campaign", 1 << 20, 128, true},
		{"containment_device", 1 << 20, 128, true},
		{"containment_orphan", 1 << 20, 128, true},
		{"containment_symlink", 1 << 20, 128, false},
		{"containment_disk", 64 << 10, 128, false},
		{"containment_inodes", 1 << 20, 16, false},
	}
	for _, test := range tests {
		t.Run(test.harness, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(repository.Root(), "agent-smith-cross-campaign-secret"), []byte("not mounted"), 0o600); err != nil {
				t.Fatal(err)
			}
			job := domain.WorkerJob{
				CampaignID: campaign.ID, ScopeID: campaign.ScopeID, Operation: domain.OperationInspect, ImageDigest: imageDigest,
				Arguments:          map[string]string{"manifest": "containment-probe", "harness": test.harness, "revision": "fixture", "sanitizer": "address"},
				AuditCorrelationID: "live-" + test.harness,
				Mounts:             []domain.JobMount{{Name: "source", HostPath: source, ContainerPath: "/source", ReadOnly: true}},
				ArtifactRules:      artifactRulesForContainmentProbe(),
				Budget: domain.ResourceBudget{MaxWallSeconds: 10, MaxMemoryBytes: 256 << 20, MaxCPUSeconds: 10,
					MaxDiskBytes: test.maxDiskBytes, MaxInodes: test.maxInodes, MaxPIDs: 16, MaxConcurrent: 1},
			}
			run, err := broker.Submit(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			result, err := broker.Wait(waitCtx, run.ID)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCompleted && result.Status != domain.RunCompleted {
				t.Fatalf("probe failed: %#v", result)
			}
			if !test.wantCompleted && result.Status == domain.RunCompleted {
				t.Fatalf("hostile probe escaped its expected control: %#v", result)
			}
			if test.harness == "containment_orphan" {
				var stdout, stderr bytes.Buffer
				if err := (osCommandExecutor{}).Run(ctx, "docker", []string{"ps", "-aq", "--filter", "label=agent-smith.research.run=" + run.ID}, &stdout, &stderr); err != nil {
					t.Fatalf("inspect orphan cleanup: %v: %s", err, stderr.String())
				}
				if strings.TrimSpace(stdout.String()) != "" {
					t.Fatalf("worker container survived completion: %s", stdout.String())
				}
			}
		})
	}
}

func artifactRulesForContainmentProbe() []domain.ArtifactRule {
	return []domain.ArtifactRule{{Role: "apparatus_inspection", MediaType: "application/json", Glob: "apparatus-inspection.json", MaxCount: 1, MaxBytes: 1 << 20}}
}
