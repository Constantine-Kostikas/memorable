package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, s := range []Status{StatusActive, StatusUnverified, StatusFlagged, StatusQuarantined, StatusSuperseded, StatusDeleted} {
		got, err := ParseStatus(s.String())
		require.NoError(t, err)
		assert.Equal(t, s, got)
	}
	_, err := ParseStatus("bogus")
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, "unknown", Status(0).String())
	assert.Equal(t, "superseded", StatusSuperseded.String())
}

func TestType_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []Type{TypeUser, TypeFeedback, TypeProject, TypeReference} {
		got, err := ParseType(v.String())
		require.NoError(t, err)
		assert.Equal(t, v, got)
	}
	_, err := ParseType("note")
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, "unknown", Type(0).String())
}

func TestSource_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []Source{SourceUserStated, SourceClaudeInferred, SourceToolOutput, SourceImported} {
		got, err := ParseSource(v.String())
		require.NoError(t, err)
		assert.Equal(t, v, got)
	}
	assert.Equal(t, "tool_output", SourceToolOutput.String())
	_, err := ParseSource("web")
	require.ErrorIs(t, err, ErrValidation)
}

func TestFeedbackKind_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []FeedbackKind{FeedbackUseful, FeedbackWrong, FeedbackOutdated} {
		got, err := ParseFeedbackKind(v.String())
		require.NoError(t, err)
		assert.Equal(t, v, got)
	}
	_, err := ParseFeedbackKind("meh")
	require.ErrorIs(t, err, ErrValidation)
}

func TestScope_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []Scope{ScopeProject, ScopeGlobal, ScopeProfile} {
		got, err := ParseScope(v.String())
		require.NoError(t, err)
		assert.Equal(t, v, got)
	}
}

func TestOnSimilar_DefaultIsAsk(t *testing.T) {
	t.Parallel()
	var o OnSimilar
	assert.Equal(t, OnSimilarAsk, o)
	got, err := ParseOnSimilar("keep_both")
	require.NoError(t, err)
	assert.Equal(t, OnSimilarKeepBoth, got)
}

func TestStatus_TextMarshaling(t *testing.T) {
	t.Parallel()
	b, err := StatusFlagged.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "flagged", string(b))
	var s Status
	require.NoError(t, s.UnmarshalText([]byte("active")))
	assert.Equal(t, StatusActive, s)
	require.ErrorIs(t, s.UnmarshalText([]byte("x")), ErrValidation)
}
