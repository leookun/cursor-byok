package aos

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildMemberRecognitionPrompt constructs the "roster review" prompt sent to
// the Leader. The Leader reads each member's name + systemPrompt and infers a
// small fixed set of routing tags (3-6 tags per member) that describe what
// that member is best at.
//
// The tags are NOT free-form: the Leader is asked to choose from a controlled
// vocabulary covering the common software-engineering domains, but may add a
// free-form tag when no controlled tag fits.
func buildMemberRecognitionPrompt(team *TeamProfile) string {
	var sb strings.Builder
	sb.WriteString("You are the team leader. Before any real work begins, you are meeting your team for the first time.\n")
	sb.WriteString("Read each member's name and role description (system prompt) below, then decide what each member is best at.\n\n")
	sb.WriteString("## Team Roster\n")
	for i, m := range team.Members {
		sb.WriteString(fmt.Sprintf("\n### Member %d\n", i+1))
		sb.WriteString(fmt.Sprintf("- ID: %s\n", m.ID))
		sb.WriteString(fmt.Sprintf("- Name: %s\n", m.Name))
		prompt := strings.TrimSpace(m.SystemPrompt)
		if prompt == "" {
			prompt = "(no system prompt provided)"
		}
		sb.WriteString(fmt.Sprintf("- Role description: %s\n", prompt))
		if len(m.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("- Existing tags (may be revised): %s\n", strings.Join(m.Tags, ", ")))
		}
	}
	sb.WriteString(`
## Your Task

For each member, output 3 to 6 routing tags that capture what they are best at.
Tags must be lowercase, single words or short hyphenated phrases (e.g. "frontend", "go", "api-design", "unit-testing").

Prefer this controlled vocabulary when it fits:
- frontend, backend, fullstack
- go, python, typescript, rust, java
- api, api-design, grpc, rest
- database, sql, postgres, redis
- vue, react, css, tailwind
- testing, unit-testing, e2e, qa
- devops, docker, k8s, ci-cd
- security, performance, observability
- documentation, research, analysis
- architecture, design, code-review

You MAY add a free-form tag if no controlled tag fits, but keep it short and lowercase.

Also write a one-sentence "summary" describing the member's specialty.

## Output Format

Output ONLY a JSON object with this exact shape, no markdown fences, no preamble:

{
  "members": [
    {
      "id": "<member ID exactly as given>",
      "name": "<member name, unchanged>",
      "tags": ["tag1", "tag2", "tag3"],
      "summary": "one short sentence"
    }
  ]
}

Rules:
- Include one entry per member, using the exact member ID from the roster.
- 3 to 6 tags per member. No empty tags. No duplicates within a member.
- Do not invent members. Do not omit members.
`)
	return sb.String()
}

// parseRecognizedMembers parses the Leader's recognition JSON output.
// Tolerates surrounding prose by extracting the first balanced JSON object.
func parseRecognizedMembers(text string) (*RecognizeMembersResult, error) {
	jsonStr := extractJSON(text)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON object found in leader output")
	}
	var raw struct {
		Members []RecognizedMember `json:"members"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("invalid recognition JSON: %w", err)
	}
	if len(raw.Members) == 0 {
		return nil, fmt.Errorf("recognition JSON has no members")
	}
	// Defensive: drop entries with empty ID.
	out := make([]RecognizedMember, 0, len(raw.Members))
	for _, m := range raw.Members {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("recognition JSON has no members with non-empty IDs")
	}
	return &RecognizeMembersResult{Members: out}, nil
}
