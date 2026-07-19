// Package sourcefetch downloads operator-pinned, immutable source bundles from
// fixed HTTPS origins and materializes them without executing target content.
package sourcefetch

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/acquisition"
)

const defaultBundleLimit int64 = 512 << 20

var (
	sourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	commitPattern     = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	digestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Source is an operator-owned mapping from repository identity to pinned
// source bundles. Bundle URLs contain no caller-controlled components.
type Source struct {
	Name       string   `json:"name"`
	Repository string   `json:"repository"`
	Bundles    []Bundle `json:"bundles"`
}

// Bundle pins one exact commit to one uncompressed tar response and digest.
type Bundle struct {
	Commit string `json:"commit"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Descriptor is the validated, immutable fetch policy for one commit.
type Descriptor struct {
	SourceName string
	Repository string
	Commit     string
	URL        string
	Host       string
	SHA256     string
}

// Result records the evidence computed during a successful bundle fetch.
type Result struct {
	Descriptor  Descriptor
	Snapshot    acquisition.Snapshot
	BundleBytes int64
	FetchedAt   time.Time
}

// HTTPDoer permits a dedicated authenticated transport without exposing
// credentials or arbitrary headers through the source configuration.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Broker owns a fixed source map and a bounded HTTP transport.
type Broker struct {
	doer           HTTPDoer
	bundles        map[string]map[string]Descriptor
	maxBundleBytes int64
}

// NewBroker validates every configured URL and commit before egress is enabled.
func NewBroker(doer HTTPDoer, sources []Source, maxBundleBytes int64) (*Broker, error) {
	if doer == nil || len(sources) == 0 {
		return nil, errors.New("sourcefetch: HTTP client and at least one fixed source required")
	}
	if maxBundleBytes <= 0 || maxBundleBytes > defaultBundleLimit {
		maxBundleBytes = defaultBundleLimit
	}
	broker := &Broker{doer: doer, bundles: make(map[string]map[string]Descriptor), maxBundleBytes: maxBundleBytes}
	for _, source := range sources {
		if !sourceNamePattern.MatchString(source.Name) || source.Repository != strings.TrimSpace(source.Repository) || source.Repository == "" || len(source.Bundles) == 0 || len(source.Bundles) > 1024 {
			return nil, fmt.Errorf("sourcefetch: invalid fixed source %q", source.Name)
		}
		if _, duplicate := broker.bundles[source.Name]; duplicate {
			return nil, fmt.Errorf("sourcefetch: duplicate source %q", source.Name)
		}
		broker.bundles[source.Name] = make(map[string]Descriptor, len(source.Bundles))
		for _, bundle := range source.Bundles {
			descriptor, err := validateBundle(source, bundle)
			if err != nil {
				return nil, err
			}
			if _, duplicate := broker.bundles[source.Name][bundle.Commit]; duplicate {
				return nil, fmt.Errorf("sourcefetch: duplicate commit in source %q", source.Name)
			}
			broker.bundles[source.Name][bundle.Commit] = descriptor
		}
	}
	return broker, nil
}

// Describe resolves only preconfigured source/commit pairs without egress.
func (b *Broker) Describe(sourceName, commit string) (Descriptor, error) {
	if b == nil {
		return Descriptor{}, errors.New("sourcefetch: broker unavailable")
	}
	byCommit, ok := b.bundles[sourceName]
	if !ok {
		return Descriptor{}, errors.New("sourcefetch: unknown fixed source")
	}
	descriptor, ok := byCommit[commit]
	if !ok {
		return Descriptor{}, errors.New("sourcefetch: commit has no pinned bundle")
	}
	return descriptor, nil
}

// Fetch downloads, hashes, and safely extracts a pinned uncompressed tar. The
// bundle plus extracted bytes share the supplied disk ceiling.
func (b *Broker) Fetch(ctx context.Context, sourceName, commit, destination string, limits acquisition.Limits) (Result, error) {
	descriptor, err := b.Describe(sourceName, commit)
	if err != nil {
		return Result{}, err
	}
	if !filepath.IsAbs(destination) || limits.MaxBytes <= 0 || limits.MaxFiles <= 0 {
		return Result{}, errors.New("sourcefetch: absolute destination and finite byte/inode limits required")
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Result{}, errors.New("sourcefetch: destination is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, descriptor.URL, nil)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Accept", "application/x-tar, application/octet-stream;q=0.8")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "agent-smith-sourcefetch/1")
	response, err := b.doer.Do(request)
	if err != nil {
		return Result{}, err
	}
	if response == nil || response.Body == nil {
		return Result{}, errors.New("sourcefetch: empty HTTP response")
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.String() != descriptor.URL {
		return Result{}, errors.New("sourcefetch: redirected or substituted bundle response")
	}
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("sourcefetch: bundle source returned HTTP %d", response.StatusCode)
	}
	downloadLimit := limits.MaxBytes
	if b.maxBundleBytes < downloadLimit {
		downloadLimit = b.maxBundleBytes
	}
	if response.ContentLength > downloadLimit {
		return Result{}, errors.New("sourcefetch: bundle response exceeds limit")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Result{}, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".source-fetch-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(temporary)
	bundlePath := filepath.Join(temporary, "source.tar")
	bundleFile, err := os.OpenFile(bundlePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(bundleFile, digest), io.LimitReader(response.Body, downloadLimit+1))
	syncErr := bundleFile.Sync()
	closeErr := bundleFile.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return Result{}, errors.New("sourcefetch: incomplete bundle download")
	}
	if written > downloadLimit {
		return Result{}, errors.New("sourcefetch: bundle response exceeds limit")
	}
	actualDigest := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if actualDigest != descriptor.SHA256 {
		return Result{}, errors.New("sourcefetch: bundle digest mismatch")
	}
	remainingBytes := limits.MaxBytes - written
	if remainingBytes < 0 {
		return Result{}, errors.New("sourcefetch: bundle exhausts acquisition disk budget")
	}
	tree := filepath.Join(temporary, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		return Result{}, err
	}
	if err := extractTar(bundlePath, tree, acquisition.Limits{MaxFiles: limits.MaxFiles, MaxBytes: remainingBytes}); err != nil {
		return Result{}, err
	}
	snapshot, err := acquisition.HashTree(tree, acquisition.Limits{MaxFiles: limits.MaxFiles, MaxBytes: remainingBytes})
	if err != nil {
		return Result{}, err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Result{}, errors.New("sourcefetch: destination is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	if existing, hashErr := acquisition.HashTree(destination, limits); hashErr == nil {
		if existing.SourceSHA256 != snapshot.SourceSHA256 || existing.Files != snapshot.Files || existing.Bytes != snapshot.Bytes {
			return Result{}, errors.New("sourcefetch: existing capture hash mismatch")
		}
		return Result{Descriptor: descriptor, Snapshot: existing, BundleBytes: written, FetchedAt: time.Now().UTC()}, nil
	} else if !errors.Is(hashErr, os.ErrNotExist) {
		return Result{}, hashErr
	}
	if err := os.Rename(tree, destination); err != nil {
		return Result{}, err
	}
	return Result{Descriptor: descriptor, Snapshot: snapshot, BundleBytes: written, FetchedAt: time.Now().UTC()}, nil
}

func validateBundle(source Source, bundle Bundle) (Descriptor, error) {
	parsed, err := url.Parse(bundle.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Port() != "" && parsed.Port() != "443") || parsed.String() != bundle.URL || !commitPattern.MatchString(bundle.Commit) || !digestPattern.MatchString(bundle.SHA256) {
		return Descriptor{}, fmt.Errorf("sourcefetch: invalid pinned bundle for source %q", source.Name)
	}
	if address, parseErr := netip.ParseAddr(parsed.Hostname()); parseErr == nil && !publicAddress(address) {
		return Descriptor{}, fmt.Errorf("sourcefetch: private or reserved bundle address for source %q", source.Name)
	}
	return Descriptor{SourceName: source.Name, Repository: source.Repository, Commit: bundle.Commit, URL: bundle.URL, Host: strings.ToLower(parsed.Hostname()), SHA256: bundle.SHA256}, nil
}

func extractTar(bundlePath, destination string, limits acquisition.Limits) error {
	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	seen := make(map[string]byte)
	var bytesWritten int64
	var inodes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return onlyZeroPadding(file)
		}
		if err != nil {
			return errors.New("sourcefetch: malformed tar bundle")
		}
		relative, err := acquisition.ValidateSourcePath(strings.TrimSuffix(header.Name, "/"))
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return errors.New("sourcefetch: tar directory contains a payload")
			}
			if err := ensureTarDirectories(destination, relative, seen, &inodes, limits.MaxFiles); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxBytes-bytesWritten {
				return errors.New("sourcefetch: extracted source exceeds byte limit")
			}
			if err := ensureTarDirectories(destination, filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative))), seen, &inodes, limits.MaxFiles); err != nil {
				return err
			}
			key := portablePathKey(relative)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("sourcefetch: duplicate or case-colliding tar path")
			}
			inodes++
			if inodes > limits.MaxFiles {
				return errors.New("sourcefetch: extracted source exceeds inode limit")
			}
			seen[key] = 'f'
			mode := fs.FileMode(0o400)
			if header.FileInfo().Mode()&0o111 != 0 {
				mode = 0o500
			}
			path := filepath.Join(destination, filepath.FromSlash(relative))
			output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(output, reader, header.Size)
			syncErr := output.Sync()
			closeErr := output.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || written != header.Size {
				return errors.New("sourcefetch: incomplete tar entry")
			}
			bytesWritten += written
		default:
			return errors.New("sourcefetch: tar contains unsupported entry type")
		}
	}
}

func ensureTarDirectories(root, relative string, seen map[string]byte, inodes *int64, maximum int64) error {
	if relative == "." || relative == "" {
		return nil
	}
	components := strings.Split(relative, "/")
	current := ""
	for _, component := range components {
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		key := portablePathKey(current)
		if kind, exists := seen[key]; exists {
			if kind != 'd' {
				return errors.New("sourcefetch: tar path crosses a regular file")
			}
			continue
		}
		(*inodes)++
		if *inodes > maximum {
			return errors.New("sourcefetch: extracted source exceeds inode limit")
		}
		seen[key] = 'd'
		if err := os.Mkdir(filepath.Join(root, filepath.FromSlash(current)), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func portablePathKey(value string) string { return strings.ToLower(filepath.ToSlash(value)) }

func onlyZeroPadding(reader io.Reader) error {
	buffer := make([]byte, 32<<10)
	for {
		count, err := reader.Read(buffer)
		for _, value := range buffer[:count] {
			if value != 0 {
				return errors.New("sourcefetch: tar contains trailing non-padding data")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
