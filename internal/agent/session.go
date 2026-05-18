package agent

import (
	"sync"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/*
Session holds the conversation state for a single Agent run. It is safe for
concurrent use by a single agent loop; do not share a Session across
concurrent agents.
*/
type Session struct {
	mu       sync.Mutex
	messages []llm.Message
}

/* NewSession returns an empty Session. */
func NewSession() *Session {
	return &Session{}
}

/* Append adds a message to the conversation history. */
func (s *Session) Append(m llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
}

/*
Messages returns a defensive copy of the current message history. The Agent
prepends its system prompt at request time, so Session itself does not store
one.
*/
func (s *Session) Messages() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]llm.Message, len(s.messages))
	copy(out, s.messages)
	return out
}

/* Reset clears the conversation history. */
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
}

/* Len returns the number of messages currently in the session. */
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}
