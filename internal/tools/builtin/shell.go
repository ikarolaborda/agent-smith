/*
Package builtin contains example Tool implementations bundled with the agent.
They are not registered by default; the application wires the ones it wants
into a tools.Registry.
*/
package builtin

import (
	"context"
	"encoding/json"
	"errors"
)

/*
Shell is a placeholder tool that, when implemented, will execute a shell
command and return its combined output. Currently it returns "not
implemented" so callers can wire it in without affecting compilation.
*/
type Shell struct{}

/* NewShell constructs an unimplemented Shell tool. */
func NewShell() *Shell { return &Shell{} }

func (*Shell) Name() string { return "shell" }

func (*Shell) Description() string {
	return "Execute a shell command and return its combined stdout/stderr output."
}

func (*Shell) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "The shell command to run." }
  },
  "required": ["command"]
}`)
}

/* Execute will eventually run the command described in args. TODO: implement. */
func (*Shell) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("shell tool: not implemented")
}
