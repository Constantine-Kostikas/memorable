package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const (
	topURI        = "memorable://top"
	memoryURIBase = "memorable://memory/"
)

func (s *Server) addResources() {
	s.mcp.AddResource(&mcp.Resource{
		URI: topURI, Name: "top", MIMEType: "text/plain",
		Description: "Pinned and most used memories for this project (the session-start set).",
	}, s.readTop)
	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: memoryURIBase + "{+slug}", Name: "memory", MIMEType: "text/plain",
		Description: "One memory by slug.",
	}, s.readMemory)
}

func (s *Server) resourceError(ctx context.Context, err error) error {
	msg, known := userMessage(err)
	if !known {
		s.logger.ErrorContext(ctx, "resource read failed", "error", err)
	}
	return errors.New(msg) //nolint:err113 // a safe, user-facing message for the client
}

func (s *Server) readTop(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	svc, err := s.backend(ctx)
	if err != nil {
		return nil, s.resourceError(ctx, err)
	}
	items, err := svc.Preload(ctx)
	if err != nil {
		return nil, s.resourceError(ctx, err)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/plain", Text: renderSummaries("memory-top", nil, items),
	}}}, nil
}

func (s *Server) readMemory(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	slug := strings.TrimPrefix(req.Params.URI, memoryURIBase)
	svc, err := s.backend(ctx)
	if err != nil {
		return nil, s.resourceError(ctx, err)
	}
	fetched, err := svc.Get(ctx, memory.GetRequest{Slugs: []string{slug}, Passive: true})
	if err != nil {
		return nil, s.resourceError(ctx, err)
	}
	if len(fetched) != 1 || fetched[0].Err != nil {
		if len(fetched) == 1 && errors.Is(fetched[0].Err, memory.ErrNotFound) {
			return nil, mcp.ResourceNotFoundError(req.Params.URI) //nolint:wrapcheck // the SDK's protocol-level error
		}
		return nil, s.resourceError(ctx, fmt.Errorf("slug %s: %w", slug, memory.ErrQuarantined))
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/plain", Text: renderMemory(fetched[0].Memory),
	}}}, nil
}
