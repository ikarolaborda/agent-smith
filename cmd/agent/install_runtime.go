/*
install_runtime.go bootstraps the llamacpp provider's one runtime dependency —
the llama.cpp `llama-server` binary — from Go, in-core, so an operator does not
have to build or fetch it by hand. It detects the host OS/arch and GPU backend,
picks the matching prebuilt release asset (Vulkan by default, since one Vulkan
build serves AMD, NVIDIA, and Intel), unpacks it under the app's data dir, and
links llama-server onto PATH where the provider resolves `binary: llama-server`.

It deliberately does NOT embed an inference engine: agent-smith supervises an
external llama-server (no cgo), and this command keeps that contract while making
the dependency self-installing.
*/
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
	"github.com/ikarolaborda/agent-smith/internal/logging"
)

const llamaCppLatestReleaseAPI = "https://api.github.com/repos/ggml-org/llama.cpp/releases/latest"

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	Tag    string    `json:"tag_name"`
	Assets []ghAsset `json:"assets"`
}

/*
runInstallRuntime is the --install-runtime entrypoint. It is intentionally
side-effect-heavy (network + filesystem) but idempotent: re-running re-links the
same binary in place.
*/
func runInstallRuntime(ctx context.Context, _ flags) error {
	logger := logging.New(logging.Options{Format: "text", Level: "info"})

	backend := detectRuntimeBackend(ctx, logger)
	candidates, ext, err := runtimeAssetCandidates(runtime.GOOS, runtime.GOARCH, backend)
	if err != nil {
		return err
	}
	logger.Info("selecting llama.cpp runtime", "os", runtime.GOOS, "arch", runtime.GOARCH, "gpu_backend", backend, "preference", strings.Join(candidates, " > "))

	rel, err := fetchLatestLlamaRelease(ctx)
	if err != nil {
		return err
	}
	asset, matched, ok := pickAsset(rel.Assets, candidates, ext)
	if !ok {
		return fmt.Errorf("install-runtime: no llama.cpp asset in release %s matched %v (ext %q); install llama-server manually", rel.Tag, candidates, ext)
	}
	logger.Info("resolved runtime asset", "release", rel.Tag, "asset", asset.Name, "variant", matched)

	installRoot := filepath.Join(runtimeDataDir(), rel.Tag)
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return fmt.Errorf("install-runtime: create %s: %w", installRoot, err)
	}
	serverPath, err := downloadAndExtractServer(ctx, asset.URL, installRoot, logger)
	if err != nil {
		return err
	}

	linkPath, linked := linkOntoPath(serverPath, logger)
	if err := verifyLlamaServer(ctx, serverPath); err != nil {
		return fmt.Errorf("install-runtime: installed binary failed to run: %w", err)
	}

	logger.Info("llama-server installed", "binary", serverPath, "release", rel.Tag, "gpu_backend", backend)
	if linked {
		logger.Info("linked onto PATH", "link", linkPath)
		if !dirOnPath(filepath.Dir(linkPath)) {
			logger.Warn("link directory is not on PATH; add it or set llama_cpp.binary to the absolute path", "dir", filepath.Dir(linkPath), "binary", serverPath)
		}
	} else {
		logger.Warn("could not link onto PATH; set llama_cpp.binary to the absolute path", "binary", serverPath)
	}
	return nil
}

/*
detectRuntimeBackend reuses the provider's own host profiler so the runtime
choice matches what the fit gate and tuner will see. Detection is advisory: any
failure falls back to a CPU build rather than aborting the install.
*/
func detectRuntimeBackend(ctx context.Context, logger *slog.Logger) llamacpp.GPUBackend {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	host, err := llamacpp.SystemProfiler{}.Profile(ctx, home)
	if err != nil {
		logger.Warn("host profile failed; assuming CPU-only runtime", "err", err)
		return llamacpp.GPUBackendNone
	}
	return host.GPU.Backend
}

/*
runtimeAssetCandidates maps (os, arch, backend) to ordered llama.cpp release
asset name fragments, most-preferred first, plus the archive extension. Vulkan is
the default GPU build because a single Vulkan artifact runs on AMD, NVIDIA, and
Intel; the CPU build is always the final fallback on platforms that ship one.
*/
func runtimeAssetCandidates(goos, goarch string, backend llamacpp.GPUBackend) ([]string, string, error) {
	hasGPU := backend != "" && backend != llamacpp.GPUBackendNone
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			if hasGPU {
				return []string{"ubuntu-vulkan-x64", "ubuntu-x64"}, ".tar.gz", nil
			}
			return []string{"ubuntu-x64"}, ".tar.gz", nil
		case "arm64":
			if hasGPU {
				return []string{"ubuntu-vulkan-arm64", "ubuntu-arm64"}, ".tar.gz", nil
			}
			return []string{"ubuntu-arm64"}, ".tar.gz", nil
		}
	case "darwin":
		/* Every Mac offloads through Metal, bundled in the standard macOS build. */
		switch goarch {
		case "arm64":
			return []string{"macos-arm64"}, ".tar.gz", nil
		case "amd64":
			return []string{"macos-x64"}, ".tar.gz", nil
		}
	}
	return nil, "", fmt.Errorf("install-runtime: unsupported platform %s/%s; install llama-server manually", goos, goarch)
}

