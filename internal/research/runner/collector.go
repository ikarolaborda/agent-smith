package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const defaultCapturedOutputLimit int64 = 1 << 20

// Collector treats worker output as hostile and ingests only allowlisted files.
type Collector struct {
	sink                   ArtifactSink
	maxCapturedOutputBytes int64
}

func NewCollector(sink ArtifactSink, maxCapturedOutputBytes int64) *Collector {
	if maxCapturedOutputBytes <= 0 {
		maxCapturedOutputBytes = defaultCapturedOutputLimit
	}
	return &Collector{sink: sink, maxCapturedOutputBytes: maxCapturedOutputBytes}
}

// Collect validates output shape, streams artifacts, and produces typed IDs.
func (c *Collector) Collect(ctx context.Context, job domain.WorkerJob, staging string, execution Execution, assurance Assurance) (domain.RunResult, error) {
	result := domain.RunResult{
		SchemaVersion: 1, RunID: job.RunID, Operation: job.Operation, Status: execution.Status, Exit: execution.Exit,
		ResourceUsage: execution.Usage, Apparatus: execution.Apparatus, IsolationAssurance: assurance.Isolation,
		AuditCorrelationID: job.AuditCorrelationID,
	}
	result.Apparatus.ImageDigest = job.ImageDigest
	result.Apparatus.Runtime = assurance.Runtime
	var parentIDs []string
	if job.InputArtifactID != "" {
		parentIDs = []string{job.InputArtifactID}
	}
	stdout, stdoutTruncated, stdoutDropped := boundedBytes(execution.Stdout, c.maxCapturedOutputBytes)
	stderr, stderrTruncated, stderrDropped := boundedBytes(execution.Stderr, c.maxCapturedOutputBytes)
	result.Output.StdoutTruncated = execution.StdoutTruncated || stdoutTruncated
	result.Output.StderrTruncated = execution.StderrTruncated || stderrTruncated
	result.Output.BytesDropped = execution.BytesDropped + stdoutDropped + stderrDropped
	if len(stdout) > 0 {
		artifact, err := c.sink.PutArtifact(ctx, domain.Artifact{
			SchemaVersion: 1, CampaignID: job.CampaignID, RunID: job.RunID, ParentIDs: parentIDs, Role: "stdout_log", MediaType: "text/plain; charset=utf-8", Sensitivity: "research",
		}, bytes.NewReader(stdout))
		if err != nil {
			return result, err
		}
		result.Output.StdoutArtifactID = artifact.ID
		result.ArtifactIDs = append(result.ArtifactIDs, artifact.ID)
	}
	if len(stderr) > 0 {
		artifact, err := c.sink.PutArtifact(ctx, domain.Artifact{
			SchemaVersion: 1, CampaignID: job.CampaignID, RunID: job.RunID, ParentIDs: parentIDs, Role: "stderr_log", MediaType: "text/plain; charset=utf-8", Sensitivity: "research",
		}, bytes.NewReader(stderr))
		if err != nil {
			return result, err
		}
		result.Output.StderrArtifactID = artifact.ID
		result.ArtifactIDs = append(result.ArtifactIDs, artifact.ID)
	}

	root, err := filepath.Abs(staging)
	if err != nil {
		return result, err
	}
	counts := make([]int, len(job.ArtifactRules))
	bytesByRule := make([]int64, len(job.ArtifactRules))
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("research runner: artifact collector rejects symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("research runner: artifact collector rejects non-regular files")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("research runner: artifact escaped staging root")
		}
		ruleIndex := matchingRule(filepath.ToSlash(relative), job.ArtifactRules)
		if ruleIndex < 0 {
			return fmt.Errorf("research runner: worker emitted unallowlisted artifact %q", filepath.ToSlash(relative))
		}
		rule := job.ArtifactRules[ruleIndex]
		info, err := entry.Info()
		if err != nil {
			return err
		}
		counts[ruleIndex]++
		bytesByRule[ruleIndex] += info.Size()
		if rule.MaxCount <= 0 || counts[ruleIndex] > rule.MaxCount || rule.MaxBytes <= 0 || bytesByRule[ruleIndex] > rule.MaxBytes {
			return fmt.Errorf("research runner: artifact rule %q exceeded", rule.Role)
		}
		file, err := openUnchangedRegular(path, info)
		if err != nil {
			return err
		}
		artifact, ingestErr := c.sink.PutArtifact(ctx, domain.Artifact{
			SchemaVersion: 1, CampaignID: job.CampaignID, RunID: job.RunID, Role: rule.Role, MediaType: rule.MediaType,
			Sensitivity: "research", ParentIDs: parentIDs, CreatedAt: time.Now().UTC(),
		}, file)
		file.Close()
		if ingestErr != nil {
			return ingestErr
		}
		result.ArtifactIDs = append(result.ArtifactIDs, artifact.ID)
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func boundedBytes(input []byte, limit int64) ([]byte, bool, int64) {
	if int64(len(input)) <= limit {
		return input, false, 0
	}
	return input[:limit], true, int64(len(input)) - limit
}

func matchingRule(relative string, rules []domain.ArtifactRule) int {
	for index, rule := range rules {
		matched, err := filepath.Match(filepath.ToSlash(rule.Glob), relative)
		if err == nil && matched && rule.Role != "" && rule.MediaType != "" {
			return index
		}
	}
	return -1
}

func openUnchangedRegular(path string, before fs.FileInfo) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, errors.New("research runner: artifact changed during ingestion")
	}
	return file, nil
}
