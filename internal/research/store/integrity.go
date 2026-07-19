package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IntegrityReport is machine-readable evidence from an offline custody-store
// verification. PlaintextBytes is derived from authenticated metadata and each
// corresponding blob is fully decrypted and hashed before the report succeeds.
type IntegrityReport struct {
	SchemaVersion   int       `json:"schema_version"`
	VerifiedAt      time.Time `json:"verified_at"`
	Database        string    `json:"database"`
	ForeignKeys     string    `json:"foreign_keys"`
	AuditEvents     int64     `json:"audit_events"`
	ActiveArtifacts int64     `json:"active_artifacts"`
	ActiveBlobs     int64     `json:"active_blobs"`
	PlaintextBytes  int64     `json:"plaintext_bytes"`
	OrphanBlobs     int64     `json:"orphan_blobs"`
}

// VerifyIntegrity performs an exhaustive, read-only logical verification of
// metadata, referential integrity, the audit chain, and active artifact bytes.
// Operators should run it against an isolated restored copy, with every
// historical artifact key supplied, before accepting a recovery exercise.
func (s *Store) VerifyIntegrity(ctx context.Context) (IntegrityReport, error) {
	report := IntegrityReport{SchemaVersion: 1, VerifiedAt: time.Now().UTC()}
	if err := s.verifySQLiteIntegrity(ctx); err != nil {
		return report, err
	}
	report.Database = "ok"
	if err := s.verifyForeignKeys(ctx); err != nil {
		return report, err
	}
	report.ForeignKeys = "ok"
	if err := s.verifyStoredJSON(ctx); err != nil {
		return report, err
	}
	if err := s.VerifyAuditChain(ctx); err != nil {
		return report, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&report.AuditEvents); err != nil {
		return report, fmt.Errorf("research store: count audit events: %w", err)
	}

	s.artifactMu.RLock()
	defer s.artifactMu.RUnlock()
	blobs, err := s.listArtifactBlobs(ctx)
	if err != nil {
		return report, fmt.Errorf("research store: enumerate artifact blobs: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE COALESCE(json_extract(data, '$.purged_at'), '') = ''`).Scan(&report.ActiveArtifacts); err != nil {
		return report, fmt.Errorf("research store: count active artifacts: %w", err)
	}
	referenced := make(map[string]bool, len(blobs))
	for _, blob := range blobs {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		path, err := s.resolveStoredBlob(blob)
		if err != nil {
			return report, fmt.Errorf("research store: resolve active artifact %s: %w", blob.contentID, err)
		}
		if _, _, err := s.verifyArtifactPath(path, blob.size, blob.contentID); err != nil {
			return report, fmt.Errorf("research store: verify active artifact %s: %w", blob.contentID, err)
		}
		referenced[filepath.Clean(path)] = true
		report.ActiveBlobs++
		if blob.size > 0 && report.PlaintextBytes > math.MaxInt64-blob.size {
			return report, errors.New("research store: artifact byte count overflow")
		}
		report.PlaintextBytes += blob.size
	}
	orphans, err := s.verifyArtifactBlobTree(ctx, referenced)
	if err != nil {
		return report, err
	}
	report.OrphanBlobs = orphans
	if orphans != 0 {
		return report, fmt.Errorf("research store: found %d unreferenced artifact blobs", orphans)
	}
	return report, nil
}

func (s *Store) verifySQLiteIntegrity(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("research store: database integrity check: %w", err)
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		seen = true
		if result != "ok" {
			return fmt.Errorf("research store: database integrity check failed: %s", boundedIntegrityMessage(result))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !seen {
		return errors.New("research store: database integrity check returned no result")
	}
	return nil
}

func (s *Store) verifyForeignKeys(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("research store: foreign-key check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("research store: foreign-key check found a violation")
	}
	return rows.Err()
}

func (s *Store) verifyStoredJSON(ctx context.Context) error {
	checks := []struct {
		label string
		query string
	}{
		{"authorization scopes", `SELECT COUNT(*) FROM authorization_scopes WHERE NOT json_valid(data)`},
		{"campaigns", `SELECT COUNT(*) FROM campaigns WHERE NOT json_valid(data)`},
		{"research records", `SELECT COUNT(*) FROM research_records WHERE NOT json_valid(data)`},
		{"artifacts", `SELECT COUNT(*) FROM artifacts WHERE NOT json_valid(data)`},
		{"audit details", `SELECT COUNT(*) FROM audit_events WHERE NOT json_valid(details_json)`},
	}
	for _, check := range checks {
		var invalid int64
		if err := s.db.QueryRowContext(ctx, check.query).Scan(&invalid); err != nil {
			return fmt.Errorf("research store: validate %s JSON: %w", check.label, err)
		}
		if invalid != 0 {
			return fmt.Errorf("research store: %s contain %d invalid JSON records", check.label, invalid)
		}
	}
	return nil
}

func (s *Store) verifyArtifactBlobTree(ctx context.Context, referenced map[string]bool) (int64, error) {
	root := filepath.Join(s.artifactRoot, "blobs")
	var orphans int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("research store: artifact blob tree contains symlink %q", filepath.Base(path))
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("research store: artifact blob escaped custody root")
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if entry.IsDir() {
			if path != root && (len(parts) != 1 || len(entry.Name()) != 2 || strings.ToLower(entry.Name()) != entry.Name() || !isHex(entry.Name())) {
				return fmt.Errorf("research store: invalid artifact shard directory %q", entry.Name())
			}
			return nil
		}
		if len(parts) != 2 {
			return fmt.Errorf("research store: invalid artifact blob depth %q", relative)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("research store: artifact blob %q is not a private regular file", entry.Name())
		}
		parent := filepath.Base(filepath.Dir(path))
		if len(entry.Name()) != 64 || !strings.HasPrefix(entry.Name(), parent) || strings.ToLower(entry.Name()) != entry.Name() || !isHex(entry.Name()) {
			return fmt.Errorf("research store: invalid artifact blob path %q", entry.Name())
		}
		if !referenced[filepath.Clean(path)] {
			orphans++
		}
		return nil
	})
	if err != nil {
		return orphans, err
	}
	return orphans, nil
}

func isHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return value != ""
}

func boundedIntegrityMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
