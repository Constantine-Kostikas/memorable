package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

// userMessage maps an error to text that is safe to show Claude; ok is false for unexpected errors.
func userMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, memory.ErrValidation):
		return err.Error(), true
	case errors.Is(err, memory.ErrSecretDetected):
		return "rejected: " + err.Error() + "; never store secrets in memory", true
	case errors.Is(err, memory.ErrNotFound):
		return "memory not found", true
	case errors.Is(err, memory.ErrConflict):
		return "memory changed since you read it; fetch it again", true
	case errors.Is(err, memory.ErrQuarantined):
		return "memory is quarantined pending human review", true
	case errors.Is(err, memory.ErrNoContext):
		return "no memory context for this directory; run `memorable context add <path> <name>`", true
	case errors.Is(err, memory.ErrReadOnly):
		msg := "this directory maps to a read-only context; run `memorable context add <path> <name>`"
		if strings.Contains(err.Error(), "profile") {
			msg = "profile memories are read-only here; the user manages them with `memorable profile`"
		}
		return msg, true
	case errors.Is(err, memory.ErrAuthFailed):
		return "the database rejected the app password, most likely because it was rotated after this server started; " +
			"ask the user to restart Claude Code so the server reloads it", true
	case errors.Is(err, memory.ErrUnavailable):
		return "memory store unavailable; ask the user to run `memorable doctor`", true
	case errors.Is(err, memory.ErrRateLimited):
		return "write rate limit reached; try again in a minute", true
	}
	return "internal error", false
}

func (s *Server) toolError(ctx context.Context, tool string, err error) *mcp.CallToolResult {
	msg, known := userMessage(err)
	if !known {
		s.logger.ErrorContext(ctx, "tool failed", "tool", tool, "error", err)
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}
