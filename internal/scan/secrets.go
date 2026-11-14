// Package scan detects secrets and prompt-injection patterns in memory text.
package scan

import (
	_ "embed"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Finding names the rule that matched; it never carries the matched text.
type Finding struct {
	Rule string
}

// gitleaks.toml is the gitleaks v8.30.1 default config (MIT, see rules/LICENSE.gitleaks).
//
//go:embed rules/gitleaks.toml
var gitleaksConfig string

type gitleaksFile struct {
	Allowlist gitleaksAllowlist `toml:"allowlist"`
	Rules     []gitleaksRule    `toml:"rules"`
}

type gitleaksRule struct {
	ID          string              `toml:"id"`
	Regex       string              `toml:"regex"`
	Keywords    []string            `toml:"keywords"`
	Entropy     float64             `toml:"entropy"`
	SecretGroup int                 `toml:"secretGroup"`
	Allowlists  []gitleaksAllowlist `toml:"allowlists"`
}

type gitleaksAllowlist struct {
	Condition   string   `toml:"condition"`
	RegexTarget string   `toml:"regexTarget"`
	Regexes     []string `toml:"regexes"`
	Stopwords   []string `toml:"stopwords"`
}

type allowlist struct {
	and       bool
	target    string
	regexes   []*regexp.Regexp
	stopwords []string
}

type rule struct {
	id          string
	re          *regexp.Regexp
	keywords    []string
	entropy     float64
	secretGroup int
	allowlists  []allowlist
}

// SecretScanner applies the gitleaks rules plus memorable's own rules.
type SecretScanner struct {
	rules  []rule
	global []allowlist
}

var (
	urlCredentials = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:[^\s@/]{3,}@[^\s/]+`)
	tokenPattern   = regexp.MustCompile(`[A-Za-z0-9+/_=-]{24,}`)
)

// NewSecretScanner parses the embedded rule set.
func NewSecretScanner() (*SecretScanner, error) {
	var f gitleaksFile
	if _, err := toml.Decode(gitleaksConfig, &f); err != nil {
		return nil, fmt.Errorf("parsing gitleaks rules: %w", err)
	}
	s := &SecretScanner{global: compileAllowlists([]gitleaksAllowlist{f.Allowlist})}
	for _, r := range f.Rules {
		if r.Regex == "" {
			continue
		}
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			continue
		}
		kws := make([]string, 0, len(r.Keywords))
		for _, k := range r.Keywords {
			kws = append(kws, strings.ToLower(k))
		}
		s.rules = append(s.rules, rule{
			id: r.ID, re: re, keywords: kws, entropy: r.Entropy,
			secretGroup: r.SecretGroup, allowlists: compileAllowlists(r.Allowlists),
		})
	}
	return s, nil
}

func compileAllowlists(in []gitleaksAllowlist) []allowlist {
	out := make([]allowlist, 0, len(in))
	for _, a := range in {
		al := allowlist{and: strings.EqualFold(a.Condition, "AND"), target: a.RegexTarget}
		for _, r := range a.Regexes {
			if re, err := regexp.Compile(r); err == nil {
				al.regexes = append(al.regexes, re)
			}
		}
		for _, w := range a.Stopwords {
			al.stopwords = append(al.stopwords, strings.ToLower(w))
		}
		if len(al.regexes) > 0 || len(al.stopwords) > 0 {
			out = append(out, al)
		}
	}
	return out
}

// Scan returns one finding per matching rule.
func (s *SecretScanner) Scan(text string) []Finding {
	var out []Finding
	lower := strings.ToLower(text)
	for i := range s.rules {
		if s.rules[i].matches(text, lower, s.global) {
			out = append(out, Finding{Rule: s.rules[i].id})
		}
	}
	if urlCredentials.MatchString(text) {
		out = append(out, Finding{Rule: "url-credentials"})
	}
	if hasHighEntropyToken(text) {
		out = append(out, Finding{Rule: "high-entropy-token"})
	}
	return out
}

func (r *rule) matches(text, lower string, global []allowlist) bool {
	if len(r.keywords) > 0 && !containsAny(lower, r.keywords) {
		return false
	}
	for _, loc := range r.re.FindAllStringSubmatchIndex(text, -1) {
		match := text[loc[0]:loc[1]]
		secret := extractSecret(text, loc, r.secretGroup)
		if r.entropy > 0 && shannon(secret) < r.entropy {
			continue
		}
		line := lineAround(text, loc[0], loc[1])
		if allowed(r.allowlists, secret, match, line) || allowed(global, secret, match, line) {
			continue
		}
		return true
	}
	return false
}

func extractSecret(text string, loc []int, group int) string {
	if group > 0 && 2*group+1 < len(loc) && loc[2*group] >= 0 {
		return text[loc[2*group]:loc[2*group+1]]
	}
	for g := 1; 2*g+1 < len(loc); g++ {
		if loc[2*g] >= 0 && loc[2*g+1] > loc[2*g] {
			return text[loc[2*g]:loc[2*g+1]]
		}
	}
	return text[loc[0]:loc[1]]
}

func lineAround(text string, start, end int) string {
	ls := strings.LastIndexByte(text[:start], '\n') + 1
	le := strings.IndexByte(text[end:], '\n')
	if le < 0 {
		return text[ls:]
	}
	return text[ls : end+le]
}

func allowed(lists []allowlist, secret, match, line string) bool {
	for _, a := range lists {
		target := secret
		switch a.target {
		case "match":
			target = match
		case "line":
			target = line
		}
		regexHit := false
		for _, re := range a.regexes {
			if re.MatchString(target) {
				regexHit = true
				break
			}
		}
		stopHit := containsAny(strings.ToLower(secret), a.stopwords)
		if a.and {
			if (len(a.regexes) == 0 || regexHit) && (len(a.stopwords) == 0 || stopHit) {
				return true
			}
			continue
		}
		if regexHit || stopHit {
			return true
		}
	}
	return false
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func hasHighEntropyToken(text string) bool {
	for _, tok := range tokenPattern.FindAllString(text, -1) {
		var upper, lower, digit bool
		for _, r := range tok {
			switch {
			case unicode.IsUpper(r):
				upper = true
			case unicode.IsLower(r):
				lower = true
			case unicode.IsDigit(r):
				digit = true
			}
		}
		if upper && lower && digit && shannon(tok) >= 4.5 {
			return true
		}
	}
	return false
}

func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}
