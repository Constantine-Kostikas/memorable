package transfer

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func FuzzParseClaudeMemoryFile(f *testing.F) {
	f.Add("---\nname: style\ndescription: Spelling\nmetadata:\n  type: user\n---\nUse US spelling.\n", "style.md")
	f.Add("---\ntags: [a, b,, c]\npinned: true\nscope: global\n---\n", "x.md")
	f.Add("---\nname: "+strings.Repeat("é", 200)+"\n---\nbody", "../weird name.md")
	f.Add("---\n", "")
	f.Fuzz(func(t *testing.T, content, fileName string) {
		n, err := ParseClaudeMemoryFile(strings.NewReader(content), fileName)
		if err != nil {
			return
		}
		if utf8.ValidString(n.Title) && utf8.RuneCountInString(n.Title) > memory.MaxTitleRunes {
			t.Fatalf("title has %d runes", utf8.RuneCountInString(n.Title))
		}
		if strings.ContainsAny(n.Title, "\r\n") {
			t.Fatalf("title spans lines: %q", n.Title)
		}
		if n.Source != memory.SourceImported {
			t.Fatalf("source %v", n.Source)
		}
	})
}

func FuzzParseJSON(f *testing.F) {
	f.Add(`{"format":"memorable-export","version":1,"memories":[{"type":"user","title":"t","content":"c"}]}`)
	f.Add(`{"memories":null}`)
	f.Add(`[`)
	f.Fuzz(func(t *testing.T, content string) {
		items, err := ParseJSON(strings.NewReader(content))
		if err != nil {
			return
		}
		for _, n := range items {
			if n.Source != memory.SourceImported {
				t.Fatalf("source %v", n.Source)
			}
		}
	})
}
