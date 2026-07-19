package server

import "testing"

func TestCapabilityStatusIsTruthfulByDefault(t *testing.T) {
	s := &Server{}
	got := s.capabilityStatus()
	for _, name := range []string{"host_shell", "arbitrary_http", "contained_execution", "coverage_guided_fuzzing", "artifact_persistence", "research_persistence", "authentication", "research_cpu_rate_limit", "research_writable_monitor", "research_kernel_storage_quota", "research_full_accounting"} {
		if enabled, _ := got[name].(bool); enabled {
			t.Errorf("%s must be false by default: %#v", name, got)
		}
	}
	if got["file_read"] != true {
		t.Fatalf("file_read must be reported: %#v", got)
	}
}

func TestCapabilityStatusReportsContainedCompatibilityRunner(t *testing.T) {
	s := &Server{workspace: t.TempDir(), allowExec: true, execImageDigest: "sha256:fixture"}
	got := s.capabilityStatus()
	if got["contained_execution"] != true || got["execution_image_pinned"] != true {
		t.Fatalf("contained runner capability missing: %#v", got)
	}
	if got["apparatus"] != "external_php_driver" || got["coverage_guided_fuzzing"] != false {
		t.Fatalf("compatibility adapter must not claim coverage-guided fuzzing: %#v", got)
	}
}
