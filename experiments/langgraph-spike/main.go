package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

/*
main runs the spike live end-to-end: an agent-smith Ollama Provider, wrapped by
the ProviderModel adapter into a langchaingo llms.Model, driving a langgraphgo
graph against a local model. Override the model with SPIKE_MODEL.
*/
func main() {
	model := os.Getenv("SPIKE_MODEL")
	if model == "" {
		model = "huihui_ai/gpt-oss-abliterated:20b"
	}

	provider := newOllamaProvider("", model)
	llm := NewProviderModel(provider, model)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	final, err := runGraph(ctx, llm, "In one sentence, what is a buffer overflow?")
	if err != nil {
		fmt.Println("graph error:", err)
		os.Exit(1)
	}

	fmt.Println("===== draft (answer node) =====")
	fmt.Println(final["draft"])
	fmt.Println("\n===== output (after ground_check node) =====")
	fmt.Println(final["output"])
}
