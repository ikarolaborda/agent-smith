package acquisition

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashAndCaptureSourceTree(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "src", "main.c"), []byte("int main(void) { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := HashTree(source, Limits{MaxFiles: 10, MaxBytes: 1024})
	if err != nil || snapshot.Files != 1 || snapshot.SourceSHA256 == "" {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	destination := filepath.Join(t.TempDir(), "captured")
	if _, err := Capture(source, destination, snapshot.SourceSHA256, Limits{MaxFiles: 10, MaxBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	captured, err := HashTree(destination, Limits{})
	if err != nil || captured.SourceSHA256 != snapshot.SourceSHA256 {
		t.Fatalf("captured=%#v err=%v", captured, err)
	}
	if err := os.WriteFile(filepath.Join(source, "src", "main.c"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := HashTree(source, Limits{})
	if err != nil || changed.SourceSHA256 == snapshot.SourceSHA256 {
		t.Fatal("content change did not alter source identity")
	}
}

func TestHashTreeRejectsSymlinksAndLimits(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "one"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, "one"), filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTree(source, Limits{}); err == nil {
		t.Fatal("source symlink accepted")
	}
	if err := os.Remove(filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTree(source, Limits{MaxFiles: 1, MaxBytes: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTree(source, Limits{MaxFiles: 1, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "two"), []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTree(source, Limits{MaxFiles: 1, MaxBytes: 2}); err == nil {
		t.Fatal("source file-count limit was not enforced")
	}
}

func TestHashTreeOnlyExcludesRootControlDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "vendor", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := HashTree(root, Limits{}); err == nil || !strings.Contains(err.Error(), "nested control entry") {
		t.Fatalf("nested Git control directory was not rejected: %v", err)
	}
}

func TestCaptureDirectoryRejectsSymlinkEscapes(t *testing.T) {
	t.Run("campaign parent", func(t *testing.T) {
		internal := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(internal, "campaign")); err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureDirectory(internal, "campaign", "target"); err == nil || !strings.Contains(err.Error(), "escapes internal storage") {
			t.Fatalf("campaign symlink escape accepted: %v", err)
		}
	})
	t.Run("target destination", func(t *testing.T) {
		internal := t.TempDir()
		destination := filepath.Join(internal, "campaign", "sources", "target")
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), destination); err != nil {
			t.Fatal(err)
		}
		if _, err := CaptureDirectory(internal, "campaign", "target"); err == nil || !strings.Contains(err.Error(), "destination is a symlink") {
			t.Fatalf("target symlink accepted: %v", err)
		}
	})
}

func TestValidateSourcePathRejectsPortableAmbiguities(t *testing.T) {
	for _, value := range []string{"../escape", "/absolute", `dir\\escape`, ".GIT/config", ".agent-smith/state", "CON", "com1.txt", "name:stream", "trailing.", "control\nname"} {
		if _, err := ValidateSourcePath(value); err == nil {
			t.Fatalf("unsafe path %q accepted", value)
		}
	}
	if value, err := ValidateSourcePath("src/parser.cc"); err != nil || value != "src/parser.cc" {
		t.Fatalf("ordinary source path rejected: value=%q err=%v", value, err)
	}
}

func TestVerifyAndCaptureGitCheckout(t *testing.T) {
	repository, commit := testGitRepository(t, map[string]testGitFile{
		"README.md":      {content: "committed\n", mode: 0o600},
		"bin/harness.sh": {content: "#!/bin/sh\nexit 0\n", mode: 0o700},
	})
	checkout, err := VerifyGitCheckout(context.Background(), repository, commit)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Commit != commit || checkout.RequestedRef != commit {
		t.Fatalf("checkout=%#v", checkout)
	}
	destination := filepath.Join(t.TempDir(), "capture")
	_, snapshot, err := CaptureGitCheckout(context.Background(), repository, commit, destination, Limits{MaxFiles: 10, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Files != 2 || snapshot.SourceSHA256 == "" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	content, err := os.ReadFile(filepath.Join(destination, "README.md"))
	if err != nil || string(content) != "committed\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "harness.sh"))
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestVerifyGitCheckoutRejectsMutableOrAmbiguousSources(t *testing.T) {
	t.Run("symbolic revision", func(t *testing.T) {
		repository, _ := testGitRepository(t, map[string]testGitFile{"source.c": {content: "one"}})
		if _, err := VerifyGitCheckout(context.Background(), repository, "HEAD"); err == nil || !strings.Contains(err.Error(), "exact lowercase") {
			t.Fatalf("symbolic revision accepted: %v", err)
		}
	})
	t.Run("mismatched head", func(t *testing.T) {
		repository, first := testGitRepository(t, map[string]testGitFile{"source.c": {content: "one"}})
		if err := os.WriteFile(filepath.Join(repository, "source.c"), []byte("two"), 0o600); err != nil {
			t.Fatal(err)
		}
		testGit(t, repository, "add", "source.c")
		testGit(t, repository, "commit", "-m", "second")
		if _, err := VerifyGitCheckout(context.Background(), repository, first); err == nil || !strings.Contains(err.Error(), "HEAD does not match") {
			t.Fatalf("mismatched HEAD accepted: %v", err)
		}
	})
	for _, test := range []struct {
		name string
		make func(*testing.T, string)
	}{
		{name: "tracked", make: func(t *testing.T, root string) { testWrite(t, filepath.Join(root, "source.c"), "changed") }},
		{name: "untracked", make: func(t *testing.T, root string) { testWrite(t, filepath.Join(root, "extra.c"), "extra") }},
		{name: "ignored", make: func(t *testing.T, root string) {
			testWrite(t, filepath.Join(root, ".gitignore"), "generated/\n")
			testGit(t, root, "add", ".gitignore")
			testGit(t, root, "commit", "-m", "ignore generated")
			if err := os.Mkdir(filepath.Join(root, "generated"), 0o700); err != nil {
				t.Fatal(err)
			}
			testWrite(t, filepath.Join(root, "generated", "artifact"), "ignored")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, commit := testGitRepository(t, map[string]testGitFile{"source.c": {content: "clean"}})
			test.make(t, repository)
			if test.name == "ignored" {
				commit = strings.TrimSpace(testGit(t, repository, "rev-parse", "HEAD"))
			}
			if _, _, err := CaptureGitCheckout(context.Background(), repository, commit, filepath.Join(t.TempDir(), "capture"), Limits{}); err == nil || !strings.Contains(err.Error(), "checkout content does not match") {
				t.Fatalf("%s worktree accepted: %v", test.name, err)
			}
		})
	}
	t.Run("staged index", func(t *testing.T) {
		repository, commit := testGitRepository(t, map[string]testGitFile{"source.c": {content: "clean"}})
		testWrite(t, filepath.Join(repository, "source.c"), "staged")
		testGit(t, repository, "add", "source.c")
		testWrite(t, filepath.Join(repository, "source.c"), "clean")
		if _, err := VerifyGitCheckout(context.Background(), repository, commit); err == nil || !strings.Contains(err.Error(), "index does not match") {
			t.Fatalf("staged index accepted: %v", err)
		}
	})
}

func TestCaptureGitCheckoutDoesNotExecuteRepositoryFilters(t *testing.T) {
	repository, commit := testGitRepository(t, map[string]testGitFile{
		".gitattributes": {content: "*.c filter=hostile\n"},
		"source.c":       {content: "clean"},
	})
	sentinel := filepath.Join(t.TempDir(), "filter-executed")
	testGit(t, repository, "config", "filter.hostile.clean", "touch "+sentinel+"; cat")
	testWrite(t, filepath.Join(repository, "source.c"), "dirty")
	if _, _, err := CaptureGitCheckout(context.Background(), repository, commit, filepath.Join(t.TempDir(), "capture"), Limits{}); err == nil {
		t.Fatal("dirty filtered checkout accepted")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("repository-defined filter executed: %v", err)
	}
}

func TestCaptureGitCheckoutRejectsUnsupportedTreeEntries(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		repository := t.TempDir()
		testGit(t, repository, "init", "-q")
		testGit(t, repository, "config", "user.email", "test@example.test")
		testGit(t, repository, "config", "user.name", "Test")
		testWrite(t, filepath.Join(repository, "target"), "target")
		if err := os.Symlink("target", filepath.Join(repository, "link")); err != nil {
			t.Fatal(err)
		}
		testGit(t, repository, "add", "target", "link")
		testGit(t, repository, "commit", "-m", "symlink")
		commit := strings.TrimSpace(testGit(t, repository, "rev-parse", "HEAD"))
		if _, _, err := CaptureGitCheckout(context.Background(), repository, commit, filepath.Join(t.TempDir(), "capture"), Limits{}); err == nil || !strings.Contains(err.Error(), "non-regular") {
			t.Fatalf("symlink tree accepted: %v", err)
		}
	})
	t.Run("submodule", func(t *testing.T) {
		child, _ := testGitRepository(t, map[string]testGitFile{"child.c": {content: "child"}})
		parent, _ := testGitRepository(t, map[string]testGitFile{"parent.c": {content: "parent"}})
		testGit(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", child, "dependency")
		testGit(t, parent, "commit", "-m", "submodule")
		commit := strings.TrimSpace(testGit(t, parent, "rev-parse", "HEAD"))
		if _, err := VerifyGitCheckout(context.Background(), parent, commit); err == nil || !strings.Contains(err.Error(), "submodules") {
			t.Fatalf("submodule accepted: %v", err)
		}
	})
}

func TestCaptureGitCheckoutRejectsExistingCaptureMismatch(t *testing.T) {
	repository, commit := testGitRepository(t, map[string]testGitFile{"source.c": {content: "trusted"}})
	destination := filepath.Join(t.TempDir(), "capture")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	testWrite(t, filepath.Join(destination, "source.c"), "tampered")
	if _, _, err := CaptureGitCheckout(context.Background(), repository, commit, destination, Limits{}); err == nil || !strings.Contains(err.Error(), "existing Git capture hash mismatch") {
		t.Fatalf("tampered capture accepted: %v", err)
	}
}

type testGitFile struct {
	content string
	mode    os.FileMode
}

func testGitRepository(t *testing.T, files map[string]testGitFile) (string, string) {
	t.Helper()
	repository := t.TempDir()
	testGit(t, repository, "init", "-q")
	testGit(t, repository, "config", "user.email", "test@example.test")
	testGit(t, repository, "config", "user.name", "Test")
	for name, file := range files {
		mode := file.mode
		if mode == 0 {
			mode = 0o600
		}
		path := filepath.Join(repository, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.content), mode); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, repository, "add", ".")
	testGit(t, repository, "commit", "-q", "-m", "fixture")
	return repository, strings.TrimSpace(testGit(t, repository, "rev-parse", "HEAD"))
}

func testWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}