/*
pickAsset returns the first release asset whose name carries the archive
extension and contains the highest-priority candidate fragment. Priority is by
candidate order, not asset order, so a Vulkan build always wins over the CPU
fallback when both are present.
*/
func pickAsset(assets []ghAsset, candidates []string, ext string) (ghAsset, string, bool) {
	for _, frag := range candidates {
		for _, a := range assets {
			name := strings.ToLower(a.Name)
			if strings.HasSuffix(name, ext) && strings.Contains(name, frag) {
				return a, frag, true
			}
		}
	}
	return ghAsset{}, "", false
}

func fetchLatestLlamaRelease(ctx context.Context) (ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, llamaCppLatestReleaseAPI, nil)
	if err != nil {
		return ghRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "agent-smith-install-runtime")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return ghRelease{}, fmt.Errorf("install-runtime: query llama.cpp releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ghRelease{}, fmt.Errorf("install-runtime: llama.cpp releases API returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rel); err != nil {
		return ghRelease{}, fmt.Errorf("install-runtime: decode release JSON: %w", err)
	}
	if rel.Tag == "" || len(rel.Assets) == 0 {
		return ghRelease{}, fmt.Errorf("install-runtime: llama.cpp latest release had no assets")
	}
	return rel, nil
}

/*
downloadAndExtractServer streams the .tar.gz to disk under installRoot and
returns the path to the extracted llama-server. Entry paths are validated to stay
within installRoot (no absolute paths or .. traversal) before any file is written.
*/
func downloadAndExtractServer(ctx context.Context, url, installRoot string, logger *slog.Logger) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "agent-smith-install-runtime")
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("install-runtime: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("install-runtime: download %s returned %s", url, resp.Status)
	}
	logger.Info("downloading runtime", "url", url)

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("install-runtime: gunzip: %w", err)
	}
	defer gz.Close()

	serverPath := ""
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("install-runtime: read archive: %w", err)
		}
		dest, err := safeJoin(installRoot, hdr.Name)
		if err != nil {
			return "", err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			if err := writeFileFromTar(dest, tr, os.FileMode(hdr.Mode)); err != nil {
				return "", err
			}
			if filepath.Base(dest) == "llama-server" {
				serverPath = dest
			}
		case tar.TypeSymlink:
			/*
				Shared libraries ship as a versioned real file plus a soname symlink
				(libfoo.so.0 -> libfoo.so.0.17.0). Skipping these breaks the RUNPATH
				($ORIGIN) resolution and the binary fails to load. Links are relative
				within the extracted tree, so recreate them verbatim.
			*/
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			_ = os.Remove(dest)
			if err := os.Symlink(hdr.Linkname, dest); err != nil {
				return "", fmt.Errorf("install-runtime: symlink %s -> %s: %w", dest, hdr.Linkname, err)
			}
		case tar.TypeLink:
			target, err := safeJoin(installRoot, hdr.Linkname)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			_ = os.Remove(dest)
			if err := os.Link(target, dest); err != nil {
				return "", fmt.Errorf("install-runtime: hardlink %s -> %s: %w", dest, target, err)
			}
		}
	}
	if serverPath == "" {
		return "", fmt.Errorf("install-runtime: archive did not contain a llama-server binary")
	}
	return serverPath, nil
}

/* safeJoin rejects archive entries that would escape root via .. or absolute paths. */
func safeJoin(root, name string) (string, error) {
	dest := filepath.Join(root, name)
	if dest != root && !strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("install-runtime: archive entry %q escapes install dir", name)
	}
	return dest, nil
}

func writeFileFromTar(dest string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	/* Ensure the executable bit survives even if the umask stripped it at create. */
	return os.Chmod(dest, mode.Perm())
}

/*
linkOntoPath symlinks the server into ~/.local/bin (the conventional user bin dir)
so `binary: llama-server` resolves from PATH. A failure is non-fatal: the caller
still reports the absolute path the operator can pin directly.
*/
func linkOntoPath(serverPath string, logger *slog.Logger) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		logger.Warn("could not create local bin dir", "dir", binDir, "err", err)
		return "", false
	}
	link := filepath.Join(binDir, "llama-server")
	_ = os.Remove(link)
	if err := os.Symlink(serverPath, link); err != nil {
		logger.Warn("could not symlink llama-server", "link", link, "err", err)
		return "", false
	}
	return link, true
}

func verifyLlamaServer(ctx context.Context, serverPath string) error {
	/* llama-server prints its version to stderr, so merge both streams. */
	out, err := exec.CommandContext(ctx, serverPath, "--version").CombinedOutput()
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(string(out)), "version") {
		return fmt.Errorf("unexpected --version output: %q", strings.TrimSpace(string(out)))
	}
	return nil
}

func runtimeDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "agent-smith", "llama.cpp")
	}
	return filepath.Join(os.TempDir(), "agent-smith", "llama.cpp")
}

func dirOnPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}
