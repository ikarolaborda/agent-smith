package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
StreamEventKind labels the variant carried in a StreamEvent. Consumers should
treat unknown kinds as a future extension and ignore them rather than fail.
*/
type StreamEventKind string

const (
	StreamEventTextDelta        StreamEventKind = "text_delta"
	StreamEventToolCallStart    StreamEventKind = "tool_call_start"
	StreamEventToolCallArgDelta StreamEventKind = "tool_call_args_delta"
	StreamEventToolCallEnd      StreamEventKind = "tool_call_end"
	StreamEventToolResult       StreamEventKind = "tool_result"
	StreamEventDone             StreamEventKind = "done"
	StreamEventError            StreamEventKind = "error"
)

/*
StreamEvent is the typed value emitted by Agent.RunStream. Exactly one of the
Delta / ToolCall* / Error fields is populated per kind; Iteration is the
1-based agent loop round in which the event was produced.
*/
type StreamEvent struct {
	Kind         StreamEventKind `json:"kind"`
	Iteration    int             `json:"iteration"`
	Delta        string          `json:"delta,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	ToolCallName string          `json:"tool_call_name,omitempty"`
	ToolCallArgs json.RawMessage `json:"tool_call_args,omitempty"`
	ToolResult   string          `json:"tool_result,omitempty"`
	ToolError    bool            `json:"tool_error,omitempty"`
	FinalContent string          `json:"final_content,omitempty"`
	Error        string          `json:"error,omitempty"`
}

/*
StreamSink receives StreamEvent values produced by RunStream. Implementations
must be safe to call from a single goroutine and should return promptly so
they do not stall the agent loop. Returning a non-nil error aborts the run
with that error.
*/
type StreamSink interface {
	Emit(StreamEvent) error
}

/*
SinkFunc adapts a plain function to the StreamSink interface, mirroring
http.HandlerFunc.
*/
type SinkFunc func(StreamEvent) error

/* Emit satisfies StreamSink. */
func (f SinkFunc) Emit(e StreamEvent) error { return f(e) }

/*
RunStream is the streaming sibling of Run. It drives the agent loop using
Provider.ChatStream and emits StreamEvent values to sink as text deltas,
tool-call lifecycle markers, and server-executed tool results arrive.

The session is mutated in place, just like Run. The function returns the
final assistant content (the last assistant message that contained no tool
calls). On context cancellation it returns ctx.Err(); on sink error it
returns that error and stops the loop.
*/
func (a *Agent) RunStream(ctx context.Context, session *Session, userInput string, sink StreamSink) (string, error) {
	if a == nil || a.Provider == nil {
		return "", errors.New("agent: not configured")
	}
	if session == nil {
		return "", errors.New("agent: nil session")
	}
	if sink == nil {
		return "", errors.New("agent: nil sink")
	}

	session.Append(llm.Message{Role: llm.RoleUser, Content: userInput})

	for i := 1; i <= a.MaxIters; i++ {
		req := llm.ChatRequest{
			Messages: a.composeMessages(ctx, session),
			Stream:   true,
			Model:    a.Model,
		}
		if a.Tools != nil {
			req.Tools = a.Tools.Definitions()
		}

		stream, err := a.Provider.ChatStream(ctx, req)
		if err != nil {
			_ = sink.Emit(StreamEvent{Kind: StreamEventError, Iteration: i, Error: err.Error()})
			return "", fmt.Errorf("agent: provider chat stream: %w", err)
		}

		assistant, runErr := a.consumeRound(ctx, i, stream, sink)
		if runErr != nil {
			return "", runErr
		}
		session.Append(assistant)

		if len(assistant.ToolCalls) == 0 {
			if err := sink.Emit(StreamEvent{Kind: StreamEventDone, Iteration: i, FinalContent: assistant.Content}); err != nil {
				return "", err
			}
			return assistant.Content, nil
		}

		for _, call := range assistant.ToolCalls {
			result, toolErr := a.dispatch(ctx, call)
			session.Append(llm.Message{
				Role:       llm.RoleTool,
				Name:       call.Name,
				ToolCallID: call.ID,
				Content:    formatToolResult(result, toolErr),
			})

			ev := StreamEvent{
				Kind:         StreamEventToolResult,
				Iteration:    i,
				ToolCallID:   call.ID,
				ToolCallName: call.Name,
			}
			if toolErr != nil {
				ev.ToolError = true
				ev.ToolResult = toolErr.Error()
			} else {
				ev.ToolResult = result
			}
			if err := sink.Emit(ev); err != nil {
				return "", err
			}
		}
	}

	_ = sink.Emit(StreamEvent{Kind: StreamEventError, Error: ErrMaxIterations.Error()})
	return "", ErrMaxIterations
}

/*
consumeRound reads a single provider stream until [DONE] or terminal error,
emitting events and assembling the canonical assistant Message. Tool-call
snapshots arrive incrementally (the provider stream emits running views);
each call is keyed by its eventual ID — falling back to the first observed
order when an ID is empty — and finalized when the stream closes.
*/
func (a *Agent) consumeRound(ctx context.Context, iter int, stream <-chan llm.StreamChunk, sink StreamSink) (llm.Message, error) {
	var content string
	type callState struct {
		call    llm.ToolCall
		emitted bool
	}
	calls := map[string]*callState{}
	order := []string{}

	for {
		select {
		case <-ctx.Done():
			return llm.Message{}, ctx.Err()
		case chunk, ok := <-stream:
			if !ok {
				goto finalize
			}
			if chunk.Err != nil {
				_ = sink.Emit(StreamEvent{Kind: StreamEventError, Iteration: iter, Error: chunk.Err.Error()})
				return llm.Message{}, chunk.Err
			}
			if chunk.Delta != "" {
				content += chunk.Delta
				if err := sink.Emit(StreamEvent{Kind: StreamEventTextDelta, Iteration: iter, Delta: chunk.Delta}); err != nil {
					return llm.Message{}, err
				}
			}
			if chunk.ToolCallDelta != nil {
				key := chunk.ToolCallDelta.ID
				if key == "" {
					key = fmt.Sprintf("anon-%d", len(order))
				}
				st, exists := calls[key]
				if !exists {
					st = &callState{call: llm.ToolCall{}}
					calls[key] = st
					order = append(order, key)
				}
				if chunk.ToolCallDelta.ID != "" {
					st.call.ID = chunk.ToolCallDelta.ID
				}
				if chunk.ToolCallDelta.Name != "" {
					st.call.Name = chunk.ToolCallDelta.Name
				}
				/*
					Provider streams emit a running snapshot of accumulated
					arguments rather than per-chunk fragments; replace rather
					than append.
				*/
				st.call.Arguments = append(st.call.Arguments[:0], chunk.ToolCallDelta.Arguments...)

				if !st.emitted && st.call.Name != "" {
					st.emitted = true
					if err := sink.Emit(StreamEvent{
						Kind:         StreamEventToolCallStart,
						Iteration:    iter,
						ToolCallID:   st.call.ID,
						ToolCallName: st.call.Name,
					}); err != nil {
						return llm.Message{}, err
					}
				}
				if len(st.call.Arguments) > 0 && st.emitted {
					if err := sink.Emit(StreamEvent{
						Kind:         StreamEventToolCallArgDelta,
						Iteration:    iter,
						ToolCallID:   st.call.ID,
						ToolCallName: st.call.Name,
						ToolCallArgs: append(json.RawMessage{}, st.call.Arguments...),
					}); err != nil {
						return llm.Message{}, err
					}
				}
			}
			if chunk.Done {
				goto finalize
			}
		}
	}

finalize:
	out := llm.Message{Role: llm.RoleAssistant, Content: content}
	for _, k := range order {
		st := calls[k]
		out.ToolCalls = append(out.ToolCalls, st.call)
		if err := sink.Emit(StreamEvent{
			Kind:         StreamEventToolCallEnd,
			Iteration:    iter,
			ToolCallID:   st.call.ID,
			ToolCallName: st.call.Name,
			ToolCallArgs: append(json.RawMessage{}, st.call.Arguments...),
		}); err != nil {
			return llm.Message{}, err
		}
	}
	return out, nil
}
