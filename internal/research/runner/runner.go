// Package runner implements durable typed research worker scheduling. It is
// intentionally independent of the chat Tool interface.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

var (
	ErrInvalidJob = errors.New("research runner: invalid job")
	ErrNotRunning = errors.New("research runner: broker not running")
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Assurance is a verified worker backend security posture.
type Assurance struct {
	Backend       string `json:"backend"`
	Isolation     string `json:"isolation"`
	Runtime       string `json:"runtime"`
	Rootless      bool   `json:"rootless"`
	Seccomp       string `json:"seccomp"`
	CgroupVersion int    `json:"cgroup_version"`
}

// Execution is untrusted backend output before artifact ingestion.
type Execution struct {
	Status          domain.RunStatus
	Exit            domain.RunExit
	Usage           domain.ResourceUsage
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	BytesDropped    int64
	Apparatus       domain.ApparatusIdentity
}

// Backend runs one job in an isolation boundary and writes files only to staging.
type Backend interface {
	Preflight(context.Context) (Assurance, error)
	Execute(context.Context, domain.WorkerJob, string) (Execution, error)
}

// Journal is the durable job/run subset required by Broker.
type Journal interface {
	CreateJobAndRun(context.Context, domain.WorkerJob, domain.ExperimentRun) error
	SaveJobAndRun(context.Context, domain.WorkerJob, domain.ExperimentRun) error
	SaveWorkerJob(context.Context, domain.WorkerJob) error
	GetWorkerJob(context.Context, string) (domain.WorkerJob, error)
	GetWorkerJobByRunID(context.Context, string) (domain.WorkerJob, error)
	ListWorkerJobsByStatus(context.Context, ...domain.RunStatus) ([]domain.WorkerJob, error)
	SaveRun(context.Context, domain.ExperimentRun) error
	GetRun(context.Context, string) (domain.ExperimentRun, error)
}

// ArtifactSink accepts streamed, bounded evidence.
type ArtifactSink interface {
	PutArtifact(context.Context, domain.Artifact, io.Reader) (domain.Artifact, error)
}

// Options configures bounded worker scheduling.
type Options struct {
	Backend                Backend
	Journal                Journal
	Artifacts              ArtifactSink
	StagingRoot            string
	GlobalConcurrency      int
	CampaignConcurrency    int
	MaxCapturedOutputBytes int64
	OnResult               func(context.Context, domain.WorkerJob, domain.RunResult) error
}

// Broker schedules jobs, persists lifecycle changes, and ingests results.
type Broker struct {
	backend             Backend
	journal             Journal
	collector           *Collector
	stagingRoot         string
	globalConcurrency   int
	campaignConcurrency int
	queue               chan string

	mu             sync.Mutex
	started        bool
	assurance      Assurance
	campaignActive map[string]int
	cancels        map[string]context.CancelFunc
	waiters        map[string][]chan domain.RunResult
	onResult       func(context.Context, domain.WorkerJob, domain.RunResult) error
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// NewBroker validates local configuration; Start performs backend preflight.
func NewBroker(opts Options) (*Broker, error) {
	if opts.Backend == nil || opts.Journal == nil || opts.Artifacts == nil {
		return nil, errors.New("research runner: backend, journal, and artifact sink required")
	}
	if opts.StagingRoot == "" {
		return nil, errors.New("research runner: staging root required")
	}
	root, err := filepath.Abs(opts.StagingRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	if opts.GlobalConcurrency <= 0 {
		opts.GlobalConcurrency = 1
	}
	if opts.CampaignConcurrency <= 0 {
		opts.CampaignConcurrency = 1
	}
	collector := NewCollector(opts.Artifacts, opts.MaxCapturedOutputBytes)
	return &Broker{
		backend: opts.Backend, journal: opts.Journal, collector: collector, stagingRoot: root,
		globalConcurrency: opts.GlobalConcurrency, campaignConcurrency: opts.CampaignConcurrency,
		queue: make(chan string, 1024), campaignActive: map[string]int{}, cancels: map[string]context.CancelFunc{}, waiters: map[string][]chan domain.RunResult{}, onResult: opts.OnResult,
	}, nil
}

// Start verifies isolation and recovers queued/interrupted jobs before work begins.
func (b *Broker) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	assurance, err := b.backend.Preflight(ctx)
	if err != nil {
		return fmt.Errorf("research runner: backend preflight: %w", err)
	}
	if assurance.Isolation == "" {
		return errors.New("research runner: backend returned no isolation assurance")
	}
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return nil
	}
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.assurance, b.started = assurance, true
	b.mu.Unlock()

	jobs, err := b.journal.ListWorkerJobsByStatus(ctx, domain.RunQueued, domain.RunRunning)
	if err != nil {
		b.Close()
		return fmt.Errorf("research runner: recover queue: %w", err)
	}
	queuedIDs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if job.Status == domain.RunRunning {
			job.Status = domain.RunQueued
			job.UpdatedAt = time.Now().UTC()
			run, err := b.journal.GetRun(ctx, job.RunID)
			if err != nil {
				b.Close()
				return err
			}
			run.Status, run.WorkerID, run.StartedAt = domain.RunQueued, "", nil
			if err := b.journal.SaveJobAndRun(ctx, job, run); err != nil {
				b.Close()
				return err
			}
		}
		queuedIDs = append(queuedIDs, job.ID)
	}
	for range b.globalConcurrency {
		b.wg.Add(1)
		go b.worker()
	}
	for _, jobID := range queuedIDs {
		select {
		case b.queue <- jobID:
		case <-b.ctx.Done():
			b.Close()
			return b.ctx.Err()
		}
	}
	return nil
}

