package scan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rules(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

func TestSecretScanner(t *testing.T) {
	t.Parallel()
	s, err := NewSecretScanner()
	require.NoError(t, err)

	githubToken := "ghp_" + "9Xk2mQ7vR4tL8wN3pZ6yB1cD5fG0hJ2sK4aE"
	require.Len(t, githubToken, 40)

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "aws access key", text: "the deploy key is AKIAZ5MDKS7G3XWRL2QP for prod", want: "aws-access-token"},
		{name: "github pat", text: "export GH_TOKEN=" + githubToken, want: "github-pat"},
		{name: "postgres url with password", text: "connect with postgres://memorable_app:s3cr3tPassw0rd@127.0.0.1:5434/memorable", want: "url-credentials"},
		{name: "random base64 token", text: "the value q8Xz3LmP9vR2tY7wB4nK6sD1fH5jG0cA8eQ3uI9o was rotated", want: "high-entropy-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, rules(s.Scan(tt.text)), tt.want)
		})
	}

	negatives := []string{
		"memory id 0193a5b2-7c4d-7e8f-9a0b-1c2d3e4f5a6b was updated",
		"fixed in commit 4b38cbd2f1e0a9c8b7d6e5f4a3b2c1d0e9f8a7b6",
		"We use Postgres with pgvector because the user prefers SQL over ORMs.",
		"The api key is stored in the OS keyring, never in config files.",
		"Run TestKey_OrderMatchesEffectiveScore after changing the ranking.",
		"connect to postgres://127.0.0.1:5434/memorable as the app role",
	}
	for _, text := range negatives {
		assert.Empty(t, s.Scan(text), text)
	}
}

func FuzzSecretScan(f *testing.F) {
	s, err := NewSecretScanner()
	if err != nil {
		f.Fatal(err)
	}
	f.Add("password = hunter2hunter2")
	f.Add("AKIAZ5MDKS7G3XWRL2QP")
	f.Fuzz(func(t *testing.T, text string) {
		a, b := s.Scan(text), s.Scan(text)
		if len(a) != len(b) {
			t.Fatal("non-deterministic")
		}
	})
}
