/*
Package docs exposes the curated Markdown knowledge shipped with agent-smith.

Keeping the corpus in an embed.FS gives fresh, single-binary installations a
read-only lexical grounding source before an operator has configured an
embedding provider or run the optional dense-index ingest command.
*/
package docs

import "embed"

/*
Corpus contains Markdown one directory below docs/. The broad pattern also
automatically picks up future corpus directories such as computer-networks and
software-engineering without requiring another code change; the RAG loader
still applies an explicit collection allowlist.
*/
//go:embed */*.md
var Corpus embed.FS
