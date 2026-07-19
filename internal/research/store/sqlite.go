// Package store persists research metadata and content-addressed evidence.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	_ "modernc.org/sqlite"
)

const defaultArtifactLimit int64 = 256 << 20

var (
	// ErrNotFound identifies a missing durable research object.
	ErrNotFound = errors.New("research store: object not found")
	// ErrVersionConflict identifies a stale aggregate update.
	ErrVersionConflict = errors.New("research store: version conflict")
	// ErrArtifactTooLarge identifies evidence exceeding the ingestion limit.
	ErrArtifactTooLarge = errors.New("research store: artifact exceeds size limit")
)

// Config controls the private on-disk research store.
type Config struct {
	Root             string
	MaxArtifactBytes int64
}

// Store owns the metadata database and artifact directory.
type Store struct {
	db               *sql.DB
	root             string
	artifactRoot     string
	maxArtifactBytes int64
}

// Open initializes or migrates a private research store.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("research store: root required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("research store: resolve root: %w", err)
	}
	if err := privateDir(root); err != nil {
		return nil, err
	}
	artifactRoot := filepath.Join(root, "artifacts")
	if err := privateDir(artifactRoot); err != nil {
		return nil, err
	}
	if err := privateDir(filepath.Join(artifactRoot, "blobs")); err != nil {
		return nil, err
	}
	if err := privateDir(filepath.Join(artifactRoot, "tmp")); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(root, "research.sqlite")
	dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("research store: open database: %w", err)
	}
	// A single embedded writer makes audit sequencing and aggregate updates
	// deterministic while WAL still permits SQLite's internal read snapshots.
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("research store: ping database: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("research store: protect database: %w", err)
	}

	s := &Store{db: db, root: root, artifactRoot: artifactRoot, maxArtifactBytes: cfg.MaxArtifactBytes}
	if s.maxArtifactBytes <= 0 {
		s.maxArtifactBytes = defaultArtifactLimit
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("research store: create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("research store: protect %s: %w", path, err)
	}
	return nil
}

// Close flushes and closes the metadata database.
func (s *Store) Close() error { return s.db.Close() }

// Root returns the absolute private store root.
func (s *Store) Root() string { return s.root }

