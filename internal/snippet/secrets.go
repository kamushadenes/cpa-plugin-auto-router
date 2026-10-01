package snippet

import (
	"regexp"
	"strings"
)

// secretPatterns are ported from dirien/jev-router src/secrets.mjs (commit
// f9093109). Each match becomes "[REDACTED <kind>]".
var secretPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`)},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-(?:api|admin|oat|ort)\d{2}-[A-Za-z0-9_-]{20,}`)},
	{"openrouter-key", regexp.MustCompile(`\bsk-or-v1-[A-Za-z0-9]{32,}`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}|\bsk-[A-Za-z0-9]{40,}\b`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"aws-secret-key", regexp.MustCompile(`(?i)\baws_secret_access_key\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}\b`)},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{40,})\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"stripe-key", regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"pulumi-token", regexp.MustCompile(`\bpul-[a-f0-9]{40}\b`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{"url-credentials", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@"'<>]+:[^\s/@"'<>]+@`)},
}

// assignment is key = value; group 1 is the value. RE2 has no lookahead, so
// references (process.env.X, $VAR, ${VAR}, <placeholder>, ***) are rejected
// in code on the value's prefix.
var assignment = regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|client_secret|api[_-]?key|access[_-]?token|auth[_-]?token)["']?\s*[:=]\s*["']?([^\s"',;)]{8,})`)

var assignmentReferences = []string{"process.env", "os.environ", "os.getenv", "env.", "$", "<", "{{", "***"}

// Scrub replaces every secret in text with "[REDACTED <kind>]".
func Scrub(text string) string {
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllString(text, "[REDACTED "+p.kind+"]")
	}
	var out strings.Builder
	for pos := 0; ; {
		loc := assignment.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			out.WriteString(text[pos:])
			return out.String()
		}
		value := strings.ToLower(text[pos+loc[2] : pos+loc[3]])
		if isReference(value) {
			// The JS lookahead retries later start positions, so a later key inside this span still counts.
			out.WriteString(text[pos : pos+loc[0]+1])
			pos += loc[0] + 1
			continue
		}
		out.WriteString(text[pos : pos+loc[0]])
		out.WriteString("[REDACTED assignment]")
		pos += loc[1]
	}
}

func isReference(value string) bool {
	for _, prefix := range assignmentReferences {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
