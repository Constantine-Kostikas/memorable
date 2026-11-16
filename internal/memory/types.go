// Package memory holds the domain model and business rules of memorable.
package memory

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// enumNames maps an enum's values (index = value) to their wire names; index 0 is "unknown".
type enumNames []string

func (n enumNames) name(v uint8) string {
	if int(v) >= len(n) {
		return "unknown"
	}
	return n[v]
}

func (n enumNames) parse(kind, s string) (uint8, error) {
	for i := 1; i < len(n); i++ {
		if n[i] == s {
			return uint8(i), nil //nolint:gosec // enum tables have far fewer than 256 entries
		}
	}
	return 0, fmt.Errorf("%w: unknown %s %q", ErrValidation, kind, s)
}

// Status is the lifecycle state of a memory.
type Status uint8

// Status values.
const (
	StatusUnknown Status = iota
	StatusActive
	StatusUnverified
	StatusFlagged
	StatusQuarantined
	StatusSuperseded
	StatusDeleted
)

var statusNames = enumNames{"unknown", "active", "unverified", "flagged", "quarantined", "superseded", "deleted"}

func (s Status) String() string { return statusNames.name(uint8(s)) }

// ParseStatus parses a status wire name.
func ParseStatus(s string) (Status, error) {
	v, err := statusNames.parse("status", s)
	return Status(v), err
}

// MarshalText implements encoding.TextMarshaler.
func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Status) UnmarshalText(b []byte) error {
	v, err := ParseStatus(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Type classifies what a memory is about.
type Type uint8

// Type values.
const (
	TypeUnknown Type = iota
	TypeUser
	TypeFeedback
	TypeProject
	TypeReference
)

var typeNames = enumNames{"unknown", "user", "feedback", "project", "reference"}

func (t Type) String() string { return typeNames.name(uint8(t)) }

// ParseType parses a type wire name.
func ParseType(s string) (Type, error) {
	v, err := typeNames.parse("type", s)
	return Type(v), err
}

// MarshalText implements encoding.TextMarshaler.
func (t Type) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *Type) UnmarshalText(b []byte) error {
	v, err := ParseType(string(b))
	if err != nil {
		return err
	}
	*t = v
	return nil
}

// Source records where a memory's content came from.
type Source uint8

// Source values.
const (
	SourceUnknown Source = iota
	SourceUserStated
	SourceClaudeInferred
	SourceToolOutput
	SourceImported
)

var sourceNames = enumNames{"unknown", "user_stated", "claude_inferred", "tool_output", "imported"}

func (s Source) String() string { return sourceNames.name(uint8(s)) }

// ParseSource parses a source wire name.
func ParseSource(s string) (Source, error) {
	v, err := sourceNames.parse("source", s)
	return Source(v), err
}

// MarshalText implements encoding.TextMarshaler.
func (s Source) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Source) UnmarshalText(b []byte) error {
	v, err := ParseSource(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// FeedbackKind is the verdict carried by memory feedback.
type FeedbackKind uint8

// FeedbackKind values.
const (
	FeedbackUnknown FeedbackKind = iota
	FeedbackUseful
	FeedbackWrong
	FeedbackOutdated
)

var feedbackNames = enumNames{"unknown", "useful", "wrong", "outdated"}

func (f FeedbackKind) String() string { return feedbackNames.name(uint8(f)) }

// ParseFeedbackKind parses a feedback kind wire name.
func ParseFeedbackKind(s string) (FeedbackKind, error) {
	v, err := feedbackNames.parse("feedback kind", s)
	return FeedbackKind(v), err
}

// MarshalText implements encoding.TextMarshaler.
func (f FeedbackKind) MarshalText() ([]byte, error) { return []byte(f.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (f *FeedbackKind) UnmarshalText(b []byte) error {
	v, err := ParseFeedbackKind(string(b))
	if err != nil {
		return err
	}
	*f = v
	return nil
}

// Scope says where a memory lives relative to the current session.
type Scope uint8

// Scope values.
const (
	ScopeUnknown Scope = iota
	ScopeProject
	ScopeGlobal
	ScopeProfile
)

var scopeNames = enumNames{"unknown", "project", "global", "profile"}

func (s Scope) String() string { return scopeNames.name(uint8(s)) }

// ParseScope parses a scope wire name.
func ParseScope(s string) (Scope, error) {
	v, err := scopeNames.parse("scope", s)
	return Scope(v), err
}

// MarshalText implements encoding.TextMarshaler.
func (s Scope) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// OnSimilar decides what a write does when a similar memory exists.
type OnSimilar uint8

// OnSimilar values; the zero value asks.
const (
	OnSimilarAsk OnSimilar = iota
	OnSimilarKeepBoth
)

var onSimilarNames = enumNames{"ask", "keep_both"}

func (o OnSimilar) String() string { return onSimilarNames.name(uint8(o)) }

// ParseOnSimilar parses an on_similar wire name; "ask" maps to the zero value.
func ParseOnSimilar(s string) (OnSimilar, error) {
	if s == "" || s == "ask" {
		return OnSimilarAsk, nil
	}
	v, err := onSimilarNames.parse("on_similar", s)
	return OnSimilar(v), err
}

// Memory is a stored memory.
type Memory struct {
	ID             uuid.UUID
	Owner          string
	Slug           *string
	Project        *string
	Type           Type
	Title          string
	Content        string
	Tags           []string
	Source         Source
	Status         Status
	Pinned         bool
	ExpiresAt      *time.Time
	Version        int
	ContentHash    []byte
	EmbeddingModel *string
	AccessCount    int64
	AccessScore    float64
	ScoreUpdatedAt time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastAccessedAt *time.Time
	DeletedAt      *time.Time
	Scope          Scope
}

// NewMemory is the input for creating a memory.
type NewMemory struct {
	Title      string
	Content    string
	Type       Type
	Tags       []string
	Slug       *string
	Source     Source
	Global     bool
	Pinned     bool
	ExpiresAt  *time.Time
	Supersedes *uuid.UUID
	OnSimilar  OnSimilar
}

// Patch is a partial update; nil fields are unchanged.
type Patch struct {
	Title     *string
	Content   *string
	Tags      *[]string
	Type      *Type
	Pinned    *bool
	ExpiresAt **time.Time
	// Status is set by the service (for example to quarantine an edit) and is not exposed to tools.
	Status *Status
}

// Summary is the compact form returned by search and listings.
type Summary struct {
	ID      uuid.UUID
	Slug    *string
	Project *string
	Title   string
	Snippet string
	Type    Type
	Tags    []string
	Scope   Scope
	Status  Status
	Score   float64
	// UpdatedAt is shown by the web UI only; MCP renderings omit it.
	UpdatedAt time.Time
}
