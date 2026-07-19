package builtin

import (
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/*
NewDefaultRegistry builds the standard agent tool set shared by the CLI and the
HTTP server so terminal and web expose identical capabilities. Read-only
project tools (file_read and read_dir) are always present and rooted at
workspace (empty = CWD). The mutating file_write/file_edit tools are
registered ONLY when a non-empty workspace is given, keeping the default posture
read-only; both confine to workspace via insideRoot so the agent can neither
escape the folder nor follow symlinks out of it.

The historical shell and http placeholders are deliberately not registered:
both return "not implemented", so advertising them to a model is a false
capability and wastes agent iterations. Research execution is exposed only by
the opt-in structured contained runner below; network acquisition uses dedicated
host-side clients with fixed policy rather than a model-controlled HTTP tool.
*/
func NewDefaultRegistry(workspace string) *tools.Registry {
	return NewDefaultRegistryWithExec(workspace, false)
}

/*
NewDefaultRegistryWithExec builds the standard tool set and, only when
allowExec is true AND a workspace is configured, additionally registers the
opt-in container-contained execution tool (ADR 0003). Exec needs a writable
workspace to mount at /work, so without one it is silently omitted. Existing
callers keep using NewDefaultRegistry (allowExec=false) and are unaffected,
preserving the read-only-by-default posture.
*/
func NewDefaultRegistryWithExec(workspace string, allowExec bool, execOpts ...ContainedExecOption) *tools.Registry {
	reg := tools.NewRegistry()
	_ = reg.Register(NewFileRead(workspace))
	_ = reg.Register(NewReadDir(workspace))
	if workspace != "" {
		_ = reg.Register(NewFileWrite(workspace))
		_ = reg.Register(NewFileEdit(workspace))
		if allowExec {
			_ = reg.Register(NewContainedExec(workspace, allowExec, execOpts...))
		}
	}
	return reg
}
