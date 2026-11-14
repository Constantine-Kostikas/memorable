package scan

import "regexp"

var (
	agentOverride = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bignore (?:all |any )?(?:previous|prior|above) (?:instructions|prompts?)\b`),
		regexp.MustCompile(`(?i)\bdisregard (?:the )?(?:system|previous)\b`),
		regexp.MustCompile(`(?i)\byou must always\b`),
		regexp.MustCompile(`(?i)\bsystem prompt\b`),
	}
	pipeToShell = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n|]*\|\s*(?:ba|z)?sh\b`),
		regexp.MustCompile(`(?i)\bbase64\s+(?:-d|--decode)\b[^\n]*\|\s*(?:ba|z)?sh\b`),
	}
	destructive   = regexp.MustCompile(`\brm\s+-rf\s+[/~*]`)
	hiddenUnicode = regexp.MustCompile(`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{FEFF}]`)
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
