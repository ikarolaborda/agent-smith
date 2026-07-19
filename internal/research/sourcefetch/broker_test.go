package sourcefetch

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/acquisition"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) { return function(request) }

func TestBrokerFetchesPinnedTarWithoutCallerControlledEgress(t *testing.T) {
	bundle := testTar(t,
		tar.Header{Name: "src/", Typeflag: tar.TypeDir, Mode: 0o755}, nil,
		tar.Header{Name: "src/harness.sh", Typeflag: tar.TypeReg, Mode: 0o755, Size: 17}, []byte("#!/bin/sh\nexit 0\n"),
		tar.Header{Name: "README.md", Typeflag: tar.TypeReg, Mode: 0o644, Size: 8}, []byte("fixture\n"),
	)
	var captured *http.Request
	broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) {
		captured = request
		return testResponse(request, bundle), nil
	})
	destination := filepath.Join(t.TempDir(), "capture")
	result, err := broker.Fetch(context.Background(), "upstream", testCommit, destination, acquisition.Limits{MaxFiles: 10, MaxBytes: int64(len(bundle) + 1024)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Descriptor.Commit != testCommit || result.BundleBytes != int64(len(bundle)) || result.Snapshot.Files != 2 || result.Snapshot.SourceSHA256 == "" || result.FetchedAt.IsZero() {
		t.Fatalf("result=%#v", result)
	}
	if captured == nil || captured.Method != http.MethodGet || captured.URL.String() != "https://sources.example.test/project/source.tar" || captured.Header.Get("Accept-Encoding") != "identity" || captured.Header.Get("Authorization") != "" || captured.Header.Get("Cookie") != "" {
		t.Fatalf("request=%#v", captured)
	}
	content, err := os.ReadFile(filepath.Join(destination, "src", "harness.sh"))
	if err != nil || string(content) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(destination, "src", "harness.sh"))
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestBrokerRejectsUnpinnedAndSubstitutedResponses(t *testing.T) {
	bundle := testTar(t, tar.Header{Name: "source.c", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, []byte("x"))
	t.Run("unknown commit", func(t *testing.T) {
		broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) { return testResponse(request, bundle), nil })
		if _, err := broker.Describe("upstream", strings.Repeat("a", 40)); err == nil {
			t.Fatal("unconfigured commit accepted")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) {
			substituted, _ := http.NewRequest(http.MethodGet, "https://sources.example.test/other.tar", nil)
			return testResponse(substituted, bundle), nil
		})
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(bundle) + 10)}); err == nil || !strings.Contains(err.Error(), "redirected or substituted") {
			t.Fatalf("redirect accepted: %v", err)
		}
	})
	t.Run("digest", func(t *testing.T) {
		broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) {
			return testResponse(request, append(bundle, 'x')), nil
		})
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(bundle) + 100)}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("digest substitution accepted: %v", err)
		}
	})
	t.Run("content length", func(t *testing.T) {
		broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) {
			response := testResponse(request, bundle)
			response.ContentLength = int64(len(bundle) + 100)
			return response, nil
		})
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(bundle) + 10)}); err == nil || !strings.Contains(err.Error(), "response exceeds") {
			t.Fatalf("oversized response accepted: %v", err)
		}
	})
}

func TestBrokerRejectsHostileTarEntriesAndBudgets(t *testing.T) {
	tests := []struct {
		name     string
		headers  []tar.Header
		bodies   [][]byte
		maxFiles int64
		extra    int64
		want     string
	}{
		{name: "traversal", headers: []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Size: 1}}, bodies: [][]byte{[]byte("x")}, maxFiles: 10, extra: 10, want: "unsafe source path"},
		{name: "symlink", headers: []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, bodies: [][]byte{nil}, maxFiles: 10, extra: 10, want: "unsupported entry"},
		{name: "hardlink", headers: []tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}}, bodies: [][]byte{nil}, maxFiles: 10, extra: 10, want: "unsupported entry"},
		{name: "reserved", headers: []tar.Header{{Name: ".GIT/config", Typeflag: tar.TypeReg, Size: 1}}, bodies: [][]byte{[]byte("x")}, maxFiles: 10, extra: 10, want: "reserved source path"},
		{name: "case collision", headers: []tar.Header{{Name: "Source.c", Typeflag: tar.TypeReg, Size: 1}, {Name: "source.c", Typeflag: tar.TypeReg, Size: 1}}, bodies: [][]byte{[]byte("a"), []byte("b")}, maxFiles: 10, extra: 10, want: "case-colliding"},
		{name: "inode limit", headers: []tar.Header{{Name: "a/b/file", Typeflag: tar.TypeReg, Size: 1}}, bodies: [][]byte{[]byte("x")}, maxFiles: 2, extra: 10, want: "inode limit"},
		{name: "byte limit", headers: []tar.Header{{Name: "file", Typeflag: tar.TypeReg, Size: 4}}, bodies: [][]byte{[]byte("four")}, maxFiles: 10, extra: 3, want: "byte limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments := make([]any, 0, len(test.headers)*2)
			for index := range test.headers {
				arguments = append(arguments, test.headers[index], test.bodies[index])
			}
			bundle := testTar(t, arguments...)
			broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) { return testResponse(request, bundle), nil })
			_, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: test.maxFiles, MaxBytes: int64(len(bundle)) + test.extra})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("hostile tar accepted or wrong error: %v", err)
			}
		})
	}
}

