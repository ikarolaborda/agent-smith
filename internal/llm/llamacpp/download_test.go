package llamacpp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func TestParseRef(t *testing.T) {
	cases := map[string]Ref{
		"hf.co/user/name":          {Repo: "user/name", Revision: "main"},
		"huggingface.co/user/name": {Repo: "user/name", Revision: "main"},
		"user/name":                {Repo: "user/name", Revision: "main"},
		"user/name:Q5_K_M":         {Repo: "user/name", Quant: "Q5_K_M", Revision: "main"},
	}
	for input, want := range cases {
		got, err := ParseRef(input)
		if err != nil || got != want {
			t.Fatalf("ParseRef(%q) = %+v, %v; want %+v", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "onlyname", "a/b/c", ":Q4", "/"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) expected error", bad)
		}
	}
}

type hfFixture struct {
	files         map[string][]byte
	extraSiblings []string
	hashOverrides map[string]string
	sizeOverrides map[string]int64
	artifactGets  atomic.Int32
	metadataGets  atomic.Int32
}

func newFakeHF(t *testing.T, fixture *hfFixture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		fixture.metadataGets.Add(1)
		if r.URL.Query().Get("blobs") != "true" {
			t.Errorf("metadata request did not set blobs=true: %s", r.URL.String())
		}
		type lfs struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
		}
		type sibling struct {
			RFilename string `json:"rfilename"`
			Size      int64  `json:"size,omitempty"`
			LFS       *lfs   `json:"lfs,omitempty"`
		}
		var siblings []sibling
		for name, blob := range fixture.files {
			sum := sha256.Sum256(blob)
			hash := hex.EncodeToString(sum[:])
			if override := fixture.hashOverrides[name]; override != "" {
				hash = override
			}
			size := int64(len(blob))
			if override, ok := fixture.sizeOverrides[name]; ok {
				size = override
			}
			siblings = append(siblings, sibling{RFilename: name, Size: size, LFS: &lfs{SHA256: hash, Size: size}})
		}
		for _, name := range fixture.extraSiblings {
			siblings = append(siblings, sibling{RFilename: name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": testCommit, "siblings": siblings})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/resolve/"+testCommit+"/") {
			http.NotFound(w, r)
			return
		}
		fixture.artifactGets.Add(1)
		name := filepath.Base(r.URL.Path)
		blob, ok := fixture.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" {
			var start int
			_, _ = fmt.Sscanf(rng, "bytes=%d-", &start)
			if start < len(blob) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(blob)-1, len(blob)))
				w.Header().Set("Content-Length", fmt.Sprint(len(blob)-start))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(blob[start:])
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
		_, _ = w.Write(blob)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func ampleProfiler() Profiler {
	return ProfilerFunc(func(context.Context, string) (HostProfile, error) {
		return HostProfile{
			OS: "test", Arch: "test", TotalMemoryBytes: 64 * byteGiB,
			AvailableMemoryBytes: 48 * byteGiB, FreeDiskBytes: 100 * byteGiB,
		}, nil
	})
}

func testDownloader(base, dir string, profiler Profiler) *Downloader {
	return &Downloader{Base: base, ModelsDir: dir, Profiler: profiler, FitPolicy: DefaultFitPolicy()}
}

func TestResolveQuant(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q4_K_M.gguf": []byte("GGUF-q4"),
		"model-Q8_0.gguf":   []byte("GGUF-q8"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	got, err := d.Resolve(context.Background(), Ref{Repo: "u/n", Quant: "Q8_0"})
	if err != nil || got != "model-Q8_0.gguf" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	got, err = d.Resolve(context.Background(), Ref{Repo: "u/n"})
	if err != nil || got != "model-Q4_K_M.gguf" {
		t.Fatalf("Resolve default = %q, %v", got, err)
	}
}

func TestResolveDoesNotSubstituteAnExplicitMissingQuant(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": []byte("GGUF-q4")}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	if _, err := d.Resolve(context.Background(), Ref{Repo: "u/n", Quant: "Q8_0"}); err == nil {
		t.Fatal("explicit Q8_0 request must not silently select the only Q4 artifact")
	}
}

