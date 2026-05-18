/*
Package prompt exposes small, reusable helpers for assembling system and
user prompts. It is deliberately kept separate from the agent package so
external callers (e.g. CLI shims, integration tests) can import it without
pulling in the agent loop.
*/
package prompt

import "strings"

/*
JoinSections concatenates non-empty sections with a blank line between them.
It trims trailing whitespace on each section so callers do not have to worry
about stray newlines.
*/
func JoinSections(sections ...string) string {
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		s = strings.TrimRight(s, " \t\r\n")
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}

/*
WithTools returns systemPrompt suffixed with a short tool-usage hint. Used
when the model needs an explicit nudge that tools are available; otherwise
the provider's native tool schema is enough.
*/
func WithTools(systemPrompt string, toolNames []string) string {
	if len(toolNames) == 0 {
		return systemPrompt
	}
	hint := "Available tools: " + strings.Join(toolNames, ", ") + "."
	return JoinSections(systemPrompt, hint)
}
