package builtin

import (
	"context"
	"encoding/json"
	"errors"
)

/*
HTTP is a placeholder tool that, when implemented, will perform an HTTP
request and return the response body.
*/
type HTTP struct{}

/* NewHTTP constructs an unimplemented HTTP tool. */
func NewHTTP() *HTTP { return &HTTP{} }

func (*HTTP) Name() string { return "http" }

func (*HTTP) Description() string {
	return "Perform an HTTP request and return the response body."
}

func (*HTTP) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "method":  { "type": "string", "enum": ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"] },
    "url":     { "type": "string", "description": "Absolute URL to call." },
    "headers": { "type": "object", "additionalProperties": { "type": "string" } },
    "body":    { "type": "string", "description": "Optional request body." }
  },
  "required": ["method", "url"]
}`)
}

/* Execute will eventually perform the HTTP request described in args. TODO: implement. */
func (*HTTP) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("http tool: not implemented")
}
