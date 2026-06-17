package main

import (
	"context"
	"fmt"

	"github.com/smallnest/langgraphgo/graph"
	"github.com/tmc/langchaingo/llms"
)

/*
runGraph builds and executes a two-node self-correction graph against model:

	answer -> ground_check -> END

"answer" drafts a reply; "ground_check" asks the model to re-state it and append
an explicit confidence/caveat line. This is the minimal shape of the "extra
confidence" loop the cybersecurity-research workflow wants, expressed as a
langgraphgo StateGraph over map[string]any state.
*/
func runGraph(ctx context.Context, model llms.Model, input string) (map[string]any, error) {
	g := graph.NewStateGraph[map[string]any]()

	g.AddNode("answer", "answer", func(ctx context.Context, state map[string]any) (map[string]any, error) {
		q, _ := state["input"].(string)
		draft, err := model.Call(ctx, q)
		if err != nil {
			return nil, err
		}
		state["draft"] = draft
		return state, nil
	})

	g.AddNode("ground_check", "ground_check", func(ctx context.Context, state map[string]any) (map[string]any, error) {
		draft, _ := state["draft"].(string)
		prompt := "Review the answer below. Re-state it, then append a final line " +
			"'CONFIDENCE: <high|medium|low> — <one-line caveat>'.\n\nAnswer:\n" + draft
		out, err := model.Call(ctx, prompt)
		if err != nil {
			return nil, err
		}
		state["output"] = out
		return state, nil
	})

	g.AddEdge("answer", "ground_check")
	g.AddEdge("ground_check", graph.END)
	g.SetEntryPoint("answer")

	runnable, err := g.Compile()
	if err != nil {
		return nil, fmt.Errorf("compile graph: %w", err)
	}
	return runnable.Invoke(ctx, map[string]any{"input": input})
}