func TestResolveManifestEnforcesFileQuantConsistency(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q4_K_M.gguf": []byte("GGUF-q4"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())

	_, err := d.ResolveManifest(context.Background(), Ref{
		Repo:  "u/n",
		File:  "model-Q4_K_M.gguf",
		Quant: "Q8_0",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match requested quant") {
		t.Fatalf("file/quant mismatch must be rejected, got %v", err)
	}
	if fixture.artifactGets.Load() != 0 {
		t.Fatal("selector mismatch must fail before any artifact GET")
	}

	manifest, err := d.ResolveManifest(context.Background(), Ref{
		Repo:  "u/n",
		File:  "model-Q4_K_M.gguf",
		Quant: "Q4_K_M",
	})
	if err != nil {
		t.Fatalf("matching file/quant: %v", err)
	}
	if manifest.Quant != "Q4_K_M" || len(manifest.ModelArtifacts) != 1 || manifest.ModelArtifacts[0].Filename != "model-Q4_K_M.gguf" {
		t.Fatalf("matching manifest = %#v", manifest)
	}

	fileOnly, err := d.ResolveManifest(context.Background(), Ref{
		Repo: "u/n",
		File: "model-Q4_K_M.gguf",
	})
	if err != nil {
		t.Fatalf("exact file without quant: %v", err)
	}
	if fileOnly.Quant != "" {
		t.Fatalf("file-only manifest quant = %q, want empty rather than an unvalidated default", fileOnly.Quant)
	}
}

func TestCachedFileOnlyManifestCannotBypassLaterExplicitQuant(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q8_0.gguf": []byte("GGUF-q8"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	fileOnly := Ref{Repo: "u/n", File: "model-Q8_0.gguf"}
	if _, err := d.EnsureArtifacts(context.Background(), fileOnly); err != nil {
		t.Fatalf("cache file-only artifact: %v", err)
	}
	explicitMismatch := Ref{Repo: "u/n", File: "model-Q8_0.gguf", Quant: "Q4_K_M"}
	if _, err := d.EnsureArtifacts(context.Background(), explicitMismatch); err == nil {
		t.Fatal("explicit Q4 selector reused a file-only Q8 cache")
	}
	if got := fixture.artifactGets.Load(); got != 1 {
		t.Fatalf("selector mismatch performed %d artifact GETs, want only the original cache fill", got)
	}

	manifest, err := d.ResolveManifest(context.Background(), fileOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.saveCachedManifest(explicitMismatch, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := d.loadCachedManifest(explicitMismatch); err == nil || !strings.Contains(err.Error(), "quant") {
		t.Fatalf("mismatched cached selectors were accepted: %v", err)
	}
}

func TestResolveManifestDoesNotSubstituteLoneNonDefaultQuant(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q8_0.gguf": []byte("GGUF-q8"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())

	if _, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n"}); err == nil {
		t.Fatal("an implicit Q4_K_M request must not silently select the lone Q8_0 artifact")
	}
	manifest, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", Quant: "Q8_0"})
	if err != nil {
		t.Fatalf("explicit Q8_0 selection: %v", err)
	}
	if manifest.Quant != "Q8_0" || len(manifest.ModelArtifacts) != 1 || manifest.ModelArtifacts[0].Filename != "model-Q8_0.gguf" {
		t.Fatalf("explicit manifest = %#v", manifest)
	}
	if fixture.artifactGets.Load() != 0 {
		t.Fatal("manifest resolution must not perform artifact GETs")
	}
}

func TestResolveManifestMatchesQuantAsACompleteFilenameToken(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q4_K_M_L.gguf": []byte("GGUF-q4-long"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())

	if _, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", Quant: "Q4_K_M"}); err == nil {
		t.Fatal("Q4_K_M must not match the different Q4_K_M_L filename token")
	}
	manifest, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", Quant: "Q4_K_M_L"})
	if err != nil {
		t.Fatalf("exact quant token: %v", err)
	}
	if manifest.Quant != "Q4_K_M_L" || manifest.ModelArtifacts[0].Filename != "model-Q4_K_M_L.gguf" {
		t.Fatalf("exact-token manifest = %#v", manifest)
	}
}

func TestResolveManifestRejectsMultipleSetsForOneQuant(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"model-Q4_K_M.gguf":                []byte("GGUF-whole"),
		"model-Q4_K_M-00001-of-00002.gguf": []byte("GGUF-shard-one"),
		"model-Q4_K_M-00002-of-00002.gguf": []byte("GGUF-shard-two"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())

	if _, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", Quant: "Q4_K_M"}); err == nil {
		t.Fatal("one quant matching whole and split sets must be rejected as ambiguous")
	}
	whole, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", File: "model-Q4_K_M.gguf", Quant: "Q4_K_M"})
	if err != nil {
		t.Fatalf("pinned whole file: %v", err)
	}
	if len(whole.ModelArtifacts) != 1 || whole.ModelArtifacts[0].Filename != "model-Q4_K_M.gguf" {
		t.Fatalf("pinned whole manifest = %#v", whole)
	}
	split, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", File: "model-Q4_K_M-00001-of-00002.gguf", Quant: "Q4_K_M"})
	if err != nil {
		t.Fatalf("pinned split file: %v", err)
	}
	if len(split.ModelArtifacts) != 2 {
		t.Fatalf("pinned split manifest has %d shards, want 2", len(split.ModelArtifacts))
	}
}

