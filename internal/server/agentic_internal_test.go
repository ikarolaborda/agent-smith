package server

import "testing"

/*
shouldAgentic must never enable agentic-RAG when there is no live RAG service to
drive the tools, regardless of the operator default or a request override — the
gate that keeps a nil/disabled RAG path from advertising retrieval tools it
cannot back.
*/
func TestShouldAgenticGatesOnLiveRAG(t *testing.T) {
	yes := true

	noRAG := &Server{rag: nil, agentic: true}
	if noRAG.shouldAgentic(&yes) {
		t.Error("agentic must stay off when RAG is nil, even with request override=true")
	}

	disabled := &Server{disableRAG: true, agentic: true}
	if disabled.shouldAgentic(&yes) {
		t.Error("agentic must stay off when RAG is disabled")
	}
}