// Database exposes the handle for health checks and administrative verification.
func (s *Store) Database() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("research store: begin migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS authorization_scopes (
			id TEXT PRIMARY KEY,
			operator_id TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			revoked_at TEXT,
			data BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS campaigns (
			id TEXT PRIMARY KEY,
			scope_id TEXT NOT NULL REFERENCES authorization_scopes(id),
			state TEXT NOT NULL,
			version INTEGER NOT NULL CHECK(version > 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			data BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS campaigns_scope_state ON campaigns(scope_id, state)`,
		`CREATE TABLE IF NOT EXISTS research_records (
			kind TEXT NOT NULL,
			id TEXT NOT NULL,
			campaign_id TEXT REFERENCES campaigns(id),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			data BLOB NOT NULL,
			PRIMARY KEY(kind, id)
		)`,
		`CREATE INDEX IF NOT EXISTS research_records_campaign_kind ON research_records(campaign_id, kind)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY,
			content_id TEXT NOT NULL,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id),
			run_id TEXT,
			role TEXT NOT NULL,
			size INTEGER NOT NULL CHECK(size >= 0),
			storage_path TEXT NOT NULL,
			created_at TEXT NOT NULL,
			data BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS artifacts_content ON artifacts(content_id)`,
		`CREATE INDEX IF NOT EXISTS artifacts_campaign_run ON artifacts(campaign_id, run_id)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			previous_hash TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			actor_id TEXT NOT NULL,
			action TEXT NOT NULL,
			resource_type TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			correlation_id TEXT,
			decision TEXT,
			details_json BLOB NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("research store: migration: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, timestamp(time.Now().UTC())); err != nil {
		return fmt.Errorf("research store: record migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("research store: commit migration: %w", err)
	}
	return nil
}

// CreateScope persists a validated authorization envelope.
func (s *Store) CreateScope(ctx context.Context, scope domain.AuthorizationScope) error {
	validationTime := scope.CreatedAt
	if validationTime.IsZero() {
		validationTime = time.Now().UTC()
	}
	if err := scope.Validate(validationTime); err != nil {
		return err
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	var revoked any
	if scope.RevokedAt != nil {
		revoked = timestamp(*scope.RevokedAt)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO authorization_scopes(id, operator_id, expires_at, revoked_at, data) VALUES(?, ?, ?, ?, ?)`,
		scope.ID, scope.OperatorID, timestamp(scope.ExpiresAt), revoked, data)
	return translateConstraint("create scope", err)
}

// GetScope loads an authorization envelope by ID.
func (s *Store) GetScope(ctx context.Context, id string) (domain.AuthorizationScope, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM authorization_scopes WHERE id = ?`, id).Scan(&data)
	var scope domain.AuthorizationScope
	if err := decodeOne(err, data, &scope); err != nil {
		return scope, err
	}
	return scope, nil
}

// RevokeScope marks a scope revoked; it cannot be unrevoked.
func (s *Store) RevokeScope(ctx context.Context, id string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM authorization_scopes WHERE id = ?`, id).Scan(&data); err != nil {
		return decodeOne(err, nil, nil)
	}
	var scope domain.AuthorizationScope
	if err := json.Unmarshal(data, &scope); err != nil {
		return err
	}
	if scope.RevokedAt == nil || at.Before(*scope.RevokedAt) {
		scope.RevokedAt = &at
	}
	data, err = json.Marshal(scope)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authorization_scopes SET revoked_at = ?, data = ? WHERE id = ?`, timestamp(*scope.RevokedAt), data, id); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateCampaign persists a new aggregate at version one.
func (s *Store) CreateCampaign(ctx context.Context, campaign domain.Campaign) (domain.Campaign, error) {
	if campaign.ID == "" || campaign.ScopeID == "" || campaign.Name == "" {
		return campaign, errors.New("research store: campaign id, scope, and name required")
	}
	now := time.Now().UTC()
	if campaign.State == "" {
		campaign.State = domain.CampaignDraft
	}
	if campaign.Version == 0 {
		campaign.Version = 1
	}
	if campaign.Version != 1 {
		return campaign, errors.New("research store: new campaign must be version one")
	}
	if campaign.CreatedAt.IsZero() {
		campaign.CreatedAt = now
	}
	if campaign.UpdatedAt.IsZero() {
		campaign.UpdatedAt = campaign.CreatedAt
	}
	data, err := json.Marshal(campaign)
	if err != nil {
		return campaign, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO campaigns(id, scope_id, state, version, created_at, updated_at, data) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		campaign.ID, campaign.ScopeID, campaign.State, campaign.Version, timestamp(campaign.CreatedAt), timestamp(campaign.UpdatedAt), data)
	if err := translateConstraint("create campaign", err); err != nil {
		return campaign, err
	}
	return campaign, nil
}

// GetCampaign loads a campaign aggregate.
func (s *Store) GetCampaign(ctx context.Context, id string) (domain.Campaign, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM campaigns WHERE id = ?`, id).Scan(&data)
	var campaign domain.Campaign
	if err := decodeOne(err, data, &campaign); err != nil {
		return campaign, err
	}
	return campaign, nil
}

// UpdateCampaign commits exactly one optimistic state/version update.
func (s *Store) UpdateCampaign(ctx context.Context, campaign domain.Campaign, expectedVersion int64) error {
	if campaign.Version != expectedVersion+1 {
		return fmt.Errorf("%w: expected next version %d, got %d", ErrVersionConflict, expectedVersion+1, campaign.Version)
	}
	if campaign.UpdatedAt.IsZero() {
		campaign.UpdatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(campaign)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE campaigns SET state = ?, version = ?, updated_at = ?, data = ? WHERE id = ? AND version = ?`,
		campaign.State, campaign.Version, timestamp(campaign.UpdatedAt), data, campaign.ID, expectedVersion)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrVersionConflict
	}
	return nil
}

// ListCampaigns returns stable newest-first campaign snapshots.
func (s *Store) ListCampaigns(ctx context.Context, limit int) ([]domain.Campaign, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM campaigns ORDER BY created_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Campaign
	for rows.Next() {
		var data []byte
		var campaign domain.Campaign
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &campaign); err != nil {
			return nil, err
		}
		result = append(result, campaign)
	}
	return result, rows.Err()
}

// ListScopes returns bounded newest-expiry-first authorization envelopes.
func (s *Store) ListScopes(ctx context.Context, limit int) ([]domain.AuthorizationScope, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM authorization_scopes ORDER BY expires_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.AuthorizationScope
	for rows.Next() {
		var data []byte
		var scope domain.AuthorizationScope
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &scope); err != nil {
			return nil, err
		}
		result = append(result, scope)
	}
	return result, rows.Err()
}

