/*
Package llamacpp implements the safe acquisition and supervision boundary for
local llama.cpp models. Downloads are planned from immutable Hugging Face
metadata, checked against live host capacity, verified, and committed before a
runtime is allowed to see them.
*/
package llamacpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultHFBase = "https://huggingface.co"
const DefaultQuant = "Q4_K_M"

// Ref identifies a Hugging Face GGUF artifact set. File pins a model file; if
// it names one shard, every shard in that split is selected. When File and
// Quant are both set, Quant is a required consistency assertion rather than an
// ignored label. MMProjFile pins a multimodal projector. Revision defaults to
// main but the resolved manifest and every artifact URL are always pinned to
// the returned commit SHA.
type Ref struct {
	Repo       string
	Quant      string
	File       string
	MMProjFile string
	Revision   string
}

func ParseRef(s string) (Ref, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Ref{}, errors.New("llamacpp: empty model reference")
	}
	for _, prefix := range []string{"https://", "http://", "hf.co/", "huggingface.co/"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	raw = strings.TrimPrefix(raw, "huggingface.co/")
	quant := ""
	if i := strings.LastIndex(raw, ":"); i >= 0 {
		quant = strings.TrimSpace(raw[i+1:])
		raw = raw[:i]
	}
	raw = strings.Trim(raw, "/")
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Ref{}, fmt.Errorf("llamacpp: model reference %q is not user/name[:quant]", s)
	}
	return Ref{Repo: parts[0] + "/" + parts[1], Quant: quant, Revision: "main"}, nil
}

type ArtifactKind string

const (
	ArtifactModel  ArtifactKind = "model"
	ArtifactMMProj ArtifactKind = "mmproj"
)

// Artifact is one size- and hash-addressed file from a resolved commit.
type Artifact struct {
	Kind     ArtifactKind `json:"kind"`
	Filename string       `json:"filename"`
	Size     uint64       `json:"size"`
	SHA256   string       `json:"sha256"`
	URL      string       `json:"url"`
}

// Manifest is a commit-pinned selection. Only artifacts needed by the chosen
// quantization are included; unrelated repository files cannot affect a pull.
type Manifest struct {
	Repo              string     `json:"repo"`
	RequestedRevision string     `json:"requested_revision"`
	CommitSHA         string     `json:"commit_sha"`
	Quant             string     `json:"quant,omitempty"`
	ModelArtifacts    []Artifact `json:"model_artifacts"`
	MMProjArtifact    *Artifact  `json:"mmproj_artifact,omitempty"`
	/*
		ContextLength is the model's native context length as reported by the
		Hugging Face gguf metadata (0 = unknown). Advisory pre-download hint for
		the context ceiling only — never part of admission or verification.
	*/
	ContextLength int `json:"context_length,omitempty"`
}

// Artifacts returns a defensive copy in download order (model shards first).
func (m Manifest) Artifacts() []Artifact {
	out := append([]Artifact(nil), m.ModelArtifacts...)
	if m.MMProjArtifact != nil {
		out = append(out, *m.MMProjArtifact)
	}
	return out
}

func (m Manifest) ModelBytes() uint64 {
	total, err := m.modelBytesChecked()
	if err != nil {
		return 0
	}
	return total
}

func (m Manifest) modelBytesChecked() (uint64, error) {
	var total uint64
	for _, a := range m.ModelArtifacts {
		var overflow bool
		total, overflow = add64(total, a.Size)
		if overflow {
			return 0, errors.New("llamacpp: total model artifact size overflow")
		}
	}
	return total, nil
}

func (m Manifest) MMProjBytes() uint64 {
	if m.MMProjArtifact == nil {
		return 0
	}
	return m.MMProjArtifact.Size
}

// DownloadPlan combines immutable remote metadata, a live host snapshot, and
// the resulting fit decision. Inspect/Plan never download artifact bodies.
type DownloadPlan struct {
	Manifest Manifest    `json:"manifest"`
	Host     HostProfile `json:"host"`
	Fit      FitReport   `json:"fit"`
	CacheDir string      `json:"cache_dir"`
}

// LocalArtifacts is returned only after every selected file is committed and
// validated. Model is the path llama-server should receive (the first shard).
type LocalArtifacts struct {
	Model      string    `json:"model"`
	ModelFiles []string  `json:"model_files"`
	MMProj     string    `json:"mmproj,omitempty"`
	Manifest   Manifest  `json:"manifest"`
	Fit        FitReport `json:"fit"`
}

// FitError exposes a structured report while remaining a normal Go error.
type FitError struct{ Report FitReport }

func (e *FitError) Error() string {
	msg := "llamacpp: model does not safely fit this host: " + strings.Join(e.Report.Reasons, "; ")
	if s := e.Report.Suggestion; s != nil {
		fit := "should fit"
		if !s.Fits {
			fit = "smallest available, may still not fit"
		}
		msg += fmt.Sprintf(
			"; try a smaller abliterated model: %s (~%.1fB params, %s)",
			s.Model.Ref, s.Model.ParamsB, fit,
		)
	}
	return msg
}

// Downloader resolves and transactionally installs one manifest at a time.
// A nil Profiler means SystemProfiler, so safety cannot be accidentally
// disabled by constructing Downloader directly instead of using NewDownloader.
type Downloader struct {
	Base          string
	ModelsDir     string
	Token         string
	HTTP          *http.Client
	Logger        *slog.Logger
	Profiler      Profiler
	FitPolicy     FitPolicy
	ContextTokens int
	Parallel      int
}