func TestResolveSplitAndMMProj(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{
		"m-Q4_K_M-00001-of-00002.gguf": []byte("GGUF-shard-one"),
		"m-Q4_K_M-00002-of-00002.gguf": []byte("GGUF-shard-two"),
		"mmproj-model-f16.gguf":        []byte("GGUF-projector"),
	}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	m, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ModelArtifacts) != 2 || m.MMProjArtifact == nil {
		t.Fatalf("manifest models=%d mmproj=%v", len(m.ModelArtifacts), m.MMProjArtifact)
	}
	for _, artifact := range m.Artifacts() {
		if !strings.Contains(artifact.URL, "/resolve/"+testCommit+"/") {
			t.Fatalf("artifact URL is not commit-pinned: %s", artifact.URL)
		}
	}
	local, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(local.ModelFiles) != 2 || local.MMProj == "" {
		t.Fatalf("local artifacts = %+v", local)
	}
}

func TestResolveRejectsSafetensorsOnly(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{}, extraSiblings: []string{"model.safetensors"}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	_, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "safetensors") {
		t.Fatalf("expected clear Safetensors rejection, got %v", err)
	}
}

func TestResolveRejectsImmutableCommitMismatch(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": []byte("GGUF-model")}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	requested := strings.Repeat("a", 40)
	_, err := d.ResolveManifest(context.Background(), Ref{Repo: "u/n", Revision: requested})
	if err == nil || !strings.Contains(err.Error(), "unexpected commit") {
		t.Fatalf("expected immutable revision mismatch, got %v", err)
	}
}