type recordKind string

const (
	recordTarget         recordKind = "target_revision"
	recordApparatus      recordKind = "apparatus_manifest"
	recordBuild          recordKind = "build"
	recordRun            recordKind = "experiment_run"
	recordCrash          recordKind = "crash_observation"
	recordCrashGroup     recordKind = "crash_group"
	recordPrimitive      recordKind = "primitive_assessment"
	recordFinding        recordKind = "finding"
	recordApproval       recordKind = "approval"
	recordWorkerJob      recordKind = "worker_job"
	recordSourceEvidence recordKind = "source_evidence"
	recordSourceReview   recordKind = "source_review"
	recordRevisionCheck  recordKind = "revision_check"
	recordRemediation    recordKind = "remediation_validation"
)

func (s *Store) putRecord(ctx context.Context, kind recordKind, id, campaignID string, created, updated time.Time, value any) error {
	if id == "" {
		return errors.New("research store: record id required")
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if updated.IsZero() {
		updated = created
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO research_records(kind, id, campaign_id, created_at, updated_at, data)
		VALUES(?, ?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(kind, id) DO UPDATE SET campaign_id=excluded.campaign_id, updated_at=excluded.updated_at, data=excluded.data`,
		kind, id, campaignID, timestamp(created), timestamp(updated), data)
	return translateConstraint("save "+string(kind), err)
}

func (s *Store) putImmutableRecord(ctx context.Context, kind recordKind, id, campaignID string, created time.Time, value any) error {
	if id == "" {
		return errors.New("research store: record id required")
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO research_records(kind, id, campaign_id, created_at, updated_at, data)
		VALUES(?, ?, NULLIF(?, ''), ?, ?, ?)`, kind, id, campaignID, timestamp(created), timestamp(created), data)
	return translateConstraint("save immutable "+string(kind), err)
}

func (s *Store) getRecord(ctx context.Context, kind recordKind, id string, value any) error {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM research_records WHERE kind = ? AND id = ?`, kind, id).Scan(&data)
	return decodeOne(err, data, value)
}

func (s *Store) listRecordData(ctx context.Context, kind recordKind, campaignID string, limit int) ([][]byte, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM research_records WHERE kind = ? AND campaign_id = ? ORDER BY created_at DESC, id LIMIT ?`, kind, campaignID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][]byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		result = append(result, append([]byte(nil), data...))
	}
	return result, rows.Err()
}

func decodeRecords[T any](rows [][]byte) ([]T, error) {
	result := make([]T, 0, len(rows))
	for _, data := range rows {
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) ListRuns(ctx context.Context, campaignID string, limit int) ([]domain.ExperimentRun, error) {
	rows, err := s.listRecordData(ctx, recordRun, campaignID, limit)
	if err != nil {
		return nil, err
	}
	return decodeRecords[domain.ExperimentRun](rows)
}

func (s *Store) ListApprovals(ctx context.Context, campaignID string, limit int) ([]domain.Approval, error) {
	rows, err := s.listRecordData(ctx, recordApproval, campaignID, limit)
	if err != nil {
		return nil, err
	}
	return decodeRecords[domain.Approval](rows)
}

func (s *Store) ListCrashGroups(ctx context.Context, campaignID string, limit int) ([]domain.CrashGroup, error) {
	rows, err := s.listRecordData(ctx, recordCrashGroup, campaignID, limit)
	if err != nil {
		return nil, err
	}
	return decodeRecords[domain.CrashGroup](rows)
}

func (s *Store) ListFindings(ctx context.Context, campaignID string, limit int) ([]domain.Finding, error) {
	rows, err := s.listRecordData(ctx, recordFinding, campaignID, limit)
	if err != nil {
		return nil, err
	}
	return decodeRecords[domain.Finding](rows)
}

func (s *Store) ListArtifacts(ctx context.Context, campaignID string, limit int) ([]domain.Artifact, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM artifacts WHERE campaign_id = ? ORDER BY created_at DESC, id LIMIT ?`, campaignID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Artifact
	for rows.Next() {
		var data []byte
		var artifact domain.Artifact
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &artifact); err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

// SaveTarget persists immutable target identity metadata.
func (s *Store) SaveTarget(ctx context.Context, v domain.TargetRevision) error {
	return s.putImmutableRecord(ctx, recordTarget, v.ID, v.CampaignID, v.AcquiredAt, v)
}

func (s *Store) GetTarget(ctx context.Context, id string) (v domain.TargetRevision, err error) {
	err = s.getRecord(ctx, recordTarget, id, &v)
	return
}

// SaveApparatus persists a versioned apparatus manifest.
func (s *Store) SaveApparatus(ctx context.Context, v domain.ApparatusManifest) error {
	return s.putImmutableRecord(ctx, recordApparatus, v.ID, "", time.Now().UTC(), v)
}

func (s *Store) GetApparatus(ctx context.Context, id string) (v domain.ApparatusManifest, err error) {
	err = s.getRecord(ctx, recordApparatus, id, &v)
	return
}

func (s *Store) ListApparatus(ctx context.Context, limit int) ([]domain.ApparatusManifest, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM research_records WHERE kind = ? ORDER BY created_at DESC, id LIMIT ?`, recordApparatus, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ApparatusManifest
	for rows.Next() {
		var data []byte
		var manifest domain.ApparatusManifest
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, err
		}
		result = append(result, manifest)
	}
	return result, rows.Err()
}

