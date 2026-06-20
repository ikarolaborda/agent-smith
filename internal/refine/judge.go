package refine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

/*
Judge is one strict, independent assessor of a candidate answer. It is a SEPARATE
role from internal/validate's advisory reviewer: different prompt, different
output contract, and a binary pass/fail rather than a non-authoritative note.
*/
type Judge interface {
	Name() string
	Judge(ctx context.Context, task, answer string) (Verdict, error)
}

/*
judgeSystemPrompt encodes the anti-fabrication rubric. The single most important
rule is that an honest negative is a PASS: the loop must never pressure the model
into inventing a finding to satisfy the judge.
*/
const judgeSystemPrompt = "You are a strict, skeptical judge of security-research output. " +
	"You are given a TASK and a candidate ANSWER. Decide whether the ANSWER is USABLE.\n\n" +
	"USABLE means ALL of: (1) grounded — every CVE id, CVSS score, affected-version range, " +
	"memory offset, and exploit primitive is tied to evidence in the answer, not asserted from thin air; " +
	"(2) feasible and technically coherent; (3) honestly scoped — explicit about what is unconfirmed, " +
	"and an honest negative such as 'no confirmed vulnerability' or 'needs a sanitizer build to confirm' " +
	"IS usable when it is well reasoned; (4) correctly labelled — it distinguishes an observation from a " +
	"candidate issue from a candidate 0-day, never conflating hypothesis with evidence.\n\n" +
	"NOT_USABLE means ANY of: fabricated or unsupported specifics (invented CVE/CVSS/version/offset), " +
	"confident claims without evidence, hypothesis presented as fact, or incoherent analysis.\n\n" +
	"Do NOT require a positive vulnerability finding. A careful, grounded negative is a PASS. " +
	"Never reward confidence; reward grounding and honest scoping.\n\n" +
	"Respond in EXACTLY this format and nothing else:\n" +
	"VERDICT: USABLE or NOT_USABLE\n" +
	"REASONS: <one concise line>\n" +
	"FIXES: <semicolon-separated concrete fixes, or NONE>\n" +
	"FAILURE_MODES: <semicolon-separated short tags like fabricated-cve;unscoped-confidence, or NONE>"

/*
OpenAIJudge is a Judge backed by an OpenAI reasoning model (gpt-5.x) through the
shared llm.Provider. It reuses the OpenAI client whose reasoning-model request
shaping is already handled, and never sets a temperature.
*/
type OpenAIJudge struct {
	model    string
	provider llm.Provider
}

/*
NewOpenAIJudge builds an OpenAI-backed judge, returning nil when no API key is
configured so the caller can detect the missing backend and refuse to run the
loop rather than silently degrade.
*/
func NewOpenAIJudge(apiKey, baseURL, model string) *OpenAIJudge {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	client, err := openai.New(openai.Config{APIKey: apiKey, BaseURL: baseURL, Model: model})
	if err != nil {
		return nil
	}
	return &OpenAIJudge{model: model, provider: client}
}

/* Name identifies the judge in ledgers and logs. */
func (j *OpenAIJudge) Name() string {
	if j.model == "" {
		return "openai-judge"
	}
	return "openai-judge:" + j.model
}

/*
Judge asks the model to assess the answer against the rubric and parses the
structured reply. A transport error is returned to the caller; an empty or
unparsable reply is handled by the caller as a fail-closed NOT_USABLE (see Loop),
so the judge here only reports genuine call failures as errors.
*/
func (j *OpenAIJudge) Judge(ctx context.Context, task, answer string) (Verdict, error) {
	if j == nil || j.provider == nil {
		return Verdict{}, errors.New("refine: nil judge")
	}
	user := fmt.Sprintf("TASK:\n%s\n\nANSWER:\n%s", task, answer)
	resp, err := j.provider.Chat(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: judgeSystemPrompt},
			{Role: llm.RoleUser, Content: user},
		},
	})
	if err != nil {
		return Verdict{}, err
	}
	if resp == nil {
		return Verdict{}, errors.New("refine: nil judge response")
	}
	return parseVerdict(resp.Message.Content), nil
}

/*
parseVerdict reads the labelled-line judge contract. It fails closed: anything it
cannot confidently read as USABLE is NOT_USABLE, so an ambiguous or malformed
judge reply can never be mistaken for a pass.
*/
func parseVerdict(raw string) Verdict {
	v := Verdict{Usable: false}
	sawVerdict := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "VERDICT:"):
			val := strings.ToUpper(strings.TrimSpace(line[len("VERDICT:"):]))
			sawVerdict = true
			/* NOT_USABLE is checked first so the substring USABLE inside it never flips the verdict. */
			if strings.Contains(val, "NOT_USABLE") || strings.Contains(val, "NOT USABLE") {
				v.Usable = false
			} else if strings.Contains(val, "USABLE") {
				v.Usable = true
			}
		case strings.HasPrefix(strings.ToUpper(line), "REASONS:"):
			v.Reasons = strings.TrimSpace(line[len("REASONS:"):])
		case strings.HasPrefix(strings.ToUpper(line), "FIXES:"):
			v.Fixes = splitTags(line[len("FIXES:"):])
		case strings.HasPrefix(strings.ToUpper(line), "FAILURE_MODES:"):
			v.FailureModes = splitTags(line[len("FAILURE_MODES:"):])
		}
	}
	if !sawVerdict {
		v.Usable = false
		if v.Reasons == "" {
			v.Reasons = "judge output unparsable"
		}
		v.FailureModes = append(v.FailureModes, "unparsable-verdict")
	}
	return v
}

/* splitTags parses a semicolon-separated field, dropping the NONE sentinel and blanks. */
func splitTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "NONE") {
		return nil
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ";") {
		if p := strings.TrimSpace(part); p != "" && !strings.EqualFold(p, "NONE") {
			out = append(out, p)
		}
	}
	return out
}
