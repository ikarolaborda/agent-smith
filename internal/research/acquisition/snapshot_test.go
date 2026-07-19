package acquisition

import (
	"os"
	"path/filepath"
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
