package builtin

import (
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/*
NewDefaultRegistry builds the standard agent tool set shared by the CLI and the
HTTP server so terminal and web expose identical capabilities. Read-only
grounding tools (shell, http, file_read, read_dir) are always present and rooted
at workspace (empty = CWD). The mutating file_write/file_edit tools are
registered ONLY when a non-empty workspace is given, keeping the default posture
read-only; both confine to workspace via insideRoot so the agent can neither
escape the folder nor follow symlinks out of it.
*/
func NewDefaultRegistry(workspace string) *tools.Registry {
	reg := tools.NewRegistry()
	_ = reg.Register(NewShell())
	_ = reg.Register(NewHTTP())
	_ = reg.Register(NewFileRead(workspace))
	_ = reg.Register(NewReadDir(workspace))
	if workspace != "" {
		_ = reg.Register(NewFileWrite(workspace))
		_ = reg.Register(NewFileEdit(workspace))
	}
	return reg
}
