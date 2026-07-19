// Package acquisition captures an operator-authorized source tree into a
// bounded immutable worker input without executing target-controlled code.
package acquisition

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

const (
	defaultMaxFiles int64 = 10000
	defaultMaxBytes int64 = 512 << 20
)

// Limits bound local source acquisition.
type Limits struct {
	MaxFiles int64
	MaxBytes int64
}

// Snapshot is a deterministic source-tree identity.
type Snapshot struct {
	SourceSHA256 string
	Files        int64
	Bytes        int64
}

// CaptureDirectory resolves the private source view for one campaign target.
func CaptureDirectory(internalRoot, campaignID, targetID string) (string, error) {
	if !filepath.IsAbs(internalRoot) || !safeIDPattern.MatchString(campaignID) || !safeIDPattern.MatchString(targetID) {
		return "", errors.New("research acquisition: unsafe capture identity")
	}
	return filepath.Join(internalRoot, campaignID, "sources", targetID), nil
}

// VerifiedCapture rehashes a captured tree immediately before it is mounted.
func VerifiedCapture(internalRoot, campaignID, targetID, expectedSHA256 string, limits Limits) (string, error) {
	path, err := CaptureDirectory(internalRoot, campaignID, targetID)
	if err != nil {
		return "", err
	}
	snapshot, err := HashTree(path, limits)
	if err != nil {
		return "", err
	}
	if snapshot.SourceSHA256 != expectedSHA256 {
		return "", errors.New("research acquisition: captured source hash mismatch")
	}
	return path, nil
}

// HashTree hashes regular files, normalized relative paths, sizes, and the
// executable bit. Symlinks and special files fail closed.
func HashTree(root string, limits Limits) (Snapshot, error) {
	root, limits, err := prepare(root, limits)
	if err != nil {
		return Snapshot{}, err
	}
	hash := sha256.New()
	result := Snapshot{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && ignoredDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("research acquisition: non-regular source entry %q", filepath.ToSlash(relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.Files++
		result.Bytes += info.Size()
		if result.Files > limits.MaxFiles || result.Bytes > limits.MaxBytes {
			return errors.New("research acquisition: source tree exceeds configured limits")
		}
		if err := writeIdentity(hash, filepath.ToSlash(relative), info); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		before := info
		_, copyErr := io.Copy(hash, file)
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
			return errors.New("research acquisition: source changed during hashing")
		}
		return closeErr
	})
	if err != nil {
		return result, err
	}
	result.SourceSHA256 = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

// Capture copies a source tree into destination and verifies that the captured
// tree has the expected identity. Destination is committed by atomic rename.
func Capture(source, destination, expectedSHA256 string, limits Limits) (Snapshot, error) {
	if !filepath.IsAbs(destination) || strings.TrimSpace(expectedSHA256) == "" {
		return Snapshot{}, errors.New("research acquisition: absolute destination and expected hash required")
	}
	if existing, err := HashTree(destination, limits); err == nil {
		if existing.SourceSHA256 != expectedSHA256 {
			return existing, errors.New("research acquisition: existing capture hash mismatch")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Snapshot{}, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".capture-*")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(temporary)
	sourceRoot, limits, err := prepare(source, limits)
	if err != nil {
		return Snapshot{}, err
	}
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if ignoredDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return os.Mkdir(filepath.Join(temporary, relative), 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("research acquisition: non-regular source entry %q", filepath.ToSlash(relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o400)
		if info.Mode()&0o111 != 0 {
			mode = 0o500
		}
		return copyRegular(path, filepath.Join(temporary, relative), mode, info)
	})
	if err != nil {
		return Snapshot{}, err
	}
	captured, err := HashTree(temporary, limits)
	if err != nil {
		return captured, err
	}
	if captured.SourceSHA256 != expectedSHA256 {
		return captured, errors.New("research acquisition: source changed while being captured")
	}
	if err := os.Rename(temporary, destination); err != nil {
		return captured, err
	}
	return captured, nil
}

func prepare(root string, limits Limits) (string, Limits, error) {
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaultMaxFiles
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultMaxBytes
	}
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", limits, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", limits, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", limits, err
	}
	if !info.IsDir() {
		return "", limits, errors.New("research acquisition: source root is not a directory")
	}
	return real, limits, nil
}

func writeIdentity(destination io.Writer, relative string, info fs.FileInfo) error {
	if len(relative) > 1<<20 {
		return errors.New("research acquisition: source path is too long")
	}
	var header [17]byte
	binary.BigEndian.PutUint64(header[:8], uint64(len(relative)))
	binary.BigEndian.PutUint64(header[8:16], uint64(info.Size()))
	if info.Mode()&0o111 != 0 {
		header[16] = 1
	}
	if _, err := destination.Write(header[:]); err != nil {
		return err
	}
	_, err := io.WriteString(destination, relative)
	return err
}

func copyRegular(sourcePath, destinationPath string, mode fs.FileMode, before fs.FileInfo) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	after, statErr := source.Stat()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if statErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		return errors.New("research acquisition: source changed during capture")
	}
	return nil
}

func ignoredDirectory(name string) bool { return name == ".git" || name == ".agent-smith" }
