#!/usr/bin/env bash
#
# seed-corrections.sh — seed the running agent-smith server's per-profile memory
# with confirmed prior hallucinations, so the RAG augment surfaces them and the
# always-on grounding guardrail ("the retrieved correction wins") makes the model
# aware of mistakes it has made before.
#
# These two corrections are NVD-confirmed (Tier-3): the abliteration model, asked
# for a PHP 7.4 CVE candidate WITHOUT web grounding, fabricated CVE->product
# mappings and CVSS scores. The real records, per
# https://services.nvd.nist.gov/rest/json/cves/2.0?cveId=<ID> :
#   - CVE-2021-33199 is ExpressionEngine (NOT PHP GD); CVSS is not 8.8.
#   - CVE-2021-30642 is Symantec Security Analytics (NOT PHP); CVSS is not 8.8.
#
# Usage:
#   AGENT_HOST=http://localhost:8080 PROFILE_ID=default ./scripts/seed-corrections.sh
#
# Defaults: AGENT_HOST=http://localhost:8080, PROFILE_ID=default.
# Requires: a running server with RAG configured (the /v1/rag/correction endpoint).

set -euo pipefail

AGENT_HOST="${AGENT_HOST:-http://localhost:8080}"
PROFILE_ID="${PROFILE_ID:-default}"
ENDPOINT="${AGENT_HOST%/}/v1/rag/correction"

post_correction() {
	local question="$1" wrong="$2" correct="$3"
	curl -fsS -X POST "$ENDPOINT" \
		-H 'Content-Type: application/json' \
		-d "$(jq -n \
			--arg p "$PROFILE_ID" \
			--arg q "$question" \
			--arg w "$wrong" \
			--arg c "$correct" \
			'{profile_id:$p, question:$q, wrong_answer:$w, correct_answer:$c}')" \
		>/dev/null
	echo "seeded correction: ${question}"
}

post_correction \
	"What is a CVE candidate affecting PHP 7.4 (e.g. in the GD image library)?" \
	"CVE-2021-33199 affects the PHP GD library with CVSS 8.8." \
	"CVE-2021-33199 is an ExpressionEngine vulnerability, NOT PHP GD, and its CVSS is not 8.8. Do not attribute it to PHP. Confirm any PHP CVE against the NVD record before asserting it."

post_correction \
	"What is a CVE candidate affecting PHP 7.4?" \
	"CVE-2021-30642 is a PHP 7.4 vulnerability with CVSS 8.8." \
	"CVE-2021-30642 is a Symantec Security Analytics vulnerability, NOT PHP, and its CVSS is not 8.8. Do not attribute it to PHP. Confirm any PHP CVE against the NVD record before asserting it."

echo "done — corrections stored for profile '${PROFILE_ID}' at ${ENDPOINT}"
