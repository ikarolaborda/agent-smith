package acquisition

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" // Git SHA-1 object identity; not used as a security primitive.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var exactCommitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

const maxGitOutputBytes = 16 << 20

// Checkout identifies the exact Git identity used for acquisition.
type Checkout struct {
	RequestedRef string
	Commit       string
}

// VerifyGitCheckout requires an exact commit identifier, a repository rooted
// at root, a matching HEAD and index, and no submodules. Worktree cleanliness
// is established by CaptureGitCheckout without invoking Git's content filters.
// Hooks, replacement objects, recursive submodules, filesystem monitors,
// pagers, optional locks, and ambient Git configuration are disabled.
func VerifyGitCheckout(ctx context.Context, root, requestedCommit string) (Checkout, error) {
	if requestedCommit != strings.TrimSpace(requestedCommit) || !exactCommitPattern.MatchString(requestedCommit) {
		return Checkout{}, errors.New("research acquisition: exact lowercase Git commit required")
	}
	realRoot, _, err := prepare(root, Limits{})
	if err != nil {
		return Checkout{}, err
	}
	if strings.ContainsAny(realRoot, "\r\n") {
		return Checkout{}, errors.New("research acquisition: source path contains unsupported control characters")
	}
	topLevel, err := runGit(ctx, realRoot, 4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return Checkout{}, err
	}
	realTop, err := filepath.EvalSymlinks(strings.TrimSpace(string(topLevel)))
	if err != nil {
		return Checkout{}, fmt.Errorf("research acquisition: resolve Git top-level: %w", err)
	}
	if !sameCanonicalPath(realRoot, realTop) {
		return Checkout{}, errors.New("research acquisition: source must be the Git repository root")
	}
	resolved, err := runGit(ctx, realRoot, 4096, "rev-parse", "--verify", "--end-of-options", requestedCommit+"^{commit}")
	if err != nil {
		return Checkout{}, errors.New("research acquisition: requested Git commit is unavailable")
	}
	resolvedCommit := strings.TrimSpace(string(resolved))
	if resolvedCommit != requestedCommit || !exactCommitPattern.MatchString(resolvedCommit) {
		return Checkout{}, errors.New("research acquisition: requested Git commit did not resolve exactly")
	}
	head, err := runGit(ctx, realRoot, 4096, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != resolvedCommit {
		return Checkout{}, errors.New("research acquisition: checkout HEAD does not match requested commit")
	}
	if _, err := runGit(ctx, realRoot, 4096, "diff-index", "--cached", "--quiet", "--no-renames", resolvedCommit, "--"); err != nil {
		return Checkout{}, errors.New("research acquisition: checkout index does not match requested commit")
	}
	index, err := runGit(ctx, realRoot, maxGitOutputBytes, "ls-files", "--stage", "-z")
	if err != nil {
		return Checkout{}, err
	}
	for _, entry := range bytes.Split(index, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("160000 ")) {
			return Checkout{}, errors.New("research acquisition: Git submodules are not supported")
		}
	}
	return Checkout{RequestedRef: requestedCommit, Commit: resolvedCommit}, nil
}

