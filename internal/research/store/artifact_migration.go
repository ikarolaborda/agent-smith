package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type storedArtifactBlob struct {
	contentID   string
	storagePath string
	size        int64
}

func (s *Store) verifyArtifactPath(path string, expectedSize int64, expectedID string) (string, string, error) {
	file, err := openArtifactFile(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	return s.verifyArtifactFile(file, expectedSize, expectedID)
}

func openArtifactFile(path string) (*os.File, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, errors.New("research store: artifact is not a regular stored object")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || !os.SameFile(pathInfo, fileInfo) {
		file.Close()
		return nil, errors.New("research store: artifact changed while opening")
	}
	return file, nil
}

func (s *Store) verifyArtifactFile(file *os.File, expectedSize int64, expectedID string) (string, string, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("research store: artifact is not a regular stored object")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	var prefix [len(artifactMagic)]byte
	_, readErr := io.ReadFull(file, prefix[:])
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if readErr == nil && bytes.Equal(prefix[:], artifactMagic[:]) {
		reader, keyID, err := newEncryptedArtifactReader(file, expectedSize, expectedID, s.artifactKeys)
		if err != nil {
			return "", keyID, err
		}
		_, verifyErr := io.Copy(io.Discard, reader)
		if verifyErr != nil {
			return "", keyID, verifyErr
		}
		return artifactEncryptionScheme, keyID, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if info.Size() != expectedSize {
		return "", "", errors.New("research store: artifact metadata mismatch")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", "", fmt.Errorf("research store: verify artifact: %w", err)
	}
	actual := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if actual != expectedID {
		return "", "", errors.New("research store: artifact content hash mismatch")
	}
	return "", "", nil
}

func (s *Store) migrateArtifactEncryption(ctx context.Context) error {
	blobs, err := s.listArtifactBlobs(ctx)
	if err != nil {
		return err
	}
	if s.activeArtifactKeyID == "" {
		for _, blob := range blobs {
			path, err := s.resolveStoredBlob(blob)
			if err != nil {
				return err
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			var prefix [len(artifactMagic)]byte
			_, readErr := io.ReadFull(file, prefix[:])
			file.Close()
			if readErr == nil && bytes.Equal(prefix[:], artifactMagic[:]) {
				return errors.New("research store: artifact encryption keys required for existing custody")
			}
		}
		return nil
	}
	for _, blob := range blobs {
		path, err := s.resolveStoredBlob(blob)
		if err != nil {
			return err
		}
		scheme, keyID, err := s.verifyArtifactPath(path, blob.size, blob.contentID)
		if err != nil {
			return fmt.Errorf("research store: verify artifact before encryption migration: %w", err)
		}
		if scheme != artifactEncryptionScheme || keyID != s.activeArtifactKeyID {
			if err := s.rotateArtifactBlob(path, blob, scheme); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE artifacts SET data = json_set(data, '$.encryption', ?, '$.encryption_key_id', ?) WHERE content_id = ?`, artifactEncryptionScheme, s.activeArtifactKeyID, blob.contentID); err != nil {
			return fmt.Errorf("research store: update artifact encryption metadata: %w", err)
		}
	}
	return nil
}

func (s *Store) rotateArtifactBlob(path string, blob storedArtifactBlob, currentScheme string) error {
	var source io.ReadCloser
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if currentScheme == artifactEncryptionScheme {
		reader, _, err := newEncryptedArtifactReader(file, blob.size, blob.contentID, s.artifactKeys)
		if err != nil {
			file.Close()
			return err
		}
		source = reader
	} else {
		source = file
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".rotate-encryption-*")
	if err != nil {
		source.Close()
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		source.Close()
		return err
	}
	written, contentID, encryptErr := encryptArtifact(temporary, source, blob.size, s.artifactKeys[s.activeArtifactKeyID], s.activeArtifactKeyID)
	sourceCloseErr := source.Close()
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if encryptErr != nil || sourceCloseErr != nil || syncErr != nil || closeErr != nil || written != blob.size || contentID != blob.contentID {
		return errors.New("research store: artifact encryption migration failed integrity checks")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("research store: install encrypted artifact: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	scheme, keyID, err := s.verifyArtifactPath(path, blob.size, blob.contentID)
	if err != nil || scheme != artifactEncryptionScheme || keyID != s.activeArtifactKeyID {
		return errors.New("research store: migrated artifact verification failed")
	}
	return nil
}

func (s *Store) listArtifactBlobs(ctx context.Context) ([]storedArtifactBlob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT content_id, storage_path, size FROM artifacts
		WHERE COALESCE(json_extract(data, '$.purged_at'), '') = ''
		GROUP BY content_id, storage_path, size ORDER BY content_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blobs []storedArtifactBlob
	for rows.Next() {
		var blob storedArtifactBlob
		if err := rows.Scan(&blob.contentID, &blob.storagePath, &blob.size); err != nil {
			return nil, err
		}
		blobs = append(blobs, blob)
	}
	return blobs, rows.Err()
}

func (s *Store) resolveStoredBlob(blob storedArtifactBlob) (string, error) {
	if !strings.HasPrefix(blob.contentID, "sha256:") || len(blob.contentID) != len("sha256:")+sha256.Size*2 || blob.size < 0 {
		return "", errors.New("research store: invalid stored artifact identity")
	}
	hexDigest := strings.TrimPrefix(blob.contentID, "sha256:")
	if decoded, err := hex.DecodeString(hexDigest); err != nil || len(decoded) != sha256.Size {
		return "", errors.New("research store: invalid stored artifact identity")
	}
	expectedRelative := filepath.Join("blobs", hexDigest[:2], hexDigest)
	if filepath.Clean(filepath.FromSlash(blob.storagePath)) != expectedRelative {
		return "", errors.New("research store: stored artifact path mismatch")
	}
	path := filepath.Join(s.artifactRoot, expectedRelative)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(s.artifactRoot, real)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("research store: stored artifact escaped root")
	}
	return real, nil
}
