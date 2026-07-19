package prompt

import "strings"

import "testing"

func TestWorkspaceDirective_EmptyPathYieldsNothing(t *testing.T) {
	if got := WorkspaceDirective(""); got != "" {
		t.Errorf("no workspace must inject no directive, got %q", got)
	}
}

func TestWorkspaceDirective_NamesPathAndToolsAndForbidsDisclaimer(t *testing.T) {
	d := WorkspaceDirective("/home/user/php74-vuln-research")
	for _, want := range []string{"/home/user/php74-vuln-research", "read_dir", "file_read", "never claim you cannot see local files"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive missing %q; got: %s", want, d)
		}
	}
}

func TestJoinSections_IncludesWorkspaceWhenOpen(t *testing.T) {
	joined := JoinSections("base", WorkspaceDirective("/ws"), "")
	if !strings.Contains(joined, "/ws") {
		t.Error("JoinSections should include the workspace directive when a path is set")
	}
	// And omit it entirely when no workspace is open.
	if strings.Contains(JoinSections("base", WorkspaceDirective(""), ""), "Workspace access") {
		t.Error("no workspace directive should appear when path is empty")
	}
}