func (s *Store) SaveBuild(ctx context.Context, v domain.Build) error {
	updated := v.CreatedAt
	if v.CompletedAt != nil {
		updated = *v.CompletedAt
	}
	return s.putRecord(ctx, recordBuild, v.ID, v.CampaignID, v.CreatedAt, updated, v)
}

func (s *Store) GetBuild(ctx context.Context, id string) (v domain.Build, err error) {
	err = s.getRecord(ctx, recordBuild, id, &v)
	return
}

func (s *Store) SaveRun(ctx context.Context, v domain.ExperimentRun) error {
	updated := v.CreatedAt
	if v.CompletedAt != nil {
		updated = *v.CompletedAt
	} else if v.StartedAt != nil {
		updated = *v.StartedAt
	}
	return s.putRecord(ctx, recordRun, v.ID, v.CampaignID, v.CreatedAt, updated, v)
}

func (s *Store) GetRun(ctx context.Context, id string) (v domain.ExperimentRun, err error) {
	err = s.getRecord(ctx, recordRun, id, &v)
	return
}

func (s *Store) SaveCrash(ctx context.Context, v domain.CrashObservation) error {
	return s.putRecord(ctx, recordCrash, v.ID, v.CampaignID, v.CreatedAt, v.CreatedAt, v)
}

func (s *Store) GetCrash(ctx context.Context, id string) (v domain.CrashObservation, err error) {
	err = s.getRecord(ctx, recordCrash, id, &v)
	return
}

func (s *Store) SaveCrashGroup(ctx context.Context, v domain.CrashGroup) error {
	return s.putRecord(ctx, recordCrashGroup, v.ID, v.CampaignID, v.CreatedAt, v.UpdatedAt, v)
}

func (s *Store) GetCrashGroup(ctx context.Context, id string) (v domain.CrashGroup, err error) {
	err = s.getRecord(ctx, recordCrashGroup, id, &v)
	return
}

func (s *Store) SavePrimitive(ctx context.Context, v domain.PrimitiveAssessment) error {
	if err := domain.ValidatePrimitive(v); err != nil {
		return err
	}
	updated := v.CreatedAt
	if v.ReviewedAt != nil {
		updated = *v.ReviewedAt
	}
	return s.putRecord(ctx, recordPrimitive, v.ID, v.CampaignID, v.CreatedAt, updated, v)
}

func (s *Store) GetPrimitive(ctx context.Context, id string) (v domain.PrimitiveAssessment, err error) {
	err = s.getRecord(ctx, recordPrimitive, id, &v)
	return
}

func (s *Store) SaveFinding(ctx context.Context, v domain.Finding) error {
	return s.putRecord(ctx, recordFinding, v.ID, v.CampaignID, v.CreatedAt, v.UpdatedAt, v)
}

func (s *Store) GetFinding(ctx context.Context, id string) (v domain.Finding, err error) {
	err = s.getRecord(ctx, recordFinding, id, &v)
	return
}

func (s *Store) SaveApproval(ctx context.Context, v domain.Approval) error {
	updated := v.CreatedAt
	if v.DecidedAt != nil {
		updated = *v.DecidedAt
	}
	return s.putRecord(ctx, recordApproval, v.ID, v.CampaignID, v.CreatedAt, updated, v)
}

