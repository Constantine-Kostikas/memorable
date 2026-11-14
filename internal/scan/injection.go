package scan

import "regexp"

var (
	agentOverride = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bignore\s+(?:(?:all|any|the|of|these|those)\s+)*(?:previous|prior|above|earlier)\s+(?:instructions|prompts?|rules)\b`),
		regexp.MustCompile(`(?i)\bdisregard\s+(?:(?:all|any|the)\s+)*(?:system|previous|prior)\b`),
		regexp.MustCompile(`(?i)\byou\s+must\s+always\b`),
		regexp.MustCompile(`(?i)\bsystem\s+prompt\b`),
	}
	pipeToShell = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n|]*\|\s*(?:ba|z)?sh\b`),
		regexp.MustCompile(`(?i)\bbase64\s+(?:-d|--decode)\b[^\n]*\|\s*(?:ba|z)?sh\b`),
	}
	destructive   = regexp.MustCompile(`\brm\s+-rf\s+[/~*]`)
	hiddenUnicode = regexp.MustCompile(`[\x{00AD}\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{FEFF}\x{E0000}-\x{E007F}]`)
	anyURL        = regexp.MustCompile(`(?i)\bhttps?://\S+`)
	shellCommand  = regexp.MustCompile(`(?i)\b(?:curl|wget|bash|sh -c|powershell|iex)\b`)
)

// Injection flags text that looks like instructions aimed at an agent or a payload to execute.
func Injection(text string) []Finding {
	var out []Finding
	if anyMatch(agentOverride, text) {
		out = append(out, Finding{Rule: "agent-override"})
	}
	if anyMatch(pipeToShell, text) {
		out = append(out, Finding{Rule: "pipe-to-shell"})
	}
	if destructive.MatchString(text) {
		out = append(out, Finding{Rule: "destructive-command"})
	}
	if hiddenUnicode.MatchString(text) {
		out = append(out, Finding{Rule: "hidden-unicode"})
	}
	if anyURL.MatchString(text) && shellCommand.MatchString(text) {
		out = append(out, Finding{Rule: "url-with-command"})
	}
	return out
}

func anyMatch(res []*regexp.Regexp, text string) bool {
	for _, re := range res {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}
