package memory

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func validNew() NewMemory {
	return NewMemory{
		Title:   "Database choice",
		Content: "We use Postgres with pgvector.",
		Type:    TypeProject,
		Tags:    []string{"db", "postgres"},
		Source:  SourceUserStated,
	}
}

func TestNewMemory_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*NewMemory)
		wantErr bool
	}{
		{name: "valid", mutate: func(*NewMemory) {}},
		{name: "empty title", mutate: func(n *NewMemory) { n.Title = "  " }, wantErr: true},
		{name: "title 121 runes", mutate: func(n *NewMemory) { n.Title = strings.Repeat("a", 121) }, wantErr: true},
		{name: "title 120 runes", mutate: func(n *NewMemory) { n.Title = strings.Repeat("a", 120) }},
		{name: "empty content", mutate: func(n *NewMemory) { n.Content = "" }, wantErr: true},
		{name: "greek content 2000 runes", mutate: func(n *NewMemory) { n.Content = strings.Repeat("α", 2000) }},
		{name: "content 2001 runes", mutate: func(n *NewMemory) { n.Content = strings.Repeat("α", 2001) }, wantErr: true},
		{name: "11 tags", mutate: func(n *NewMemory) {
			n.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}
		}, wantErr: true},
		{name: "bad tag", mutate: func(n *NewMemory) { n.Tags = []string{"Bad_Tag"} }, wantErr: true},
		{name: "duplicate tags", mutate: func(n *NewMemory) { n.Tags = []string{"db", "db"} }, wantErr: true},
		{name: "bad slug", mutate: func(n *NewMemory) { n.Slug = ptr("-x") }, wantErr: true},
		{name: "good slug", mutate: func(n *NewMemory) { n.Slug = ptr("memorable/db-choice") }},
		{name: "unknown type", mutate: func(n *NewMemory) { n.Type = TypeUnknown }, wantErr: true},
		{name: "unknown source", mutate: func(n *NewMemory) { n.Source = SourceUnknown }, wantErr: true},
		{name: "invalid utf8", mutate: func(n *NewMemory) { n.Content = "a\xffb" }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			n := validNew()
			tt.mutate(&n)
			err := n.Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrValidation)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPatch_Validate(t *testing.T) {
	t.Parallel()
	require.NoError(t, Patch{Title: ptr("new title")}.Validate())
	require.ErrorIs(t, Patch{}.Validate(), ErrValidation)
	require.ErrorIs(t, Patch{Content: ptr(strings.Repeat("x", 2001))}.Validate(), ErrValidation)
	require.ErrorIs(t, Patch{Tags: &[]string{"UPPER"}}.Validate(), ErrValidation)
	require.ErrorIs(t, Patch{Type: ptr(TypeUnknown)}.Validate(), ErrValidation)
}

func TestContentHash(t *testing.T) {
	t.Parallel()
	a := ContentHash("Title", "Some   content\n here")
	b := ContentHash("  title ", "some content here")
	c := ContentHash("Title", "other content")
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Len(t, a, 32)
}

func TestSnippet(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "short text", Snippet("short text", 200))

	long := strings.Repeat("word ", 100)
	got := Snippet(long, 50)
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.LessOrEqual(t, utf8.RuneCountInString(got), 51)
	assert.False(t, strings.HasSuffix(strings.TrimSuffix(got, "…"), " "))

	greek := strings.Repeat("αβγ", 100)
	g := Snippet(greek, 10)
	assert.True(t, utf8.ValidString(g))
	assert.Equal(t, 11, utf8.RuneCountInString(g))
}

func FuzzNewMemoryValidate(f *testing.F) {
	f.Add("title", "content", "tag", "slug")
	f.Add("", "\xff", "BAD", "-")
	f.Fuzz(func(_ *testing.T, title, content, tag, slug string) {
		n := NewMemory{Title: title, Content: content, Type: TypeUser, Source: SourceUserStated, Tags: []string{tag}, Slug: &slug}
		_ = n.Validate()
	})
}

func FuzzSnippet(f *testing.F) {
	f.Add("hello world", 5)
	f.Add("αβγ δεζ", 3)
	f.Fuzz(func(t *testing.T, s string, n int) {
		if n < 1 || n > 500 || !utf8.ValidString(s) {
			return
		}
		got := Snippet(s, n)
		if utf8.RuneCountInString(got) > n+1 {
			t.Fatalf("snippet too long: %d > %d", utf8.RuneCountInString(got), n+1)
		}
		if !utf8.ValidString(got) {
			t.Fatal("invalid utf8")
		}
	})
}

func TestNormalizeTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil stays nil", nil, nil},
		{"lowercase and trim", []string{" Go ", "CI"}, []string{"go", "ci"}},
		{"spaces and underscores", []string{"web ui", "web_ui", "Web  UI"}, []string{"web-ui"}},
		{"drops empty", []string{"", "  "}, []string{}},
		{"keeps first-seen order", []string{"b", "a", "B"}, []string{"b", "a"}},
		{"invalid characters survive for validation", []string{"C++"}, []string{"c++"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, NormalizeTags(tt.in))
		})
	}
}
