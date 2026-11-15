package store

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

func TestAdminAuditOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		opts        []AdminOption
		wantProcess string
		wantDetails map[string]any
	}{
		{name: "no options", wantProcess: ""},
		{
			name:        "process and details",
			opts:        []AdminOption{WithAuditProcess("ui:42@2026-11-15T10:00:00Z"), WithAuditDetails(map[string]any{"via": "ui"})},
			wantProcess: "ui:42@2026-11-15T10:00:00Z",
			wantDetails: map[string]any{"via": "ui"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &Admin{}
			for _, opt := range tt.opts {
				opt(a)
			}

			got := a.audit(memory.AuditEntry{Action: "purge"})

			assert.Equal(t, "purge", got.Action)
			assert.Equal(t, tt.wantProcess, got.Process)
			assert.Equal(t, tt.wantDetails, got.Details)
		})
	}
}
