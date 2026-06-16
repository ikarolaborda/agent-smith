/*
Shared workspace path-safety primitives for the mutating file tools
(file_write, file_edit). These mirror file_read's sandbox but must also handle
targets that do not exist yet (a fresh file), so symlink resolution is anchored
on the nearest EXISTING ancestor directory rather than the target itself.

Mutation is a real security surface, so the policy is deliberately strict:
  - the resolved path must stay inside the workspace root,
  - no component of the existing ancestor chain may symlink out of the root,
  - the target, if it exists, must be a regular file (never a symlink, dir,
    device, pipe, or socket),
  - writes are atomic (temp file in the destination dir, then rename),
  - payloads are size-capped.
*/
package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

/* DefaultWorkspaceMaxWriteBytes caps the size of a single write/edit payload. */
const DefaultWorkspaceMaxWriteBytes = 1 << 20 /* 1 MiB */

/*
resolveWorkspaceTarget validates rel against root for a mutating operation and
returns the cleaned absolute path. It rejects traversal, absolute-path escapes,
and symlinked ancestors that point outside the root. The target itself may or
may not exist; when it exists it must be a regular file.
*/
func resolveWorkspaceTarget(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(filepath.Join(root, filepath.Clean(rel)))
	if err != nil {
		return "", fmt.Errorf("abs path: %w", err)
	}
	if !insideRoot(abs, root) {
		return "", fmt.Errorf("refused: %q is outside the workspace", rel)
	}

	/*
		Resolve symlinks on the nearest existing ancestor. The target file may
		not exist yet, so walking up to the first existing directory and
		canonicalizing THAT blocks a symlinked parent from redirecting the write
		outside the workspace.
	*/
	rootReal, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		rootReal = filepath.Clean(root)
	}
	ancestor := abs
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	if real, err := filepath.EvalSymlinks(ancestor); err == nil {
		if !insideRoot(real, rootReal) {
			return "", fmt.Errorf("refused: %q resolves outside the workspace", rel)
		}
	}

	/* If the target exists it must be a regular file; never follow a symlink. */
	if info, err := os.Lstat(abs); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refused: %q is a symlink", rel)
		}
		if info.IsDir() {
			return "", fmt.Errorf("refused: %q is a directory", rel)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("refused: %q is not a regular file", rel)
		}
	}
	return abs, nil
}

/*
atomicWrite writes data to abs by creating a temp file in the same directory and
renaming it into place, so a crash mid-write never leaves a truncated file. The
destination directory must already exist inside the workspace (we do not
auto-create arbitrary parents in this first slice). On overwrite the existing
file's mode is preserved; new files default to 0o644.
*/
func atomicWrite(abs string, data []byte) error {
	dir := filepath.Dir(abs)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("destination directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination parent %q is not a directory", dir)
	}

	mode := os.FileMode(0o644)
	if existing, err := os.Stat(abs); err == nil {
		mode = existing.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".agent-write-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		cleanup()
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