func (s *Store) GetApproval(ctx context.Context, id string) (v domain.Approval, err error) {
	err = s.getRecord(ctx, recordApproval, id, &v)
	return
}

func (s *Store) SaveWorkerJob(ctx context.Context, v domain.WorkerJob) error {
	return s.putRecord(ctx, recordWorkerJob, v.ID, v.CampaignID, v.CreatedAt, v.UpdatedAt, v)
}

func (s *Store) GetWorkerJob(ctx context.Context, id string) (v domain.WorkerJob, err error) {
	err = s.getRecord(ctx, recordWorkerJob, id, &v)
	return
}

func (s *Store) SaveSourceEvidence(ctx context.Context, v domain.SourceEvidence) error {
	return s.putImmutableRecord(ctx, recordSourceEvidence, v.ID, v.CampaignID, v.CheckedAt, v)
}

func (s *Store) GetSourceEvidence(ctx context.Context, id string) (v domain.SourceEvidence, err error) {
	err = s.getRecord(ctx, recordSourceEvidence, id, &v)
	return
}

func (s *Store) SaveSourceReview(ctx context.Context, v domain.SourceReview) error {
	return s.putImmutableRecord(ctx, recordSourceReview, v.ID, v.CampaignID, v.ReviewedAt, v)
}

func (s *Store) GetSourceReview(ctx context.Context, id string) (v domain.SourceReview, err error) {
	err = s.getRecord(ctx, recordSourceReview, id, &v)
	return
}

func (s *Store) SaveRevisionCheck(ctx context.Context, v domain.RevisionCheck) error {
	return s.putImmutableRecord(ctx, recordRevisionCheck, v.ID, v.CampaignID, v.CheckedAt, v)
}

func (s *Store) GetRevisionCheck(ctx context.Context, id string) (v domain.RevisionCheck, err error) {
	err = s.getRecord(ctx, recordRevisionCheck, id, &v)
	return
}

func (s *Store) SaveRemediation(ctx context.Context, v domain.RemediationValidation) error {
	return s.putImmutableRecord(ctx, recordRemediation, v.ID, v.CampaignID, v.ValidatedAt, v)
}

func (s *Store) GetRemediation(ctx context.Context, id string) (v domain.RemediationValidation, err error) {
	err = s.getRecord(ctx, recordRemediation, id, &v)
	return
}

// CreateJobAndRun atomically creates the queue envelope and public run record.
func (s *Store) CreateJobAndRun(ctx context.Context, job domain.WorkerJob, run domain.ExperimentRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertRecordTx(ctx, tx, recordRun, run.ID, run.CampaignID, run.CreatedAt, run.CreatedAt, run); err != nil {
		return err
	}
	if err := insertRecordTx(ctx, tx, recordWorkerJob, job.ID, job.CampaignID, job.CreatedAt, job.UpdatedAt, job); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveJobAndRun atomically checkpoints a worker lifecycle state.
func (s *Store) SaveJobAndRun(ctx context.Context, job domain.WorkerJob, run domain.ExperimentRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	runUpdated := run.CreatedAt
	if run.CompletedAt != nil {
		runUpdated = *run.CompletedAt
	} else if run.StartedAt != nil {
		runUpdated = *run.StartedAt
	}
	if err := upsertRecordTx(ctx, tx, recordRun, run.ID, run.CampaignID, run.CreatedAt, runUpdated, run); err != nil {
		return err
	}
	if err := upsertRecordTx(ctx, tx, recordWorkerJob, job.ID, job.CampaignID, job.CreatedAt, job.UpdatedAt, job); err != nil {
		return err
	}
	return tx.Commit()
}

func insertRecordTx(ctx context.Context, tx *sql.Tx, kind recordKind, id, campaignID string, created, updated time.Time, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_records(kind, id, campaign_id, created_at, updated_at, data)
		VALUES(?, ?, NULLIF(?, ''), ?, ?, ?)`, kind, id, campaignID, timestamp(created), timestamp(updated), data)
	return translateConstraint("create "+string(kind), err)
}

func upsertRecordTx(ctx context.Context, tx *sql.Tx, kind recordKind, id, campaignID string, created, updated time.Time, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_records(kind, id, campaign_id, created_at, updated_at, data)
		VALUES(?, ?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(kind, id) DO UPDATE SET campaign_id=excluded.campaign_id, updated_at=excluded.updated_at, data=excluded.data`,
		kind, id, campaignID, timestamp(created), timestamp(updated), data)
	return translateConstraint("checkpoint "+string(kind), err)
}

