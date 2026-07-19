package builtin_test

import (
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
)

func TestNewDefaultRegistry_WriteToolsGatedOnWorkspace(t *testing.T) {
	readOnly := builtin.NewDefaultRegistry("")
	for _, name := range []string{"file_read", "read_dir"} {
		if _, err := readOnly.Get(name); err != nil {
			t.Errorf("read-only registry should always expose %q: %v", name, err)
		}
	}
	for _, name := range []string{"shell", "http"} {
		if _, err := readOnly.Get(name); err == nil {
			t.Errorf("registry must not advertise non-functional placeholder %q", name)
		}
	}
	for _, name := range []string{"file_write", "file_edit"} {
		if _, err := readOnly.Get(name); err == nil {
			t.Errorf("read-only registry must NOT expose %q without a workspace", name)
		}
	}

	scoped := builtin.NewDefaultRegistry(t.TempDir())
	for _, name := range []string{"file_write", "file_edit", "file_read", "read_dir"} {
		if _, err := scoped.Get(name); err != nil {
			t.Errorf("workspace registry should expose %q: %v", name, err)
		}
	}
}
