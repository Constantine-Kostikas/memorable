package memory

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Input limits, counted in runes.
const (
	MaxTitleRunes   = 120
	MaxContentRunes = 2000
	MaxTags         = 10
	MaxBatch        = 20
	MaxQueryRunes   = 500
	DefaultK        = 5
	MaxK            = 20
	SnippetRunes    = 200
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/-]{1,79}$`)
	tagPattern  = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// Validate checks a new memory against the input limits.
func (n NewMemory) Validate() error {
	if err := validateTitle(n.Title); err != nil {
		return err
	}
	if err := validateContent(n.Content); err != nil {
		return err
	}
	if err := validateTags(n.Tags); err != nil {
		return err
	}
	if n.Slug != nil && !slugPattern.MatchString(*n.Slug) {
		return invalid("slug %q must match %s", *n.Slug, slugPattern)
	}
	if n.Type == TypeUnknown {
		return invalid("type is required")
	}
	if n.Source == SourceUnknown {
		return invalid("source is required")
	}
	return nil
}

// Validate checks a patch; an empty patch is invalid.
func (p Patch) Validate() error {
	if p.Title == nil && p.Content == nil && p.Tags == nil && p.Type == nil && p.Pinned == nil && p.ExpiresAt == nil {
		return invalid("patch changes nothing")
	}
	if p.Title != nil {
		if err := validateTitle(*p.Title); err != nil {
			return err
		}
	}
	if p.Content != nil {
		if err := validateContent(*p.Content); err != nil {
			return err
		}
	}
	if p.Tags != nil {
		if err := validateTags(*p.Tags); err != nil {
			return err
		}
	}
	if p.Type != nil && *p.Type == TypeUnknown {
		return invalid("type is required")
	}
	return nil
}

func validateTitle(s string) error {
	return validateText("title", s, MaxTitleRunes)
}

func validateContent(s string) error {
	return validateText("content", s, MaxContentRunes)
}

func validateText(field, s string, limit int) error {
	if !utf8.ValidString(s) {
		return invalid("%s is not valid UTF-8", field)
	}
	if strings.TrimSpace(s) == "" {
		return invalid("%s is required", field)
	}
	if n := utf8.RuneCountInString(s); n > limit {
		return invalid("%s has %d characters, limit is %d", field, n, limit)
	}
	return nil
}

func validateTags(tags []string) error {
	if len(tags) > MaxTags {
		return invalid("%d tags, limit is %d", len(tags), MaxTags)
	}
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		if !tagPattern.MatchString(t) {
			return invalid("tag %q must match %s", t, tagPattern)
		}
		if _, dup := seen[t]; dup {
			return invalid("duplicate tag %q", t)
		}
		seen[t] = struct{}{}
	}
	return nil
}

// ContentHash returns the sha256 of the normalized title and content.
func ContentHash(title, content string) []byte {
	sum := sha256.Sum256([]byte(normalize(title) + "\n" + normalize(content)))
	return sum[:]
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Snippet returns at most n runes of content, cut at a word boundary near the end, with an ellipsis when truncated.
func Snippet(content string, n int) string {
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= n {
		return string(runes)
	}
	cut := runes[:n]
	for i := len(cut) - 1; i >= 0 && i >= len(cut)-20; i-- {
		if cut[i] == ' ' {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRight(string(cut), " ") + "…"
}
