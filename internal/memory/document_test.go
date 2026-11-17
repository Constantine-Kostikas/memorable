package memory_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestDocumentText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "title: T | text: C", memory.DocumentText("T", nil, "C"))
	assert.Equal(t, "title: T | text: C", memory.DocumentText("T", []string{}, "C"))
	assert.Equal(t, "title: T | tags: ci, go | text: C", memory.DocumentText("T", []string{"ci", "go"}, "C"))
}
