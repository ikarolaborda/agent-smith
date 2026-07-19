package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

const maxPurgeReasonBytes = 1024

// PurgeArtifact creates an irreversible logical tombstone and removes the CAS
// blob only when no unpurged artifact record still references it. Authorization
// and campaign-state checks belong to the service; the store independently
// enforces the minimum custody deadline and records the exact approval.
func (s *Store) PurgeArtifact(ctx context.Context, id, approvalID, reason string, now time.Time) (domain.Artifact, error) {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	artifact, err := s.GetArtifact(ctx, id)
	if err != nil {
		return artifact, err
	}
	reason = strings.TrimSpace(reason)
	if approvalID == "" || reason == "" || len(reason) > maxPurgeReasonBytes {
		return artifact, errors.New("research store: purge approval and a bounded reason are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if artifact.RetainUntil.IsZero() || now.Before(artifact.RetainUntil) {
		return artifact, ErrRetentionActive
	}
	if artifact.PurgedAt == nil {
		path, err := s.validatedArtifactBlobPath(storedArtifactBlob{contentID: artifact.ContentID, storagePath: artifact.StoragePath, size: artifact.Size})
		if err != nil {
			return artifact, err
		}
		if _, _, err := s.verifyArtifactPath(path, artifact.Size, artifact.ContentID); err != nil {
			return artifact, fmt.Errorf("research store: verify artifact before tombstone: %w", err)
		}
		artifact.PurgedAt = &now
		artifact.PurgeApprovalID = approvalID
		artifact.PurgeReason = reason
		data, err := json.Marshal(artifact)
		if err != nil {
			return artifact, err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE artifacts SET data = ?
			WHERE id = ? AND COALESCE(json_extract(data, '$.purged_at'), '') = ''`, data, artifact.ID)
		if err != nil {
			return artifact, fmt.Errorf("research store: create artifact purge tombstone: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return artifact, err
		}
		if changed == 0 {
			artifact, err = s.GetArtifact(ctx, id)
			if err != nil {
				return artifact, err
			}
		}
	}
	if artifact.PurgeApprovalID != approvalID || artifact.PurgeReason != reason {
		return artifact, errors.New("research store: artifact already has a different immutable purge decision")
	}

	var activeReferences int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts
		WHERE content_id = ? AND COALESCE(json_extract(data, '$.purged_at'), '') = ''`, artifact.ContentID).Scan(&activeReferences); err != nil {
		return artifact, err
	}
	if activeReferences == 0 {
		if err := s.finishArtifactBlobPurge(ctx, storedArtifactBlob{contentID: artifact.ContentID, storagePath: artifact.StoragePath, size: artifact.Size}, now); err != nil {
			return artifact, err
		}
	}
	return s.GetArtifact(ctx, artifact.ID)
}

func (s *Store) migrateArtifactRetention(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM artifacts ORDER BY id`)
	if err != nil {
		return err
	}
	type retainedArtifact struct {
		id       string
		artifact domain.Artifact
	}
	var updates []retainedArtifact
	for rows.Next() {
		var id string
		var data []byte
		var artifact domain.Artifact
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(data, &artifact); err != nil {
			rows.Close()
			return err
		}
		if artifact.RetainUntil.IsZero() {
			if artifact.PurgedAt != nil {
				artifact.RetainUntil = *artifact.PurgedAt
			} else {
				artifact.RetainUntil = time.Now().UTC().Add(s.artifactRetention)
			}
			updates = append(updates, retainedArtifact{id: id, artifact: artifact})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		data, err := json.Marshal(update.artifact)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE artifacts SET data = ? WHERE id = ?`, data, update.id); err != nil {
			return fmt.Errorf("research store: backfill artifact retention: %w", err)
		}
	}
	return nil
}

func (s *Store) completeApprovedArtifactPurges(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT content_id, storage_path, size FROM artifacts
		GROUP BY content_id, storage_path, size
		HAVING SUM(CASE WHEN COALESCE(json_extract(data, '$.purged_at'), '') = '' THEN 1 ELSE 0 END) = 0
		ORDER BY content_id`)
	if err != nil {
		return err
	}
	var blobs []storedArtifactBlob
	for rows.Next() {
		var blob storedArtifactBlob
		if err := rows.Scan(&blob.contentID, &blob.storagePath, &blob.size); err != nil {
			rows.Close()
			return err
		}
		blobs = append(blobs, blob)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, blob := range blobs {
		if err := s.finishArtifactBlobPurge(ctx, blob, time.Now().UTC()); err != nil {
			return fmt.Errorf("research store: complete approved artifact purge: %w", err)
		}
	}
	return nil
}

func (s *Store) finishArtifactBlobPurge(ctx context.Context, blob storedArtifactBlob, deletedAt time.Time) error {
	path, err := s.validatedArtifactBlobPath(blob)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if _, _, err := s.verifyArtifactPath(path, blob.size, blob.contentID); err != nil {
			return fmt.Errorf("research store: verify artifact before purge: %w", err)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("research store: remove purged artifact blob: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM artifacts WHERE content_id = ? ORDER BY id`, blob.contentID)
	if err != nil {
		return err
	}
	type tombstoneUpdate struct {
		id       string
		artifact domain.Artifact
	}
	var updates []tombstoneUpdate
	for rows.Next() {
		var id string
		var data []byte
		var artifact domain.Artifact
		if err := rows.Scan(&id, &data); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(data, &artifact); err != nil {
			rows.Close()
			return err
		}
		if artifact.PurgedAt == nil {
			rows.Close()
			return errors.New("research store: active artifact reference appeared during purge")
		}
		if artifact.BlobDeletedAt == nil {
			artifact.BlobDeletedAt = &deletedAt
			updates = append(updates, tombstoneUpdate{id: id, artifact: artifact})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		data, err := json.Marshal(update.artifact)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE artifacts SET data = ? WHERE id = ? AND COALESCE(json_extract(data, '$.purged_at'), '') != ''`, data, update.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validatedArtifactBlobPath(blob storedArtifactBlob) (string, error) {
	if !strings.HasPrefix(blob.contentID, "sha256:") || len(blob.contentID) != len("sha256:")+64 || blob.size < 0 {
		return "", errors.New("research store: invalid stored artifact identity")
	}
	hexDigest := strings.TrimPrefix(blob.contentID, "sha256:")
	if decoded, err := hex.DecodeString(hexDigest); err != nil || len(decoded) != 32 {
		return "", errors.New("research store: invalid stored artifact identity")
	}
	expectedRelative := filepath.Join("blobs", hexDigest[:2], hexDigest)
	if filepath.Clean(filepath.FromSlash(blob.storagePath)) != expectedRelative {
		return "", errors.New("research store: stored artifact path mismatch")
	}
	return filepath.Join(s.artifactRoot, expectedRelative), nil
}