// Assurance returns the verified backend posture after Start.
func (b *Broker) Assurance() Assurance {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.assurance
}

// Submit durably queues a previously authorized job.
func (b *Broker) Submit(ctx context.Context, job domain.WorkerJob) (domain.ExperimentRun, error) {
	b.mu.Lock()
	started := b.started
	b.mu.Unlock()
	if !started {
		return domain.ExperimentRun{}, ErrNotRunning
	}
	if err := validateJob(job); err != nil {
		return domain.ExperimentRun{}, err
	}
	now := time.Now().UTC()
	if job.ID == "" {
		job.ID = newID("job")
	}
	if job.RunID == "" {
		job.RunID = newID("run")
	}
	job.SchemaVersion, job.Status, job.CreatedAt, job.UpdatedAt = 1, domain.RunQueued, now, now
	run := domain.ExperimentRun{
		SchemaVersion: 1, ID: job.RunID, CampaignID: job.CampaignID, ScopeID: job.ScopeID, TargetID: job.TargetID, BuildID: job.BuildID, InputArtifactID: job.InputArtifactID, PatchArtifactID: job.PatchArtifactID, Operation: job.Operation,
		Arguments: job.Arguments, Status: domain.RunQueued, AuditCorrelationID: job.AuditCorrelationID, CreatedAt: now,
	}
	if err := b.journal.CreateJobAndRun(ctx, job, run); err != nil {
		return run, err
	}
	select {
	case b.queue <- job.ID:
		return run, nil
	case <-ctx.Done():
		return run, ctx.Err()
	case <-b.ctx.Done():
		return run, ErrNotRunning
	}
}

// Wait returns a terminal result and supports multiple observers.
func (b *Broker) Wait(ctx context.Context, runID string) (domain.RunResult, error) {
	run, err := b.journal.GetRun(ctx, runID)
	if err != nil {
		return domain.RunResult{}, err
	}
	if terminalRun(run.Status) {
		return resultFromRun(run), nil
	}
	ch := make(chan domain.RunResult, 1)
	b.mu.Lock()
	b.waiters[runID] = append(b.waiters[runID], ch)
	b.mu.Unlock()
	// Close the Get/register race: if completion happened between the first
	// read and waiter registration, return the durable terminal snapshot.
	if current, currentErr := b.journal.GetRun(ctx, runID); currentErr == nil && terminalRun(current.Status) {
		b.removeWaiter(runID, ch)
		return resultFromRun(current), nil
	}
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return domain.RunResult{}, ctx.Err()
	}
}

// Cancel stops a running job or marks queued work cancelled before execution.
func (b *Broker) Cancel(ctx context.Context, jobID string) error {
	job, err := b.journal.GetWorkerJob(ctx, jobID)
	if err != nil {
		return err
	}
	b.mu.Lock()
	cancel := b.cancels[jobID]
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		return nil
	}
	if terminalRun(job.Status) {
		return nil
	}
	now := time.Now().UTC()
	job.Status, job.UpdatedAt = domain.RunCancelled, now
	run, err := b.journal.GetRun(ctx, job.RunID)
	if err != nil {
		return err
	}
	run.Status, run.CompletedAt = domain.RunCancelled, &now
	run.Exit.Reason = "cancelled before execution"
	if err := b.journal.SaveJobAndRun(ctx, job, run); err != nil {
		return err
	}
	b.publish(resultFromRun(run))
	return nil
}

// CancelRun resolves the private job identity from a public run ID and applies
// the same durable cancellation path.
func (b *Broker) CancelRun(ctx context.Context, runID string) error {
	job, err := b.journal.GetWorkerJobByRunID(ctx, runID)
	if err != nil {
		return err
	}
	return b.Cancel(ctx, job.ID)
}

