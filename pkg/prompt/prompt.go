/*
Package prompt exposes small, reusable helpers for assembling system and
user prompts. It is deliberately kept separate from the agent package so
external callers (e.g. CLI shims, integration tests) can import it without
pulling in the agent loop.
*/
package prompt

import "strings"

/*
CodingParadigmDirective is a baseline system instruction applied to every model
on every request. It steers code generation toward object-oriented design while
leaving an explicit escape hatch so procedural code is still produced where it
is genuinely the better fit (or when the user asks for it).
*/
const CodingParadigmDirective = "When asked to write, refactor, or modify code for any purpose, prefer an object-oriented design: encapsulate related state and behaviour in classes/objects with clear, single responsibilities, and favour composition and well-named types over free-standing procedural routines. Choose OOP over a procedural style by default unless the user explicitly requests procedural code or the language/context makes OOP inappropriate (for example SQL, shell one-liners, or a language without object support)."

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