func NewDownloader(modelsDir, token string, logger *slog.Logger) *Downloader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Downloader{
		Base:      DefaultHFBase,
		ModelsDir: modelsDir,
		Token:     token,
		HTTP:      &http.Client{Timeout: 6 * time.Hour},
		Logger:    logger,
		Profiler:  SystemProfiler{},
		FitPolicy: DefaultFitPolicy(),
	}
}

func (d *Downloader) base() string {
	if d.Base == "" {
		return DefaultHFBase
	}
	return strings.TrimRight(d.Base, "/")
}

func (d *Downloader) httpClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 6 * time.Hour}
}

func (d *Downloader) log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d *Downloader) profiler() Profiler {
	if d.Profiler != nil {
		return d.Profiler
	}
	return SystemProfiler{}
}

func (d *Downloader) policy() FitPolicy {
	if d.FitPolicy.KVBytesPerToken == 0 {
		return DefaultFitPolicy()
	}
	return d.FitPolicy
}

type hfLFS struct {
	SHA256 string `json:"sha256"`
	OID    string `json:"oid"`
	Size   int64  `json:"size"`
}

type hfSibling struct {
	RFilename string `json:"rfilename"`
	Size      int64  `json:"size"`
	LFS       *hfLFS `json:"lfs"`
}

type hfModelInfo struct {
	SHA      string      `json:"sha"`
	Siblings []hfSibling `json:"siblings"`
	/*
		GGUF is Hugging Face's server-side parse of the repo's GGUF metadata.
		ContextLength is an advisory pre-download hint for the tuner's context
		ceiling; the committed local file remains authoritative once present.
	*/
	GGUF *hfGGUFInfo `json:"gguf"`
}

type hfGGUFInfo struct {
	Architecture  string `json:"architecture"`
	ContextLength uint64 `json:"context_length"`
}

// Resolve preserves the original API and returns the primary model filename.
// Use ResolveManifest when sizes, shards, mmproj, or commit identity matter.
func (d *Downloader) Resolve(ctx context.Context, ref Ref) (string, error) {
	m, err := d.ResolveManifest(ctx, ref)
	if err != nil {
		return "", err
	}
	return m.ModelArtifacts[0].Filename, nil
}

// ResolveManifest queries the Hugging Face model API with blob metadata and
// fails closed unless every selected artifact has an exact LFS size and SHA256.
func (d *Downloader) ResolveManifest(ctx context.Context, ref Ref) (Manifest, error) {
	if err := validateRef(ref); err != nil {
		return Manifest{}, err
	}
	revision := ref.Revision
	if revision == "" {
		revision = "main"
	}
	u := d.base() + "/api/models/" + ref.Repo + "/revision/" + url.PathEscape(revision) + "?blobs=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("llamacpp: build metadata request: %w", err)
	}
	d.authorize(req)
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("llamacpp: fetch metadata for %q: %w", ref.Repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Manifest{}, fmt.Errorf("llamacpp: metadata for %q: http %d: %s", ref.Repo, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var info hfModelInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&info); err != nil {
		return Manifest{}, fmt.Errorf("llamacpp: decode metadata for %q: %w", ref.Repo, err)
	}
	if !validHex(info.SHA, 40, 64) {
		return Manifest{}, fmt.Errorf("llamacpp: metadata for %q did not provide a valid immutable commit SHA", ref.Repo)
	}
	if isCommitRevision(revision) && !strings.EqualFold(revision, info.SHA) {
		return Manifest{}, fmt.Errorf(
			"llamacpp: immutable revision %q resolved to unexpected commit %q",
			revision, info.SHA,
		)
	}

	models, mmproj, err := selectArtifacts(ref, info.Siblings)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Repo:              ref.Repo,
		RequestedRevision: revision,
		CommitSHA:         strings.ToLower(info.SHA),
		Quant:             manifestQuant(ref),
	}
	if info.GGUF != nil && info.GGUF.ContextLength > 0 {
		manifest.ContextLength = clampCtx(info.GGUF.ContextLength)
	}
	for _, s := range models {
		a, err := d.manifestArtifact(ref.Repo, manifest.CommitSHA, s, ArtifactModel)
		if err != nil {
			return Manifest{}, err
		}
		manifest.ModelArtifacts = append(manifest.ModelArtifacts, a)
	}
	if mmproj != nil {
		a, err := d.manifestArtifact(ref.Repo, manifest.CommitSHA, *mmproj, ArtifactMMProj)
		if err != nil {
			return Manifest{}, err
		}
		manifest.MMProjArtifact = &a
	}
	return cloneManifest(manifest), nil
}

func (d *Downloader) manifestArtifact(repo, commit string, s hfSibling, kind ArtifactKind) (Artifact, error) {
	name, err := sanitizeGGUFName(s.RFilename)
	if err != nil {
		return Artifact{}, err
	}
	size := s.Size
	hash := ""
	if s.LFS != nil {
		if s.LFS.Size > 0 {
			size = s.LFS.Size
		}
		hash = s.LFS.SHA256
		if hash == "" {
			hash = strings.TrimPrefix(s.LFS.OID, "sha256:")
		}
	}
	if size <= 0 {
		return Artifact{}, fmt.Errorf("llamacpp: artifact %q has no trustworthy size in Hugging Face blob metadata", name)
	}
	if !validHex(hash, 64, 64) {
		return Artifact{}, fmt.Errorf("llamacpp: artifact %q has no trustworthy LFS SHA256 in Hugging Face blob metadata", name)
	}
	return Artifact{
		Kind:     kind,
		Filename: name,
		Size:     uint64(size),
		SHA256:   strings.ToLower(hash),
		URL:      fmt.Sprintf("%s/%s/resolve/%s/%s", d.base(), repo, commit, url.PathEscape(name)),
	}, nil
}