// ListWorkerJobsByStatus supports queue recovery after a control-plane restart.
func (s *Store) ListWorkerJobsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]domain.WorkerJob, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	arguments := make([]any, 0, len(statuses)+1)
	arguments = append(arguments, recordWorkerJob)
	for index, status := range statuses {
		placeholders[index] = "?"
		arguments = append(arguments, status)
	}
	query := `SELECT data FROM research_records WHERE kind = ? AND json_extract(data, '$.status') IN (` + strings.Join(placeholders, ",") + `) ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []domain.WorkerJob
	for rows.Next() {
		var data []byte
		var job domain.WorkerJob
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// PutArtifact streams evidence into the bounded content-addressed store.
func (s *Store) PutArtifact(ctx context.Context, metadata domain.Artifact, source io.Reader) (domain.Artifact, error) {
	if metadata.ID == "" {
		metadata.ID = newID("artifact")
	}
	if metadata.CampaignID == "" || metadata.Role == "" || metadata.MediaType == "" {
		return metadata, errors.New("research store: artifact campaign, role, and media type required")
	}
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	}
	tmp, err := os.CreateTemp(filepath.Join(s.artifactRoot, "tmp"), ".ingest-*")
	if err != nil {
		return metadata, fmt.Errorf("research store: create artifact temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return metadata, err
	}
	h := sha256.New()
	written, copyErr := copyBounded(tmp, h, source, s.maxArtifactBytes)
	if copyErr != nil {
		tmp.Close()
		return metadata, copyErr
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return metadata, err
	}
	if err := tmp.Close(); err != nil {
		return metadata, err
	}
	hexDigest := hex.EncodeToString(h.Sum(nil))
	metadata.ContentID = "sha256:" + hexDigest
	metadata.Size = written
	relative := filepath.Join("blobs", hexDigest[:2], hexDigest)
	destinationDir := filepath.Join(s.artifactRoot, "blobs", hexDigest[:2])
	if err := privateDir(destinationDir); err != nil {
		return metadata, err
	}
	destination := filepath.Join(s.artifactRoot, relative)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(tmpName, destination); err != nil {
			return metadata, fmt.Errorf("research store: commit artifact: %w", err)
		}
		if err := os.Chmod(destination, 0o600); err != nil {
			return metadata, err
		}
	} else if err != nil {
		return metadata, err
	}
	metadata.StoragePath = filepath.ToSlash(relative)
	data, err := json.Marshal(metadata)
	if err != nil {
		return metadata, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artifacts(id, content_id, campaign_id, run_id, role, size, storage_path, created_at, data)
		VALUES(?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`, metadata.ID, metadata.ContentID, metadata.CampaignID,
		metadata.RunID, metadata.Role, metadata.Size, metadata.StoragePath, timestamp(metadata.CreatedAt), data)
	if err := translateConstraint("save artifact", err); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func copyBounded(file *os.File, digest hash.Hash, source io.Reader, limit int64) (int64, error) {
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, limit+1))
	if err != nil {
		return written, fmt.Errorf("research store: ingest artifact: %w", err)
	}
	if written > limit {
		return written, ErrArtifactTooLarge
	}
	return written, nil
}

