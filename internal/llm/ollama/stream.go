package ollama

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
ChatStream performs a streaming POST /api/chat call. Ollama emits one JSON
object per line (NDJSON) and signals end-of-stream with {"done": true} on
the final object. Tool calls arrive whole inside a single message object,
not as cumulative deltas the way OpenAI/Anthropic do.
*/
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	resp, err := c.doRequest(ctx, c.buildRequest(req, true))
	if err != nil {
		return nil, err
	}

	out := make(chan llm.StreamChunk, 8)

	go func() {
		defer resp.Body.Close()
		defer close(out)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var wr wireResponse
			if err := json.Unmarshal(line, &wr); err != nil {
				out <- llm.StreamChunk{Err: fmt.Errorf("ollama: stream decode: %w", err)}
				return
			}
			if wr.Message.Content != "" {
				out <- llm.StreamChunk{Delta: wr.Message.Content}
			}
			for _, tc := range wr.Message.ToolCalls {
				snapshot := llm.ToolCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				}
				out <- llm.StreamChunk{ToolCallDelta: &snapshot}
			}
			if wr.Done {
				out <- llm.StreamChunk{Done: true}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			out <- llm.StreamChunk{Err: fmt.Errorf("ollama: stream read: %w", err)}
			return
		}
		out <- llm.StreamChunk{Done: true}
	}()

	return out, nil
}
