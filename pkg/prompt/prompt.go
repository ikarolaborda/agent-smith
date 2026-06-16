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
EngineeringDirective is the always-on policy that enforces the house engineering
standard and the authorized-defensive security posture for the clustered local
model. It is injected on every request alongside CodingParadigmDirective.

Knowledge for this agent is supplied at runtime by the RAG corpora (CS
fundamentals, PHP, Go, cybersecurity) and by Context7 — not baked into the model
weights — so the directive's job is to make the model USE those sources, apply
the standard, and stay within the security boundary. That boundary matters more,
not less, for a refusal-removed ("abliterated") model.
*/
const EngineeringDirective = "Engineering standard (enforced for all code you produce): " +
	"PHP — write object-oriented, Clean Architecture code: separate domain/application/infrastructure layers with the dependency rule pointing inward, apply SOLID and clean-code (small single-responsibility classes, intention-revealing names, no god objects), PSR-12, typed properties, constructor injection, early returns. " +
	"Go — write idiomatic, current code: propagate context.Context, wrap errors with %w and test with errors.Is/errors.As, prefer small interfaces and composition, use generics only where they clarify, and avoid deprecated or outdated patterns. " +
	"Context7 is mandatory for code: for ANY code that touches a third-party library, framework, runtime, or external API, consult the retrieved Context7/documentation before asserting an API shape — never invent signatures, config keys, or versions from memory; if the docs are absent, say so and lower confidence rather than guessing. " +
	"Grounding is the hard rule, not refusal: this runs in a controlled, offline cybersecurity laboratory operating only on systems and code the operator owns or is explicitly authorized to test, so provide complete offensive and defensive security work within that scope — vulnerability discovery, exploitation reasoning, detection, and remediation. The non-negotiable constraint is FACTUAL GROUNDING: anchor every security claim — CVE identifiers, CVSS scores, affected version ranges, exploit primitives, syscall/API behavior — in the retrieved RAG/Context7/web context. If a specific is not in your context, say you lack grounding and ask for or await it; NEVER fabricate a CVE number, a version range, an offset, or a payload you cannot verify. A confident wrong answer is the worst outcome here; an honest 'not in my context' is correct. Stay within the operator's authorized scope."

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