// Close cancels workers and waits for broker goroutines.
func (b *Broker) Close() error {
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return nil
	}
	b.started = false
	cancel := b.cancel
	b.mu.Unlock()
	cancel()
	b.wg.Wait()
	return nil
}

func (b *Broker) worker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case jobID := <-b.queue:
			job, err := b.journal.GetWorkerJob(b.ctx, jobID)
			if err != nil || job.Status != domain.RunQueued {
				continue
			}
			if !b.acquireCampaign(job.CampaignID) {
				return
			}
			b.execute(job)
			b.releaseCampaign(job.CampaignID)
		}
	}
}

func (b *Broker) acquireCampaign(campaignID string) bool {
	for {
		b.mu.Lock()
		if b.campaignActive[campaignID] < b.campaignConcurrency {
			b.campaignActive[campaignID]++
			b.mu.Unlock()
			return true
		}
		b.mu.Unlock()
		select {
		case <-b.ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *Broker) releaseCampaign(campaignID string) {
	b.mu.Lock()
	b.campaignActive[campaignID]--
	b.mu.Unlock()
}

func (b *Broker) execute(job domain.WorkerJob) {
	now := time.Now().UTC()
	job.Status, job.UpdatedAt = domain.RunRunning, now
	run, err := b.journal.GetRun(b.ctx, job.RunID)
	if err != nil {
		return
	}
	run.Status, run.WorkerID, run.IsolationAssurance, run.StartedAt = domain.RunRunning, b.assurance.Backend, b.assurance.Isolation, &now
	if b.journal.SaveJobAndRun(b.ctx, job, run) != nil {
		return
	}
	jobCtx := b.ctx
	var cancel context.CancelFunc
	if job.Budget.MaxWallSeconds > 0 {
		jobCtx, cancel = context.WithTimeout(jobCtx, time.Duration(job.Budget.MaxWallSeconds)*time.Second)
	} else {
		jobCtx, cancel = context.WithCancel(jobCtx)
	}
	b.mu.Lock()
	b.cancels[job.ID] = cancel
	b.mu.Unlock()
	defer func() {
		cancel()
		b.mu.Lock()
		delete(b.cancels, job.ID)
		b.mu.Unlock()
	}()
	staging, err := os.MkdirTemp(b.stagingRoot, "run-"+safeID(job.RunID)+"-")
	if err != nil {
		b.finishFailed(job, run, err)
		return
	}
	defer os.RemoveAll(staging)
	// The staging parent is 0700 and therefore untraversable by other host
	// users. Its bind-mounted leaf is 0777 so rootless/userns-remapped container
	// identities can export artifacts without granting access outside that
	// private parent.
	_ = os.Chmod(staging, 0o777)
	execution, executeErr := b.backend.Execute(jobCtx, job, staging)
	result, collectErr := b.collector.Collect(b.ctx, job, staging, execution, b.assurance)
	if executeErr != nil && execution.Exit.Reason == "" {
		execution.Exit.Reason = executeErr.Error()
	}
	if collectErr != nil {
		b.finishFailed(job, run, collectErr)
		return
	}
	completed := time.Now().UTC()
	if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
		result.Status, result.Exit.Reason = domain.RunTimedOut, "wall-clock budget exceeded"
	} else if errors.Is(jobCtx.Err(), context.Canceled) {
		result.Status, result.Exit.Reason = domain.RunCancelled, "cancelled"
	} else if result.Status == "" {
		if executeErr != nil {
			result.Status = domain.RunFailed
		} else {
			result.Status = domain.RunCompleted
		}
	}
	run.Status, run.Exit, run.Usage, run.ArtifactIDs, run.CompletedAt = result.Status, result.Exit, result.ResourceUsage, result.ArtifactIDs, &completed
	job.Status, job.UpdatedAt = result.Status, completed
	_ = b.journal.SaveJobAndRun(context.Background(), job, run)
	if b.onResult != nil {
		callbackCtx, callbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
		callbackErr := b.onResult(callbackCtx, job, result)
		callbackCancel()
		if callbackErr != nil {
			completed = time.Now().UTC()
			result.Status, result.Exit.Reason = domain.RunFailed, "post-run evidence ingestion: "+callbackErr.Error()
			run.Status, run.Exit, run.CompletedAt = result.Status, result.Exit, &completed
			job.Status, job.UpdatedAt = result.Status, completed
			_ = b.journal.SaveJobAndRun(context.Background(), job, run)
		}
	}
	b.publish(result)
}

func (b *Broker) finishFailed(job domain.WorkerJob, run domain.ExperimentRun, failure error) {
	now := time.Now().UTC()
	job.Status, job.UpdatedAt = domain.RunFailed, now
	run.Status, run.CompletedAt, run.Exit.Reason = domain.RunFailed, &now, failure.Error()
	_ = b.journal.SaveJobAndRun(context.Background(), job, run)
	b.publish(resultFromRun(run))
}

func (b *Broker) publish(result domain.RunResult) {
	b.mu.Lock()
	waiters := b.waiters[result.RunID]
	delete(b.waiters, result.RunID)
	b.mu.Unlock()
	for _, waiter := range waiters {
		waiter <- result
		close(waiter)
	}
}

func (b *Broker) removeWaiter(runID string, target chan domain.RunResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	waiters := b.waiters[runID]
	for index, waiter := range waiters {
		if waiter == target {
			waiters = append(waiters[:index], waiters[index+1:]...)
			break
		}
	}
	if len(waiters) == 0 {
		delete(b.waiters, runID)
	} else {
		b.waiters[runID] = waiters
	}
}

func resultFromRun(run domain.ExperimentRun) domain.RunResult {
	return domain.RunResult{SchemaVersion: 1, RunID: run.ID, Operation: run.Operation, Status: run.Status, Exit: run.Exit,
		ArtifactIDs: run.ArtifactIDs, ResourceUsage: run.Usage, IsolationAssurance: run.IsolationAssurance, AuditCorrelationID: run.AuditCorrelationID}
}

func terminalRun(status domain.RunStatus) bool {
	return status == domain.RunCompleted || status == domain.RunFailed || status == domain.RunCancelled || status == domain.RunTimedOut
}

func validateJob(job domain.WorkerJob) error {
	if job.CampaignID == "" || job.ScopeID == "" || job.Operation == "" || job.AuditCorrelationID == "" {
		return fmt.Errorf("%w: campaign, scope, operation, and correlation required", ErrInvalidJob)
	}
	if !domain.IsKnownOperation(job.Operation) {
		return fmt.Errorf("%w: unknown operation", ErrInvalidJob)
	}
	if !digestPattern.MatchString(job.ImageDigest) {
		return fmt.Errorf("%w: exact sha256 image digest required", ErrInvalidJob)
	}
	if job.Budget.MaxWallSeconds <= 0 || job.Budget.MaxMemoryBytes <= 0 || job.Budget.MaxPIDs <= 0 || job.Budget.MaxDiskBytes <= 0 {
		return fmt.Errorf("%w: positive wall, memory, pid, and disk limits required", ErrInvalidJob)
	}
	seenNames, seenContainers := map[string]bool{}, map[string]bool{}
	for _, mount := range job.Mounts {
		if mount.Name == "" || seenNames[mount.Name] || seenContainers[mount.ContainerPath] {
			return fmt.Errorf("%w: duplicate or empty mount", ErrInvalidJob)
		}
		seenNames[mount.Name], seenContainers[mount.ContainerPath] = true, true
		if !filepath.IsAbs(mount.HostPath) || !filepath.IsAbs(mount.ContainerPath) || mount.ContainerPath == "/" || strings.Contains(mount.HostPath, ",") || strings.Contains(mount.ContainerPath, ",") {
			return fmt.Errorf("%w: mounts require absolute non-root paths", ErrInvalidJob)
		}
		info, err := os.Stat(mount.HostPath)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: mount source must be an existing directory", ErrInvalidJob)
		}
		if mount.Name != "corpus" && !mount.ReadOnly {
			return fmt.Errorf("%w: only corpus mounts may be writable", ErrInvalidJob)
		}
	}
	for _, rule := range job.ArtifactRules {
		if rule.Role == "" || rule.MediaType == "" || rule.Glob == "" || rule.MaxCount <= 0 || rule.MaxBytes <= 0 {
			return fmt.Errorf("%w: complete positive artifact rule required", ErrInvalidJob)
		}
		if _, err := filepath.Match(filepath.ToSlash(rule.Glob), "fixture"); err != nil || filepath.IsAbs(rule.Glob) || strings.Contains(rule.Glob, "..") {
			return fmt.Errorf("%w: invalid artifact glob", ErrInvalidJob)
		}
	}
	for key, value := range job.Environment {
		if !safeEnvironmentKey(key) || strings.ContainsRune(value, 0) || len(value) > 4096 {
			return fmt.Errorf("%w: invalid environment allowlist", ErrInvalidJob)
		}
	}
	return nil
}

func safeEnvironmentKey(key string) bool {
	switch key {
	case "ASAN_OPTIONS", "UBSAN_OPTIONS", "MSAN_OPTIONS":
		return true
	default:
		return false
	}
}

func safeID(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, id)
}

func newID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