// CaptureGitCheckout exports regular files directly from the verified commit
// object rather than trusting mutable worktree bytes. The resulting tree is
// read-only, bounded, and atomically installed at destination.
func CaptureGitCheckout(ctx context.Context, root, requestedCommit, destination string, limits Limits) (Checkout, Snapshot, error) {
	if !filepath.IsAbs(destination) {
		return Checkout{}, Snapshot{}, errors.New("research acquisition: absolute destination required")
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Checkout{}, Snapshot{}, errors.New("research acquisition: capture destination is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Checkout{}, Snapshot{}, err
	}
	checkout, err := VerifyGitCheckout(ctx, root, requestedCommit)
	if err != nil {
		return Checkout{}, Snapshot{}, err
	}
	realRoot, limits, err := prepare(root, limits)
	if err != nil {
		return Checkout{}, Snapshot{}, err
	}
	entries, err := gitTreeEntries(ctx, realRoot, checkout.Commit, limits)
	if err != nil {
		return Checkout{}, Snapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Checkout{}, Snapshot{}, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".git-capture-*")
	if err != nil {
		return Checkout{}, Snapshot{}, err
	}
	defer os.RemoveAll(temporary)
	if err := exportGitBlobs(ctx, realRoot, temporary, entries); err != nil {
		return Checkout{}, Snapshot{}, err
	}
	snapshot, err := HashTree(temporary, limits)
	if err != nil {
		return Checkout{}, snapshot, err
	}
	if _, err := os.Lstat(filepath.Join(realRoot, ".agent-smith")); err == nil {
		return Checkout{}, snapshot, errors.New("research acquisition: checkout contains reserved control content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Checkout{}, snapshot, err
	}
	worktree, err := HashTree(realRoot, limits)
	if err != nil {
		return Checkout{}, snapshot, err
	}
	if worktree.SourceSHA256 != snapshot.SourceSHA256 || worktree.Files != snapshot.Files || worktree.Bytes != snapshot.Bytes {
		return Checkout{}, snapshot, errors.New("research acquisition: checkout content does not match requested commit")
	}
	confirmed, err := VerifyGitCheckout(ctx, realRoot, requestedCommit)
	if err != nil || confirmed.Commit != checkout.Commit {
		return Checkout{}, snapshot, errors.New("research acquisition: checkout changed during commit export")
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Checkout{}, snapshot, errors.New("research acquisition: capture destination is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Checkout{}, snapshot, err
	}
	if existing, hashErr := HashTree(destination, limits); hashErr == nil {
		if existing.SourceSHA256 != snapshot.SourceSHA256 {
			return Checkout{}, snapshot, errors.New("research acquisition: existing Git capture hash mismatch")
		}
		return checkout, existing, nil
	} else if !errors.Is(hashErr, os.ErrNotExist) {
		return Checkout{}, snapshot, hashErr
	}
	if err := os.Rename(temporary, destination); err != nil {
		return Checkout{}, snapshot, err
	}
	return checkout, snapshot, nil
}

type gitTreeEntry struct {
	object string
	path   string
	size   int64
	mode   fs.FileMode
}

func gitTreeEntries(ctx context.Context, root, commit string, limits Limits) ([]gitTreeEntry, error) {
	output, err := runGit(ctx, root, maxGitOutputBytes, "ls-tree", "-rz", "--long", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	entries := make([]gitTreeEntry, 0)
	var bytesTotal int64
	seen := make(map[string]struct{})
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("research acquisition: malformed Git tree entry")
		}
		metadata := strings.Fields(string(parts[0]))
		if len(metadata) != 4 || metadata[1] != "blob" || (metadata[0] != "100644" && metadata[0] != "100755") || !exactCommitPattern.MatchString(metadata[2]) {
			return nil, errors.New("research acquisition: Git tree contains a non-regular or malformed entry")
		}
		size, parseErr := strconv.ParseInt(metadata[3], 10, 64)
		if parseErr != nil || size < 0 {
			return nil, errors.New("research acquisition: malformed Git blob size")
		}
		relative, pathErr := safeGitPath(string(parts[1]))
		if pathErr != nil {
			return nil, pathErr
		}
		if _, duplicate := seen[relative]; duplicate {
			return nil, errors.New("research acquisition: duplicate Git tree path")
		}
		seen[relative] = struct{}{}
		if size > limits.MaxBytes-bytesTotal || int64(len(entries)+1) > limits.MaxFiles {
			return nil, errors.New("research acquisition: Git tree exceeds configured limits")
		}
		bytesTotal += size
		mode := fs.FileMode(0o400)
		if metadata[0] == "100755" {
			mode = 0o500
		}
		entries = append(entries, gitTreeEntry{object: metadata[2], path: relative, size: size, mode: mode})
	}
	return entries, nil
}

func exportGitBlobs(ctx context.Context, root, destination string, entries []gitTreeEntry) error {
	command := gitCommand(ctx, root, "cat-file", "--batch")
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &boundedBuffer{limit: maxGitOutputBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("research acquisition: start Git object export: %w", err)
	}
	abort := func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}
	reader := bufio.NewReaderSize(stdout, 4096)
	for _, entry := range entries {
		if _, err := io.WriteString(stdin, entry.object+"\n"); err != nil {
			abort()
			return errors.New("research acquisition: request Git blob")
		}
		header, err := readBoundedLine(reader, 4096)
		if err != nil {
			abort()
			return errors.New("research acquisition: read Git blob header")
		}
		fields := strings.Fields(strings.TrimSuffix(header, "\n"))
		if len(fields) != 3 || fields[0] != entry.object || fields[1] != "blob" {
			abort()
			return errors.New("research acquisition: Git blob identity mismatch")
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size != entry.size {
			abort()
			return errors.New("research acquisition: Git blob size mismatch")
		}
		destinationPath := filepath.Join(destination, filepath.FromSlash(entry.path))
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o700); err != nil {
			abort()
			return err
		}
		file, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.mode)
		if err != nil {
			abort()
			return err
		}
		objectHash, hashErr := gitObjectHash(entry.object, "blob", entry.size)
		if hashErr != nil {
			_ = file.Close()
			abort()
			return hashErr
		}
		written, copyErr := io.CopyN(io.MultiWriter(file, objectHash), reader, entry.size)
		syncErr := file.Sync()
		closeErr := file.Close()
		terminator, readErr := reader.ReadByte()
		objectMatches := hex.EncodeToString(objectHash.Sum(nil)) == entry.object
		if copyErr != nil || written != entry.size || syncErr != nil || closeErr != nil || readErr != nil || terminator != '\n' || !objectMatches {
			abort()
			return errors.New("research acquisition: incomplete Git blob export")
		}
	}
	if err := stdin.Close(); err != nil {
		abort()
		return err
	}
	if err := command.Wait(); err != nil || stderr.exceeded {
		return errors.New("research acquisition: Git object export failed")
	}
	return nil
}