func TestPreflightBlocksBeforeArtifactGET(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile HostProfile
	}{
		{"memory", HostProfile{OS: "test", Arch: "test", TotalMemoryBytes: 2 * byteGiB, AvailableMemoryBytes: byteGiB, FreeDiskBytes: 100 * byteGiB}},
		{"disk", HostProfile{OS: "test", Arch: "test", TotalMemoryBytes: 64 * byteGiB, AvailableMemoryBytes: 48 * byteGiB, FreeDiskBytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &hfFixture{files: map[string][]byte{"m-Q4_K_M.gguf": []byte("GGUF-model")}}
			srv := newFakeHF(t, fixture)
			profiler := ProfilerFunc(func(context.Context, string) (HostProfile, error) { return tc.profile, nil })
			d := testDownloader(srv.URL, t.TempDir(), profiler)
			_, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
			var fitErr *FitError
			if !errors.As(err, &fitErr) {
				t.Fatalf("expected FitError, got %v", err)
			}
			if got := fixture.artifactGets.Load(); got != 0 {
				t.Fatalf("preflight made %d artifact GETs", got)
			}
		})
	}
}

func TestDownloadVerifiesChecksumAndSize(t *testing.T) {
	badHash := strings.Repeat("0", 64)
	for _, tc := range []struct {
		name string
		fix  *hfFixture
	}{
		{"checksum", &hfFixture{files: map[string][]byte{"m-Q4_K_M.gguf": []byte("GGUF-model")}, hashOverrides: map[string]string{"m-Q4_K_M.gguf": badHash}}},
		{"size", &hfFixture{files: map[string][]byte{"m-Q4_K_M.gguf": []byte("GGUF-model")}, sizeOverrides: map[string]int64{"m-Q4_K_M.gguf": 999}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeHF(t, tc.fix)
			d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
			if _, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"}); err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}

func TestDownloadAndSizeValidatedReuse(t *testing.T) {
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": []byte("GGUF-verified-weights")}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	first, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	gets := fixture.artifactGets.Load()
	metadataGets := fixture.metadataGets.Load()
	second, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
	if err != nil || second.Model != first.Model {
		t.Fatalf("reuse = %+v, %v", second, err)
	}
	if fixture.artifactGets.Load() != gets {
		t.Fatal("size-valid cache reuse performed another artifact GET")
	}
	if fixture.metadataGets.Load() != metadataGets {
		t.Fatal("complete pinned cache reuse performed a metadata GET")
	}
	if _, err := os.Stat(first.Model); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(first.Model)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("cached artifact is not private: mode=%v", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(first.Model))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("artifact cache directory is not private: mode=%v", dirInfo.Mode().Perm())
	}
}

func TestSameSizeCacheCorruptionIsNotReused(t *testing.T) {
	blob := []byte("GGUF-verified-weights")
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": blob}}
	srv := newFakeHF(t, fixture)
	d := testDownloader(srv.URL, t.TempDir(), ampleProfiler())
	local, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), blob...)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := os.WriteFile(local.Model, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	before := fixture.artifactGets.Load()
	repaired, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.artifactGets.Load() != before+1 {
		t.Fatal("same-size corrupted cache was reused instead of re-fetched")
	}
	got, err := os.ReadFile(repaired.Model)
	if err != nil || string(got) != string(blob) {
		t.Fatalf("repaired cache = %q, %v", got, err)
	}
}

func TestCachedManifestCannotRedirectTokenOrEscapeCache(t *testing.T) {
	d := testDownloader("https://huggingface.example", t.TempDir(), ampleProfiler())
	ref := Ref{Repo: "u/n", Revision: "main"}
	base := Manifest{
		Repo:              ref.Repo,
		RequestedRevision: ref.Revision,
		CommitSHA:         testCommit,
		ModelArtifacts: []Artifact{{
			Kind: ArtifactModel, Filename: "model-Q4_K_M.gguf", Size: 8,
			SHA256: strings.Repeat("a", 64),
			URL:    "https://huggingface.example/u/n/resolve/" + testCommit + "/model-Q4_K_M.gguf",
		}},
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) {
			m.ModelArtifacts[0].URL = "https://attacker.example/resolve/" + testCommit + "/model-Q4_K_M.gguf"
		},
		func(m *Manifest) { m.ModelArtifacts[0].Filename = "../outside.gguf" },
	} {
		manifest := cloneManifest(base)
		mutate(&manifest)
		if err := d.saveCachedManifest(ref, manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := d.loadCachedManifest(ref); err == nil {
			t.Fatal("tampered cached manifest must be rejected")
		}
	}
}

func TestManifestModelBytesRejectsOverflow(t *testing.T) {
	m := Manifest{ModelArtifacts: []Artifact{{Size: ^uint64(0)}, {Size: 1}}}
	if _, err := m.modelBytesChecked(); err == nil {
		t.Fatal("expected model artifact size overflow")
	}
}

func TestResumePartial(t *testing.T) {
	blob := []byte("GGUF-0123456789ABCDEF")
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": blob}}
	srv := newFakeHF(t, fixture)
	dir := t.TempDir()
	d := testDownloader(srv.URL, dir, ampleProfiler())
	partDir := filepath.Join(dir, "u_n", testCommit)
	if err := os.MkdirAll(partDir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(partDir, "model-Q4_K_M.gguf.part")
	if err := os.WriteFile(part, blob[:8], 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := d.EnsureLocal(context.Background(), Ref{Repo: "u/n"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(blob) {
		t.Fatalf("resumed content %q", got)
	}
}

func TestClaimPartialUsesUniquePrivateStage(t *testing.T) {
	dir := t.TempDir()
	stable := filepath.Join(dir, "model.gguf.part")
	if err := os.WriteFile(stable, []byte("GGUF-partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage, err := claimPartial(stable)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(stage)
	if stage == stable || !strings.Contains(filepath.Base(stage), "-stage-") {
		t.Fatalf("stage is not transaction-unique: %q", stage)
	}
	if _, err := os.Lstat(stable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stable partial was not claimed: %v", err)
	}
	got, err := os.ReadFile(stage)
	if err != nil || string(got) != "GGUF-partial" {
		t.Fatalf("claimed stage = %q, %v", got, err)
	}
	fi, err := os.Stat(stage)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("stage mode = %v", fi.Mode().Perm())
	}
}

func TestEnsureArtifactsRejectsSymlinkPartial(t *testing.T) {
	blob := []byte("GGUF-verified-weights")
	fixture := &hfFixture{files: map[string][]byte{"model-Q4_K_M.gguf": blob}}
	srv := newFakeHF(t, fixture)
	dir := t.TempDir()
	d := testDownloader(srv.URL, dir, ampleProfiler())
	cacheDir := filepath.Join(dir, "u_n", testCommit)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(cacheDir, "model-Q4_K_M.gguf.part")
	if err := os.Symlink(outside, part); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnsureArtifacts(context.Background(), Ref{Repo: "u/n"}); err == nil {
		t.Fatal("expected symlink partial to be rejected")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "do-not-touch" {
		t.Fatalf("symlink target was modified: %q, %v", got, err)
	}
}

func TestVerifyLocalArtifactsAgainstManifestRejectsPostCommitCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model.gguf")
	want := []byte("GGUF-authenticated")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(want)
	local := LocalArtifacts{
		Model:      path,
		ModelFiles: []string{path},
		Manifest: Manifest{ModelArtifacts: []Artifact{{
			Kind: ArtifactModel, Filename: filepath.Base(path), Size: uint64(len(want)), SHA256: hex.EncodeToString(hash[:]),
		}}},
	}
	if err := verifyLocalArtifactsAgainstManifest(local); err != nil {
		t.Fatalf("valid committed artifact rejected: %v", err)
	}
	corrupt := append([]byte(nil), want...)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyLocalArtifactsAgainstManifest(local); err == nil {
		t.Fatal("same-size post-commit corruption was accepted")
	}
}

func TestSanitizeFilenames(t *testing.T) {
	for _, bad := range []string{"../evil.gguf", "a/b.gguf", "model.bin", "", "..gguf/x"} {
		if _, err := sanitizeGGUFName(bad); err == nil {
			t.Errorf("sanitizeGGUFName(%q) expected error", bad)
		}
	}
}
