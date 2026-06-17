/*
Package dataset builds supervised fine-tuning (SFT) datasets natively, with no
third-party framework. It turns "rich" source items — Context7 docs, RAG corpus
chunks, or files in a folder — into OpenAI chat fine-tuning JSONL by asking any
configured llm.Provider (e.g. the remote abliteration model) to produce a
grounded training example per source item.

The generation is deliberately low-temperature and grounding-constrained: the
assistant must answer ONLY from the supplied source and must not invent facts —
the same posture abliteration.ai's own dataset product enforces, and the same
anti-hallucination boundary the rest of agent-smith uses.
*/
package dataset

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* DefaultTemperature keeps generated examples factual and low-creativity. */
const DefaultTemperature = 0.2

/* DefaultMaxItemBytes caps how much of any single source item is sent to the model. */
const DefaultMaxItemBytes = 24 * 1024

/* SourceItem is one unit of source material (a doc, a chunk, or a file). */
type SourceItem struct {
	ID      string
	Content string
}

/* Msg is one chat turn in a training record. */
type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

/* Record is a single OpenAI chat fine-tuning example: {"messages":[...]}. */
type Record struct {
	Messages []Msg `json:"messages"`
}

/*
Options tune generation. SystemPrompt is the analyst persona written into both
the generation request and the emitted record; Instruction is the user task.
Temperature and MaxItemBytes fall back to the package defaults when zero.
*/
type Options struct {
	SystemPrompt string
	Instruction  string
	Temperature  float64
	MaxItemBytes int
}

const defaultSystemPrompt = "You are a defensive cybersecurity analyst. Use ONLY the provided source material. " +
	"Do not invent facts, identifiers, versions, or details that are not present in the source; " +
	"if something is not in the source, say it is not available."

const defaultInstruction = "Using only the source material below, write a single grounded question a practitioner " +
	"would ask, then answer it strictly from the source. Do not add information beyond the source."

func (o Options) withDefaults() Options {
	if o.SystemPrompt == "" {
		o.SystemPrompt = defaultSystemPrompt
	}
	if o.Instruction == "" {
		o.Instruction = defaultInstruction
	}
	if o.Temperature == 0 {
		o.Temperature = DefaultTemperature
	}
	if o.MaxItemBytes <= 0 {
		o.MaxItemBytes = DefaultMaxItemBytes
	}
	return o
}

/*
Build generates one grounded Chat-SFT record per source item by calling the
provider. Items that fail generation are skipped (and counted in skipped); the
returned slice preserves input order for the successes.
*/
func Build(ctx context.Context, provider llm.Provider, items []SourceItem, opts Options) (records []Record, skipped int, err error) {
	if provider == nil {
		return nil, 0, fmt.Errorf("dataset: nil provider")
	}
	opts = opts.withDefaults()
	temp := opts.Temperature

	for _, it := range items {
		content := it.Content
		if len(content) > opts.MaxItemBytes {
			content = content[:opts.MaxItemBytes]
		}
		userPrompt := opts.Instruction + "\n\nSource (id=" + it.ID + "):\n" + content

		resp, gErr := provider.Chat(ctx, llm.ChatRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: opts.SystemPrompt},
				{Role: llm.RoleUser, Content: userPrompt},
			},
			Temperature: &temp,
		})
		if gErr != nil || resp == nil || strings.TrimSpace(resp.Message.Content) == "" {
			skipped++
			continue
		}

		records = append(records, Record{Messages: []Msg{
			{Role: "system", Content: opts.SystemPrompt},
			{Role: "user", Content: opts.Instruction},
			{Role: "assistant", Content: resp.Message.Content},
		}})
	}
	return records, skipped, nil
}

/* WriteJSONL emits records as OpenAI chat fine-tuning JSONL, one object per line. */
func WriteJSONL(w io.Writer, records []Record) error {
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}
