package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
streamChoice is the delta-shaped variant of wireChoice. The OpenAI streaming
protocol delivers tool calls piecewise: a first chunk carries id+name, and
subsequent chunks append argument-string fragments. Each chunk identifies
the call it belongs to by integer Index.
*/
type streamChoice struct {
	Delta struct {
		Content   string         `json:"content,omitempty"`
		ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

type streamEnvelope struct {
	Choices []streamChoice `json:"choices"`
}

/*
ChatStream performs a streaming POST /chat/completions request and converts
each SSE event into a StreamChunk on the returned channel. The channel is
closed when the stream terminates with "data: [DONE]" or on the first error.
Errors are also pushed as a final chunk with Err set, so consumers can do
range-based reads without a separate error path.
*/
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	resp, err := c.doRequest(ctx, c.buildRequest(req, true), "text/event-stream")
	if err != nil {
		return nil, err
	}

	out := make(chan llm.StreamChunk, 8)

	go func() {
		defer resp.Body.Close()
		defer close(out)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		toolBuf := map[int]*llm.ToolCall{}

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				out <- llm.StreamChunk{Done: true}
				return
			}

			var env streamEnvelope
			if err := json.Unmarshal([]byte(payload), &env); err != nil {
				out <- llm.StreamChunk{Err: fmt.Errorf("openai: stream decode: %w", err)}
				return
			}
			if len(env.Choices) == 0 {
				continue
			}
			ch := env.Choices[0]

			if ch.Delta.Content != "" {
				out <- llm.StreamChunk{Delta: ch.Delta.Content}
			}

			for _, tc := range ch.Delta.ToolCalls {
				if tc.Index == nil {
					continue
				}
				idx := *tc.Index
				existing, ok := toolBuf[idx]
				if !ok {
					existing = &llm.ToolCall{}
					toolBuf[idx] = existing
				}
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Function.Name != "" {
					existing.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					existing.Arguments = append(existing.Arguments, []byte(tc.Function.Arguments)...)
				}
				/*
					Emit a snapshot of the in-progress call. Consumers that
					only care about the final call can ignore until the
					terminator; consumers that want incremental tool-call
					rendering get the running view.
				*/
				snapshot := *existing
				out <- llm.StreamChunk{ToolCallDelta: &snapshot}
			}

			if ch.FinishReason != "" {
				out <- llm.StreamChunk{Done: true}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			out <- llm.StreamChunk{Err: fmt.Errorf("openai: stream read: %w", err)}
			return
		}
		out <- llm.StreamChunk{Err: errors.New("openai: stream ended without [DONE]")}
	}()

	return out, nil
}