func TestBrokerRejectsPinnedTrailingPayloadAndExistingTampering(t *testing.T) {
	clean := testTar(t, tar.Header{Name: "source.c", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}, []byte("x"))
	t.Run("trailing payload", func(t *testing.T) {
		bundle := append(append([]byte(nil), clean...), []byte("hidden payload")...)
		broker := testBroker(t, bundle, func(request *http.Request) (*http.Response, error) { return testResponse(request, bundle), nil })
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(bundle) + 10)}); err == nil || !strings.Contains(err.Error(), "trailing non-padding") {
			t.Fatalf("trailing payload accepted: %v", err)
		}
	})
	t.Run("existing capture", func(t *testing.T) {
		broker := testBroker(t, clean, func(request *http.Request) (*http.Response, error) { return testResponse(request, clean), nil })
		destination := filepath.Join(t.TempDir(), "capture")
		limits := acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(clean) + 10)}
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, destination, limits); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(destination, "source.c"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "source.c"), []byte("t"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := broker.Fetch(context.Background(), "upstream", testCommit, destination, limits); err == nil || !strings.Contains(err.Error(), "existing capture hash mismatch") {
			t.Fatalf("tampered capture accepted: %v", err)
		}
	})
}

func TestNewBrokerRejectsUnsafeConfiguration(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("a", 64)
	tests := []Bundle{
		{Commit: "main", URL: "https://sources.example.test/source.tar", SHA256: validDigest},
		{Commit: testCommit, URL: "http://sources.example.test/source.tar", SHA256: validDigest},
		{Commit: testCommit, URL: "https://user@sources.example.test/source.tar", SHA256: validDigest},
		{Commit: testCommit, URL: "https://sources.example.test/source.tar?token=secret", SHA256: validDigest},
		{Commit: testCommit, URL: "https://sources.example.test:8443/source.tar", SHA256: validDigest},
		{Commit: testCommit, URL: "https://sources.example.test/source.tar", SHA256: "sha256:bad"},
	}
	for index, bundle := range tests {
		if _, err := NewBroker(doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), []Source{{Name: "source", Repository: "repo", Bundles: []Bundle{bundle}}}, 0, "", time.Time{}); err == nil {
			t.Fatalf("unsafe configuration %d accepted", index)
		}
	}
}

func TestPublicAddressRejectsInternalAndReservedNetworks(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.1.1", "192.0.2.1", "::1", "fc00::1", "2001:db8::1", "::ffff:127.0.0.1"} {
		if publicAddress(netip.MustParseAddr(value)) {
			t.Fatalf("internal address %s accepted", value)
		}
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(value)) {
			t.Fatalf("public address %s rejected", value)
		}
	}
}

func TestBrokerRechecksSignedManifestExpiryBeforeEgress(t *testing.T) {
	bundle := testTar(t, tar.Header{Name: "source.c", Typeflag: tar.TypeReg, Size: 1}, []byte("x"))
	digest := sha256.Sum256(bundle)
	expires := time.Now().UTC().Add(time.Hour)
	requests := 0
	broker, err := NewBroker(doerFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return testResponse(request, bundle), nil
	}), []Source{{Name: "upstream", Repository: "repo", Bundles: []Bundle{{Commit: testCommit, URL: "https://sources.example.test/source.tar", SHA256: "sha256:" + hex.EncodeToString(digest[:])}}}}, int64(len(bundle)+10), "sha256:"+strings.Repeat("b", 64), expires)
	if err != nil {
		t.Fatal(err)
	}
	broker.now = func() time.Time { return expires }
	if _, err := broker.Fetch(context.Background(), "upstream", testCommit, filepath.Join(t.TempDir(), "capture"), acquisition.Limits{MaxFiles: 4, MaxBytes: int64(len(bundle) + 10)}); err == nil || !strings.Contains(err.Error(), "manifest expired") || requests != 0 {
		t.Fatalf("expired manifest fetch err=%v requests=%d", err, requests)
	}
}

func testBroker(t *testing.T, body []byte, doer doerFunc) *Broker {
	t.Helper()
	digest := sha256.Sum256(body)
	broker, err := NewBroker(doer, []Source{{Name: "upstream", Repository: "https://example.test/project.git", Bundles: []Bundle{{Commit: testCommit, URL: "https://sources.example.test/project/source.tar", SHA256: "sha256:" + hex.EncodeToString(digest[:])}}}}, int64(len(body)+1024), "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func testResponse(request *http.Request, body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request}
}

func testTar(t *testing.T, values ...any) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for index := 0; index < len(values); index += 2 {
		header := values[index].(tar.Header)
		var body []byte
		if values[index+1] != nil {
			body = values[index+1].([]byte)
		}
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if len(body) > 0 {
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
