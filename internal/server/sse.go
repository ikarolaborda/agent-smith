package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

/*
sseEncoder writes OpenAI-shape chat completion delta frames and small named
events to an http.ResponseWriter, flushing after every write so the client
sees frames as soon as they are produced.
*/
type sseEncoder struct {
	w       io.Writer
	flusher http.Flusher
}

func newSSEEncoder(w io.Writer, f http.Flusher) *sseEncoder {
	return &sseEncoder{w: w, flusher: f}
}

/*
writeOpenAIDelta writes one chat-completion chunk. The OpenAI shape is:

	{
	  "id": "...",
	  "object": "chat.completion.chunk",
	  "created": 0,
	  "model": "...",
	  "choices": [{ "index": 0, "delta": {...}, "finish_reason": "..." }]
	}

finishReason is omitted when empty.
*/
func (e *sseEncoder) writeOpenAIDelta(id, model string, created int64, delta map[string]any, finishReason string) error {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	} else {
		choice["finish_reason"] = nil
	}
	envelope := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{choice},
	}
	buf, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "data: %s\n\n", buf); err != nil {
		return err
	}
	e.flusher.Flush()
	return nil
}

/*
writeNamedEvent writes a named SSE event with a JSON payload. The UI uses
this for `tool_result` and `error` extensions; generic OpenAI clients ignore
unknown event names and continue parsing the default `data:` frames.
*/
func (e *sseEncoder) writeNamedEvent(name string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", name, buf); err != nil {
		return err
	}
	e.flusher.Flush()
	return nil
}

/* writeDone emits the OpenAI terminator. */
func (e *sseEncoder) writeDone() {
	_, _ = fmt.Fprint(e.w, "data: [DONE]\n\n")
	e.flusher.Flush()
}