func validateRef(ref Ref) error {
	parts := strings.Split(ref.Repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(ref.Repo, "..") {
		return fmt.Errorf("llamacpp: invalid Hugging Face repository %q", ref.Repo)
	}
	for _, part := range parts {
		if strings.ContainsAny(part, `\\?#`) {
			return fmt.Errorf("llamacpp: invalid Hugging Face repository %q", ref.Repo)
		}
	}
	if strings.ContainsAny(ref.Revision, "\r\n?#") {
		return errors.New("llamacpp: invalid Hugging Face revision")
	}
	return nil
}

var splitGGUF = regexp.MustCompile(`(?i)^(.*)-(\d{5})-of-(\d{5})\.gguf$`)

type artifactGroup struct {
	key     string
	files   []hfSibling
	primary string
	split   bool
	display string
}

func selectArtifacts(ref Ref, siblings []hfSibling) ([]hfSibling, *hfSibling, error) {
	byName := make(map[string]hfSibling)
	var modelFiles, mmprojFiles []hfSibling
	hasSafetensors := false
	for _, s := range siblings {
		name := strings.TrimSpace(s.RFilename)
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".safetensors") {
			hasSafetensors = true
		}
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		if _, err := sanitizeGGUFName(name); err != nil {
			return nil, nil, err
		}
		byName[name] = s
		if strings.Contains(lower, "mmproj") {
			mmprojFiles = append(mmprojFiles, s)
		} else {
			modelFiles = append(modelFiles, s)
		}
	}
	if len(modelFiles) == 0 {
		if hasSafetensors {
			return nil, nil, fmt.Errorf("llamacpp: repository %q contains Safetensors but no runnable model GGUF; choose a trusted GGUF conversion repository", ref.Repo)
		}
		return nil, nil, fmt.Errorf("llamacpp: no runnable model .gguf files found in %q", ref.Repo)
	}
	groups, err := groupModels(modelFiles)
	if err != nil {
		return nil, nil, err
	}
	selected, err := selectModelGroup(groups, ref)
	if err != nil {
		return nil, nil, err
	}

	var projector *hfSibling
	if ref.MMProjFile != "" {
		name, err := sanitizeGGUFName(ref.MMProjFile)
		if err != nil {
			return nil, nil, err
		}
		s, ok := byName[name]
		if !ok || !strings.Contains(strings.ToLower(name), "mmproj") {
			return nil, nil, fmt.Errorf("llamacpp: requested mmproj %q was not found as an mmproj GGUF in %q", name, ref.Repo)
		}
		projector = &s
	} else if len(mmprojFiles) == 1 {
		projector = &mmprojFiles[0]
	} else if len(mmprojFiles) > 1 {
		names := siblingNames(mmprojFiles)
		return nil, nil, fmt.Errorf("llamacpp: multiple mmproj GGUF files in %q (%s); pin one with MMProjFile", ref.Repo, strings.Join(names, ", "))
	}
	return selected.files, projector, nil
}

func groupModels(files []hfSibling) ([]artifactGroup, error) {
	groups := make(map[string]*artifactGroup)
	for _, s := range files {
		match := splitGGUF.FindStringSubmatch(s.RFilename)
		if match == nil {
			key := "whole:" + strings.ToLower(s.RFilename)
			groups[key] = &artifactGroup{key: key, files: []hfSibling{s}, primary: s.RFilename, display: s.RFilename}
			continue
		}
		total, _ := strconv.Atoi(match[3])
		index, _ := strconv.Atoi(match[2])
		if total < 1 || index < 1 || index > total {
			return nil, fmt.Errorf("llamacpp: invalid split GGUF filename %q", s.RFilename)
		}
		key := "split:" + strings.ToLower(match[1]) + ":" + match[3]
		g := groups[key]
		if g == nil {
			g = &artifactGroup{key: key, split: true, display: match[1] + "-*-of-" + match[3] + ".gguf"}
			groups[key] = g
		}
		g.files = append(g.files, s)
		if index == 1 {
			g.primary = s.RFilename
		}
	}
	out := make([]artifactGroup, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g.files, func(i, j int) bool { return g.files[i].RFilename < g.files[j].RFilename })
		if g.split {
			match := splitGGUF.FindStringSubmatch(g.files[0].RFilename)
			total, _ := strconv.Atoi(match[3])
			if len(g.files) != total || g.primary == "" {
				return nil, fmt.Errorf("llamacpp: split GGUF %q is incomplete: found %d of %d shards", g.display, len(g.files), total)
			}
			for i, f := range g.files {
				m := splitGGUF.FindStringSubmatch(f.RFilename)
				idx, _ := strconv.Atoi(m[2])
				if idx != i+1 {
					return nil, fmt.Errorf("llamacpp: split GGUF %q has missing or duplicate shard %d", g.display, i+1)
				}
			}
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].display < out[j].display })
	return out, nil
}