// GetArtifact loads immutable artifact metadata.
func (s *Store) GetArtifact(ctx context.Context, id string) (domain.Artifact, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM artifacts WHERE id = ?`, id).Scan(&data)
	var artifact domain.Artifact
	if err := decodeOne(err, data, &artifact); err != nil {
		return artifact, err
	}
	return artifact, nil
}

// OpenArtifact returns a verified regular evidence object. The caller closes it.
func (s *Store) OpenArtifact(ctx context.Context, id string) (domain.Artifact, *os.File, error) {
	metadata, err := s.GetArtifact(ctx, id)
	if err != nil {
		return metadata, nil, err
	}
	expectedPrefix := "sha256:"
	if !strings.HasPrefix(metadata.ContentID, expectedPrefix) || len(metadata.ContentID) != len(expectedPrefix)+sha256.Size*2 {
		return metadata, nil, errors.New("research store: invalid artifact content id")
	}
	hexDigest := strings.TrimPrefix(metadata.ContentID, expectedPrefix)
	expectedRelative := filepath.Join("blobs", hexDigest[:2], hexDigest)
	if filepath.Clean(filepath.FromSlash(metadata.StoragePath)) != expectedRelative {
		return metadata, nil, errors.New("research store: artifact path does not match content id")
	}
	path := filepath.Join(s.artifactRoot, expectedRelative)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return metadata, nil, err
	}
	rel, err := filepath.Rel(s.artifactRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return metadata, nil, errors.New("research store: artifact escaped storage root")
	}
	file, err := os.Open(real)
	if err != nil {
		return metadata, nil, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != metadata.Size {
		file.Close()
		return metadata, nil, errors.New("research store: artifact metadata mismatch")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		file.Close()
		return metadata, nil, fmt.Errorf("research store: verify artifact: %w", err)
	}
	actual := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if actual != metadata.ContentID {
		file.Close()
		return metadata, nil, errors.New("research store: artifact content hash mismatch")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return metadata, nil, err
	}
	return metadata, file, nil
}

// AppendAudit adds one tamper-evident event to the global event chain.
func (s *Store) AppendAudit(ctx context.Context, event domain.AuditEvent) (domain.AuditEvent, error) {
	if event.ID == "" {
		event.ID = newID("audit")
	}
	if event.ActorID == "" || event.Action == "" || event.ResourceType == "" || event.ResourceID == "" {
		return event, errors.New("research store: audit actor, action, resource type, and resource id required")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	var sequence int64
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT sequence, hash FROM audit_events ORDER BY sequence DESC LIMIT 1`).Scan(&sequence, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return event, err
	}
	event.Sequence = sequence + 1
	event.PreviousHash = previous
	event.Hash, err = auditHash(event)
	if err != nil {
		return event, err
	}
	details, err := json.Marshal(event.Details)
	if err != nil {
		return event, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(sequence, id, previous_hash, hash, actor_id, action, resource_type, resource_id, correlation_id, decision, details_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?)`, event.Sequence, event.ID, event.PreviousHash, event.Hash,
		event.ActorID, event.Action, event.ResourceType, event.ResourceID, event.CorrelationID, event.Decision, details, timestamp(event.CreatedAt))
	if err != nil {
		return event, err
	}
	if err := tx.Commit(); err != nil {
		return event, err
	}
	return event, nil
}

// ListAudit returns events in chain order.
func (s *Store) ListAudit(ctx context.Context, afterSequence int64, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, id, previous_hash, hash, actor_id, action, resource_type, resource_id,
		COALESCE(correlation_id, ''), COALESCE(decision, ''), details_json, created_at
		FROM audit_events WHERE sequence > ? ORDER BY sequence LIMIT ?`, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.AuditEvent
	for rows.Next() {
		var event domain.AuditEvent
		var details []byte
		var created string
		if err := rows.Scan(&event.Sequence, &event.ID, &event.PreviousHash, &event.Hash, &event.ActorID, &event.Action,
			&event.ResourceType, &event.ResourceID, &event.CorrelationID, &event.Decision, &details, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &event.Details); err != nil {
			return nil, err
		}
		event.SchemaVersion = 1
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// VerifyAuditChain recomputes every link and fails at the first altered event.
func (s *Store) VerifyAuditChain(ctx context.Context) error {
	const pageSize = 100
	after, expectedSequence, previous := int64(0), int64(1), ""
	for {
		events, err := s.ListAudit(ctx, after, pageSize)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Sequence != expectedSequence || event.PreviousHash != previous {
				return fmt.Errorf("research store: audit chain broken at sequence %d", event.Sequence)
			}
			expected, err := auditHash(event)
			if err != nil {
				return err
			}
			if event.Hash != expected {
				return fmt.Errorf("research store: audit hash mismatch at sequence %d", event.Sequence)
			}
			previous, after = event.Hash, event.Sequence
			expectedSequence++
		}
		if len(events) < pageSize {
			return nil
		}
	}
}

func auditHash(event domain.AuditEvent) (string, error) {
	event.Hash = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeOne(queryErr error, data []byte, value any) error {
	if errors.Is(queryErr, sql.ErrNoRows) {
		return ErrNotFound
	}
	if queryErr != nil {
		return queryErr
	}
	if value == nil {
		return nil
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("research store: decode object: %w", err)
	}
	return nil
}

func translateConstraint(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("research store: %s: %w", action, err)
}

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func newID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