func gitObjectHash(object, kind string, size int64) (hash.Hash, error) {
	var result hash.Hash
	switch len(object) {
	case sha1.Size * 2:
		result = sha1.New() // #nosec G505 -- matching Git's immutable object format.
	case sha256.Size * 2:
		result = sha256.New()
	default:
		return nil, errors.New("research acquisition: unsupported Git object identity")
	}
	_, _ = fmt.Fprintf(result, "%s %d%c", kind, size, byte(0))
	return result, nil
}

func safeGitPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\\') || len(value) > 1<<20 {
		return "", errors.New("research acquisition: unsafe Git tree path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean != value || filepath.IsAbs(filepath.FromSlash(value)) || filepath.VolumeName(filepath.FromSlash(value)) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("research acquisition: unsafe Git tree path")
	}
	for _, component := range strings.Split(clean, "/") {
		if strings.EqualFold(component, ".git") || strings.EqualFold(component, ".agent-smith") {
			return "", errors.New("research acquisition: Git tree contains a reserved control path")
		}
	}
	return clean, nil
}

func runGit(ctx context.Context, root string, limit int64, arguments ...string) ([]byte, error) {
	command := gitCommand(ctx, root, arguments...)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.exceeded || stderr.exceeded {
		return nil, errors.New("research acquisition: constrained Git command failed")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func gitCommand(ctx context.Context, root string, arguments ...string) *exec.Cmd {
	global := []string{"--no-optional-locks", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "submodule.recurse=false", "-C", root}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = gitEnvironment()
	return command
}

func gitEnvironment() []string {
	allowed := map[string]bool{"PATH": true, "SystemRoot": true, "SYSTEMROOT": true, "WINDIR": true, "TMPDIR": true, "TMP": true, "TEMP": true}
	environment := make([]string, 0, len(allowed)+7)
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if found && allowed[key] {
			environment = append(environment, item)
		}
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"LC_ALL=C",
		"LANG=C",
	)
}

func sameCanonicalPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int64
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - int64(buffer.Len())
	if remaining <= 0 {
		buffer.exceeded = true
		return original, nil
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(value)
	return original, nil
}

func readBoundedLine(reader *bufio.Reader, limit int) (string, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > limit {
		return "", errors.New("line exceeds limit")
	}
	if err != nil {
		return "", err
	}
	return string(line), nil
}