func selectModelGroup(groups []artifactGroup, ref Ref) (artifactGroup, error) {
	if ref.File != "" {
		name, err := sanitizeGGUFName(ref.File)
		if err != nil {
			return artifactGroup{}, err
		}
		for _, g := range groups {
			for _, f := range g.files {
				if f.RFilename == name {
					if quant := strings.TrimSpace(ref.Quant); quant != "" && !groupMatchesQuant(g, quant) {
						return artifactGroup{}, fmt.Errorf(
							"llamacpp: requested model file %q does not match requested quant %q",
							name, quant,
						)
					}
					return g, nil
				}
			}
		}
		return artifactGroup{}, fmt.Errorf("llamacpp: requested model file %q was not found", name)
	}
	quant := selectedQuant(ref.Quant)
	var matches []artifactGroup
	for _, g := range groups {
		if groupMatchesQuant(g, quant) {
			matches = append(matches, g)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	available := make([]string, 0, len(groups))
	for _, g := range groups {
		available = append(available, g.display)
	}
	return artifactGroup{}, fmt.Errorf("llamacpp: quant %q did not select exactly one GGUF set in %q (available: %s); pin Ref.File", quant, ref.Repo, strings.Join(available, ", "))
}

// groupMatchesQuant is the single quant-name predicate used for both selection
// and file/quant consistency. GGUF repositories encode the quantization as a
// filename token. Boundary matching prevents Q4_K_M from silently matching a
// different token such as IQ4_K_M or Q4_K_M_L.
func groupMatchesQuant(group artifactGroup, quant string) bool {
	quant = strings.TrimSpace(quant)
	if quant == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?i)(^|[-_.])` + regexp.QuoteMeta(quant) + `($|[-.])`)
	return pattern.MatchString(group.display)
}

func selectedQuant(quant string) string {
	if strings.TrimSpace(quant) == "" {
		return DefaultQuant
	}
	return strings.TrimSpace(quant)
}

// manifestQuant reports only a quantization that actually constrained or was
// validated against the selected artifact set. An exact File without Quant is
// sufficient selection and must not be mislabeled with the default quant.
func manifestQuant(ref Ref) string {
	if quant := strings.TrimSpace(ref.Quant); quant != "" {
		return quant
	}
	if strings.TrimSpace(ref.File) != "" {
		return ""
	}
	return DefaultQuant
}

func siblingNames(in []hfSibling) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.RFilename)
	}
	sort.Strings(out)
	return out
}

// Inspect returns a fresh remote plan and never requests artifact bodies.
func (d *Downloader) Inspect(ctx context.Context, ref Ref) (DownloadPlan, error) {
	m, err := d.ResolveManifest(ctx, ref)
	if err != nil {
		return DownloadPlan{}, err
	}
	return d.planManifest(ctx, m)
}

// Plan is an alias for Inspect for callers that prefer action-oriented naming.
func (d *Downloader) Plan(ctx context.Context, ref Ref) (DownloadPlan, error) {
	return d.Inspect(ctx, ref)
}

func (d *Downloader) planManifest(ctx context.Context, m Manifest) (DownloadPlan, error) {
	plan, _, err := d.planManifestWithValidation(ctx, m)
	return plan, err
}

/* planManifestWithValidation hashes each cached artifact once and returns the result for the commit phase. */
func (d *Downloader) planManifestWithValidation(ctx context.Context, m Manifest) (DownloadPlan, map[string]bool, error) {
	if d.ModelsDir == "" {
		return DownloadPlan{}, nil, errors.New("llamacpp: models directory is not configured")
	}
	cacheDir := d.manifestDir(m)
	var missing uint64
	valid := make(map[string]bool, len(m.Artifacts()))
	for _, a := range m.Artifacts() {
		path := filepath.Join(cacheDir, a.Filename)
		valid[a.Filename] = validCachedArtifact(path, a)
		if !valid[a.Filename] {
			var overflow bool
			missing, overflow = add64(missing, a.Size)
			if overflow {
				return DownloadPlan{}, nil, errors.New("llamacpp: artifact download size overflow")
			}
		}
	}
	host, err := d.profiler().Profile(ctx, d.ModelsDir)
	if err != nil {
		return DownloadPlan{}, nil, fmt.Errorf("llamacpp: host preflight failed: %w", err)
	}
	modelBytes, err := m.modelBytesChecked()
	if err != nil {
		return DownloadPlan{}, nil, err
	}
	report := EstimateFitWithPolicy(host, FitRequest{
		ModelBytes:    modelBytes,
		MMProjBytes:   m.MMProjBytes(),
		DownloadBytes: missing,
		ContextTokens: d.ContextTokens,
		Parallel:      d.Parallel,
	}, d.policy())
	return DownloadPlan{Manifest: cloneManifest(m), Host: host, Fit: report, CacheDir: cacheDir}, valid, nil
}

// EnsureLocal preserves the original single-path API.
func (d *Downloader) EnsureLocal(ctx context.Context, ref Ref) (string, error) {
	local, err := d.EnsureArtifacts(ctx, ref)
	if err != nil {
		return "", err
	}
	return local.Model, nil
}

// EnsureArtifacts resolves, preflights, downloads, verifies, and atomically
// commits the complete selected artifact set. The fit check is repeated under
// a models-directory acquisition lock and a per-manifest filesystem lock
// immediately before the first artifact GET. Both locks cross process boundaries.
func (d *Downloader) EnsureArtifacts(ctx context.Context, ref Ref) (LocalArtifacts, error) {
	// A complete SHA-256-validated pinned cache is usable fully offline. Validate
	// once under the per-manifest lock and carry those results through to path
	// construction; hashing a multi-gigabyte model four times on every launch
	// would turn the integrity boundary into an avoidable startup bottleneck.
	if cached, cacheErr := d.loadCachedManifest(ref); cacheErr == nil {
		cacheDir := d.manifestDir(cached)
		lock, err := acquireManifestLock(ctx, cacheDir)
		if err != nil {
			return LocalArtifacts{}, err
		}
		plan, valid, err := d.planManifestWithValidation(ctx, cached)
		if err != nil {
			_ = lock.Close()
			return LocalArtifacts{}, err
		}
		if allArtifactsValid(cached, valid) {
			if !plan.Fit.Fits {
				_ = lock.Close()
				return LocalArtifacts{}, &FitError{Report: plan.Fit}
			}
			d.log().Info("llamacpp: using complete pinned artifact cache", "repo", ref.Repo, "commit", cached.CommitSHA)
			local, err := localArtifactPaths(plan, cacheDir)
			closeErr := lock.Close()
			if err == nil {
				err = closeErr
			}
			return local, err
		}
		if err := lock.Close(); err != nil {
			return LocalArtifacts{}, fmt.Errorf("llamacpp: release cached-manifest lock: %w", err)
		}
	}

	manifest, err := d.ResolveManifest(ctx, ref)
	resolvedRemotely := err == nil
	if err != nil {
		cached, cacheErr := d.loadCachedManifest(ref)
		if cacheErr != nil {
			return LocalArtifacts{}, err
		}
		d.log().Warn("llamacpp: metadata unavailable; using previously pinned manifest", "repo", ref.Repo, "commit", cached.CommitSHA, "err", err)
		manifest = cached
	}

	if err := ensureLockDirectory(d.ModelsDir, false); err != nil {
		return LocalArtifacts{}, err
	}
	acquisitionLock, err := acquireLockInDirectory(ctx, d.ModelsDir, ".agent-smith-acquisition.lock")
	if err != nil {
		return LocalArtifacts{}, fmt.Errorf("llamacpp: acquire models-directory admission lock: %w", err)
	}
	defer acquisitionLock.Close()

	cacheDir := d.manifestDir(manifest)
	lock, err := acquireManifestLock(ctx, cacheDir)
	if err != nil {
		return LocalArtifacts{}, err
	}
	defer lock.Close()
	if resolvedRemotely {
		if err := d.saveCachedManifest(ref, manifest); err != nil {
			return LocalArtifacts{}, err
		}
	}

	plan, valid, err := d.planManifestWithValidation(ctx, manifest)
	if err != nil {
		return LocalArtifacts{}, err
	}
	if !plan.Fit.Fits {
		return LocalArtifacts{}, &FitError{Report: plan.Fit}
	}
	type stagedArtifact struct {
		artifact Artifact
		path     string
		stable   string
	}
	var staged []stagedArtifact
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, item := range staged {
			_ = restorePartial(item.path, item.stable)
		}
	}()
	for _, a := range manifest.Artifacts() {
		dest := filepath.Join(cacheDir, a.Filename)
		if valid[a.Filename] {
			continue
		}
		stable := dest + ".part"
		stage, err := claimPartial(stable)
		if err != nil {
			return LocalArtifacts{}, fmt.Errorf("llamacpp: prepare private staging file for %q: %w", a.Filename, err)
		}
		item := stagedArtifact{artifact: a, path: stage, stable: stable}
		staged = append(staged, item)
		if err := d.downloadArtifact(ctx, a, stage); err != nil {
			return LocalArtifacts{}, err
		}
	}
	// Nothing becomes visible at its final path until the entire missing set has
	// been downloaded and hash-validated.
	for _, item := range staged {
		dest := filepath.Join(cacheDir, item.artifact.Filename)
		if err := commitStagedFile(item.path, dest); err != nil {
			return LocalArtifacts{}, fmt.Errorf("llamacpp: commit artifact %q: %w", item.artifact.Filename, err)
		}
	}
	if err := syncDirectory(cacheDir); err != nil {
		return LocalArtifacts{}, err
	}
	if err := verifyManifestDirectory(cacheDir, manifest); err != nil {
		return LocalArtifacts{}, fmt.Errorf("llamacpp: post-commit artifact verification failed: %w", err)
	}
	committed = true
	return localArtifactPaths(plan, cacheDir)
}

func allArtifactsValid(m Manifest, valid map[string]bool) bool {
	if len(m.ModelArtifacts) == 0 {
		return false
	}
	for _, a := range m.Artifacts() {
		if !valid[a.Filename] {
			return false
		}
	}
	return true
}

func acquireManifestLock(ctx context.Context, cacheDir string) (*fileLock, error) {
	if err := ensureLockDirectory(cacheDir, true); err != nil {
		return nil, fmt.Errorf("llamacpp: prepare private artifact cache: %w", err)
	}
	lock, err := acquireLockInDirectory(ctx, cacheDir, ".agent-smith-manifest.lock")
	if err != nil {
		return nil, fmt.Errorf("llamacpp: acquire artifact-manifest lock: %w", err)
	}
	return lock, nil
}

// ensureLockDirectory creates only directories and rejects a terminal symlink.
// Cache directories are private; an operator-provided model root is left at its
// existing mode, but a newly created root is private by default.
func ensureLockDirectory(path string, private bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("llamacpp: lock directory is empty")
	}
	_, beforeErr := os.Lstat(path)
	created := errors.Is(beforeErr, os.ErrNotExist)
	if beforeErr != nil && !created {
		return beforeErr
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("llamacpp: lock directory %q is not a real directory", path)
	}
	if private || created {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("llamacpp: secure directory %q: %w", path, err)
		}
	}
	return nil
}

func acquireLockInDirectory(ctx context.Context, dir, name string) (*fileLock, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("llamacpp: resolve lock directory %q: %w", dir, err)
	}
	return acquireFileLock(ctx, filepath.Join(canonical, name))
}

// claimPartial moves a resumable stable partial to a transaction-unique name,
// or creates a new unique private stage. The caller holds the filesystem locks.
func claimPartial(stable string) (string, error) {
	dir := filepath.Dir(stable)
	stageFile, err := os.CreateTemp(dir, "."+filepath.Base(stable)+"-stage-")
	if err != nil {
		return "", err
	}
	stage := stageFile.Name()
	if err := stageFile.Chmod(0o600); err != nil {
		_ = stageFile.Close()
		_ = os.Remove(stage)
		return "", err
	}
	if err := stageFile.Close(); err != nil {
		_ = os.Remove(stage)
		return "", err
	}

	fi, statErr := os.Lstat(stable)
	switch {
	case statErr == nil:
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			_ = os.Remove(stage)
			return "", fmt.Errorf("partial path %q is not a regular non-symlink file", stable)
		}
		if err := os.Chmod(stable, 0o600); err != nil {
			_ = os.Remove(stage)
			return "", err
		}
		if err := os.Remove(stage); err != nil {
			return "", err
		}
		if err := os.Rename(stable, stage); err != nil {
			return "", err
		}
	case errors.Is(statErr, os.ErrNotExist):
		// The exclusive CreateTemp file is the new empty stage.
	default:
		_ = os.Remove(stage)
		return "", statErr
	}
	return stage, nil
}

func restorePartial(stage, stable string) error {
	fi, err := os.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return fmt.Errorf("staging path %q is not a regular non-symlink file", stage)
	}
	if _, err := os.Lstat(stable); err == nil {
		return fmt.Errorf("refusing to replace unexpected partial path %q", stable)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(stage, stable)
}

func commitStagedFile(stage, dest string) error {
	fi, err := os.Lstat(stage)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return fmt.Errorf("staging path %q is not a regular non-symlink file", stage)
	}
	if destInfo, err := os.Lstat(dest); err == nil {
		if destInfo.Mode()&os.ModeSymlink != 0 || !destInfo.Mode().IsRegular() {
			return fmt.Errorf("destination %q is not a regular non-symlink file", dest)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o600)
}

func verifyManifestDirectory(cacheDir string, m Manifest) error {
	for _, artifact := range m.Artifacts() {
		if err := verifyArtifact(filepath.Join(cacheDir, artifact.Filename), artifact, true); err != nil {
			return err
		}
	}
	return nil
}

// verifyLocalArtifactsAgainstManifest binds the final pre-exec check to the
// exact remote manifest. Local operator-supplied files have an empty manifest
// and are validated by InspectLocal instead.
func verifyLocalArtifactsAgainstManifest(local LocalArtifacts) error {
	if len(local.Manifest.ModelArtifacts) == 0 {
		return nil
	}
	if len(local.ModelFiles) != len(local.Manifest.ModelArtifacts) {
		return errors.New("llamacpp: committed model paths do not match the remote manifest")
	}
	for i, artifact := range local.Manifest.ModelArtifacts {
		path := local.ModelFiles[i]
		if filepath.Base(path) != artifact.Filename {
			return fmt.Errorf("llamacpp: committed model path %q does not match manifest artifact %q", path, artifact.Filename)
		}
		if err := verifyArtifact(path, artifact, true); err != nil {
			return err
		}
	}
	if local.Manifest.MMProjArtifact == nil {
		if local.MMProj != "" {
			return errors.New("llamacpp: unexpected projector path outside the remote manifest")
		}
		return nil
	}
	if local.MMProj == "" || filepath.Base(local.MMProj) != local.Manifest.MMProjArtifact.Filename {
		return errors.New("llamacpp: committed projector path does not match the remote manifest")
	}
	return verifyArtifact(local.MMProj, *local.Manifest.MMProjArtifact, true)
}

/* localArtifactPaths is called only after this lock holder validated or downloaded every manifest artifact. */
func localArtifactPaths(plan DownloadPlan, cacheDir string) (LocalArtifacts, error) {
	local := LocalArtifacts{Manifest: cloneManifest(plan.Manifest), Fit: plan.Fit}
	for _, a := range plan.Manifest.ModelArtifacts {
		path := filepath.Join(cacheDir, a.Filename)
		local.ModelFiles = append(local.ModelFiles, path)
	}
	if len(local.ModelFiles) == 0 {
		return LocalArtifacts{}, errors.New("llamacpp: manifest has no committed model artifacts")
	}
	local.Model = local.ModelFiles[0]
	if plan.Manifest.MMProjArtifact != nil {
		path := filepath.Join(cacheDir, plan.Manifest.MMProjArtifact.Filename)
		local.MMProj = path
	}
	return local, nil
}

func (d *Downloader) downloadArtifact(ctx context.Context, artifact Artifact, part string) error {
	var offset uint64
	if fi, err := os.Lstat(part); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return fmt.Errorf("llamacpp: staging path %q is not a regular non-symlink file", part)
		}
		if uint64(fi.Size()) <= artifact.Size {
			offset = uint64(fi.Size())
		} else if err := os.Truncate(part, 0); err != nil {
			return fmt.Errorf("llamacpp: reset oversized partial %q: %w", artifact.Filename, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("llamacpp: inspect staging file %q: %w", part, err)
	}
	if offset == artifact.Size {
		if err := verifyArtifact(part, artifact, true); err == nil {
			return nil
		}
		if err := os.Truncate(part, 0); err != nil {
			return fmt.Errorf("llamacpp: reset invalid partial %q: %w", artifact.Filename, err)
		}
		offset = 0
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return fmt.Errorf("llamacpp: build artifact request: %w", err)
	}
	d.authorize(req)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("llamacpp: download %q: %w", artifact.Filename, err)
	}
	defer resp.Body.Close()
	appendMode := false
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		if err := validateContentRange(resp.Header.Get("Content-Range"), offset, artifact.Size); err != nil {
			return fmt.Errorf("llamacpp: resume %q: %w", artifact.Filename, err)
		}
		appendMode = true
	case resp.StatusCode == http.StatusOK:
		offset = 0 // Range ignored: safely restart rather than append.
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return fmt.Errorf("llamacpp: download %q returned unexpected HTTP %d", artifact.Filename, resp.StatusCode)
	default:
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("llamacpp: download %q: http %d: %s", artifact.Filename, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	expectedBody := artifact.Size - offset
	if resp.ContentLength >= 0 && uint64(resp.ContentLength) != expectedBody {
		return fmt.Errorf("llamacpp: download %q Content-Length=%d, expected %d", artifact.Filename, resp.ContentLength, expectedBody)
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return fmt.Errorf("llamacpp: open partial %q: %w", artifact.Filename, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("llamacpp: secure partial %q: %w", artifact.Filename, err)
	}
	d.log().Info("llamacpp: downloading verified artifact", "file", artifact.Filename, "bytes", artifact.Size, "resume_from", offset)
	written, copyErr := io.Copy(f, io.LimitReader(resp.Body, int64(expectedBody)+1))
	if copyErr == nil && uint64(written) != expectedBody {
		copyErr = fmt.Errorf("received %d bytes, expected %d", written, expectedBody)
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("llamacpp: write %q: %w", artifact.Filename, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("llamacpp: close %q: %w", artifact.Filename, closeErr)
	}
	if err := verifyArtifact(part, artifact, true); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

func validateContentRange(value string, offset, total uint64) error {
	var start, end, gotTotal uint64
	if _, err := fmt.Sscanf(value, "bytes %d-%d/%d", &start, &end, &gotTotal); err != nil {
		return fmt.Errorf("invalid Content-Range %q", value)
	}
	if start != offset || gotTotal != total || end != total-1 || end < start {
		return fmt.Errorf("Content-Range %q does not match requested offset %d and total %d", value, offset, total)
	}
	return nil
}

func validCachedArtifact(path string, a Artifact) bool {
	// Size and GGUF magic are not sufficient for an offline trust decision: a
	// same-length corruption or replacement would otherwise bypass the pinned
	// manifest. Cache reuse therefore authenticates the complete file against
	// its immutable LFS SHA-256 before skipping network access.
	return verifyArtifact(path, a, true) == nil
}

func verifyArtifact(path string, a Artifact, verifyHash bool) error {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("llamacpp: artifact %q is not a regular non-symlink file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(pathInfo, fi) {
		return fmt.Errorf("llamacpp: artifact %q changed while being opened", path)
	}
	if !fi.Mode().IsRegular() || fi.Size() < 4 || uint64(fi.Size()) != a.Size {
		return fmt.Errorf("llamacpp: artifact %q has size %d, expected %d", path, fi.Size(), a.Size)
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return fmt.Errorf("llamacpp: artifact %q is not a GGUF file", path)
	}
	if !verifyHash {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("llamacpp: hash %q: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, a.SHA256) {
		return fmt.Errorf("llamacpp: artifact %q SHA256 mismatch: got %s, expected %s", a.Filename, got, a.SHA256)
	}
	return nil
}

func (d *Downloader) manifestDir(m Manifest) string {
	return filepath.Join(d.ModelsDir, repoDir(m.Repo), m.CommitSHA)
}

func (d *Downloader) cachedManifestPath(ref Ref) string {
	revision := ref.Revision
	if revision == "" {
		revision = "main"
	}
	quantKey := "implicit"
	if quant := strings.TrimSpace(ref.Quant); quant != "" {
		quantKey = "explicit:" + quant
	}
	keyRaw := strings.Join([]string{ref.Repo, revision, quantKey, ref.File, ref.MMProjFile}, "\x00")
	key := sha256.Sum256([]byte(keyRaw))
	return filepath.Join(d.ModelsDir, repoDir(ref.Repo), ".manifests", hex.EncodeToString(key[:16])+".json")
}

func (d *Downloader) saveCachedManifest(ref Ref, m Manifest) error {
	path := d.cachedManifestPath(ref)
	if err := ensureLockDirectory(filepath.Dir(path), true); err != nil {
		return fmt.Errorf("llamacpp: create manifest cache: %w", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("llamacpp: write manifest cache: %w", err)
	}
	if existing, statErr := os.Lstat(path); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			_ = os.Remove(tmp)
			return errors.New("llamacpp: cached manifest destination is not a regular non-symlink file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = os.Remove(tmp)
		return statErr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("llamacpp: commit manifest cache: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("llamacpp: secure manifest cache: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func (d *Downloader) loadCachedManifest(ref Ref) (Manifest, error) {
	path := d.cachedManifestPath(ref)
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return Manifest{}, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return Manifest{}, errors.New("llamacpp: cached manifest path is not a regular non-symlink file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	openedInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return Manifest{}, err
	}
	if !os.SameFile(pathInfo, openedInfo) {
		_ = f.Close()
		return Manifest{}, errors.New("llamacpp: cached manifest changed while being opened")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16<<20))
	closeErr := f.Close()
	if err != nil {
		return Manifest{}, err
	}
	if closeErr != nil {
		return Manifest{}, closeErr
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, err
	}
	if m.Repo != ref.Repo || !validHex(m.CommitSHA, 40, 64) || len(m.ModelArtifacts) == 0 {
		return Manifest{}, errors.New("llamacpp: cached manifest is invalid")
	}
	if err := validateCachedManifestSelection(ref, m); err != nil {
		return Manifest{}, err
	}
	seen := make(map[string]struct{}, len(m.Artifacts()))
	for i, a := range m.ModelArtifacts {
		if err := d.validateCachedArtifact(m, a, ArtifactModel, seen); err != nil {
			return Manifest{}, err
		}
		m.ModelArtifacts[i] = a
	}
	if m.MMProjArtifact != nil {
		if err := d.validateCachedArtifact(m, *m.MMProjArtifact, ArtifactMMProj, seen); err != nil {
			return Manifest{}, err
		}
	}
	return cloneManifest(m), nil
}

/* validateCachedManifestSelection prevents a valid artifact cache from weakening a later, stricter selector. */
func validateCachedManifestSelection(ref Ref, m Manifest) error {
	revision := ref.Revision
	if revision == "" {
		revision = "main"
	}
	if m.RequestedRevision != revision {
		return errors.New("llamacpp: cached manifest revision does not match the request")
	}
	explicitQuant := strings.TrimSpace(ref.Quant)
	if ref.File != "" {
		name, err := sanitizeGGUFName(ref.File)
		if err != nil {
			return err
		}
		found := false
		for _, artifact := range m.ModelArtifacts {
			if artifact.Filename == name {
				found = true
				break
			}
		}
		if !found {
			return errors.New("llamacpp: cached manifest does not contain the requested model file")
		}
		if explicitQuant == "" {
			if m.Quant != "" {
				return errors.New("llamacpp: cached file-only manifest has an unexpected quant selector")
			}
		} else if !strings.EqualFold(m.Quant, explicitQuant) || !groupMatchesQuant(artifactGroup{display: name}, explicitQuant) {
			return errors.New("llamacpp: cached manifest model file does not satisfy the requested quant")
		}
	} else {
		expectedQuant := selectedQuant(ref.Quant)
		if !strings.EqualFold(m.Quant, expectedQuant) {
			return errors.New("llamacpp: cached manifest quant does not match the request")
		}
		matched := false
		for _, artifact := range m.ModelArtifacts {
			if groupMatchesQuant(artifactGroup{display: artifact.Filename}, expectedQuant) {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("llamacpp: cached manifest artifacts do not satisfy the requested quant")
		}
	}
	if ref.MMProjFile != "" {
		name, err := sanitizeGGUFName(ref.MMProjFile)
		if err != nil {
			return err
		}
		if m.MMProjArtifact == nil || m.MMProjArtifact.Filename != name {
			return errors.New("llamacpp: cached manifest projector does not match the request")
		}
	}
	return nil
}

/* validateCachedArtifact prevents a tampered manifest from escaping the cache or receiving the HF token. */
func (d *Downloader) validateCachedArtifact(m Manifest, a Artifact, wantKind ArtifactKind, seen map[string]struct{}) error {
	name, err := sanitizeGGUFName(a.Filename)
	if err != nil || name != a.Filename || a.Kind != wantKind {
		return errors.New("llamacpp: cached manifest artifact has an unsafe filename or kind")
	}
	if _, duplicate := seen[name]; duplicate {
		return errors.New("llamacpp: cached manifest contains duplicate artifact filenames")
	}
	seen[name] = struct{}{}
	expectedURL := fmt.Sprintf("%s/%s/resolve/%s/%s", d.base(), m.Repo, m.CommitSHA, url.PathEscape(name))
	if a.Size == 0 || !validHex(a.SHA256, 64, 64) || a.URL != expectedURL {
		return errors.New("llamacpp: cached manifest artifact is invalid or not commit-pinned")
	}
	return nil
}

func cloneManifest(m Manifest) Manifest {
	out := m
	out.ModelArtifacts = append([]Artifact(nil), m.ModelArtifacts...)
	if m.MMProjArtifact != nil {
		a := *m.MMProjArtifact
		out.MMProjArtifact = &a
	}
	return out
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("llamacpp: open directory for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("llamacpp: sync directory: %w", err)
	}
	return nil
}

func (d *Downloader) authorize(req *http.Request) {
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
}

func sanitizeGGUFName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", errors.New("llamacpp: empty model filename")
	}
	if strings.ContainsAny(n, `/\\`) || n != filepath.Base(n) || strings.Contains(n, "..") {
		return "", fmt.Errorf("llamacpp: unsafe model filename %q", name)
	}
	if !strings.HasSuffix(strings.ToLower(n), ".gguf") {
		return "", fmt.Errorf("llamacpp: model filename %q is not a .gguf", name)
	}
	return n, nil
}

func repoDir(repo string) string {
	return strings.ReplaceAll(strings.Trim(repo, "/"), "/", "_")
}

func validHex(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func isCommitRevision(revision string) bool {
	return validHex(revision, 40, 40) || validHex(revision, 64, 64)
}

// selectByQuant is retained for compatibility with existing package tests.
// It follows the same strict token and ambiguity rules as selectModelGroup.
func selectByQuant(candidates []string, quant string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	var matches []string
	for _, candidate := range candidates {
		if groupMatchesQuant(artifactGroup{display: candidate}, selectedQuant(quant)) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}
