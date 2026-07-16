package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
The Anthropic SSE format uses named events. Each event is a pair of lines:

	event: <name>
	data:  <json>

with a blank line between events. Event names and the deltas we care about:

  - content_block_start  : delta.type = text | tool_use (carries id+name)
  - content_block_delta  : delta.type = text_delta (.text) OR input_json_delta (.partial_json)
  - content_block_stop   : per-block terminator
  - message_delta        : carries the final stop_reason
  - message_stop         : whole-stream terminator

Tool-use blocks are partial across many input_json_delta events; we accumulate
.partial_json into ToolCall.Arguments and emit running snapshots.
*/

type streamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content_block"`

	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

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

		toolBlocks := map[int]*llm.ToolCall{}

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}

			var ev streamEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				llm.SendChunk(ctx, out, llm.StreamChunk{Err: fmt.Errorf("anthropic: stream decode: %w", err)})
				return
			}

			switch ev.Type {
			case "content_block_start":
				if ev.ContentBlock.Type == "tool_use" {
					tc := &llm.ToolCall{
						ID:        ev.ContentBlock.ID,
						Name:      ev.ContentBlock.Name,
						Arguments: json.RawMessage{},
					}
					toolBlocks[ev.Index] = tc
					/*
						Surface the call at block start. A tool_use block with an
						empty input schema emits no input_json_delta, so without
						this a no-argument tool call would never reach the consumer
						and the turn would end with an empty final answer.
					*/
					snapshot := *tc
					if !llm.SendChunk(ctx, out, llm.StreamChunk{ToolCallDelta: &snapshot}) {
						return
					}
				}

			case "content_block_delta":
				switch ev.Delta.Type {
				case "text_delta":
					if ev.Delta.Text != "" {
						if !llm.SendChunk(ctx, out, llm.StreamChunk{Delta: ev.Delta.Text}) {
							return
						}
					}
				case "input_json_delta":
					tc, ok := toolBlocks[ev.Index]
					if !ok {
						tc = &llm.ToolCall{Arguments: json.RawMessage{}}
						toolBlocks[ev.Index] = tc
					}
					tc.Arguments = append(tc.Arguments, []byte(ev.Delta.PartialJSON)...)
					snapshot := *tc
					if !llm.SendChunk(ctx, out, llm.StreamChunk{ToolCallDelta: &snapshot}) {
						return
					}
				}

			case "message_stop":
				llm.SendChunk(ctx, out, llm.StreamChunk{Done: true})
				return
			}
		}

		if err := scanner.Err(); err != nil {
			llm.SendChunk(ctx, out, llm.StreamChunk{Err: fmt.Errorf("anthropic: stream read: %w", err)})
			return
		}
		llm.SendChunk(ctx, out, llm.StreamChunk{Done: true})
	}()

	return out, nil
}
