// Package webui serves the memorable web interface: embedded templates, static assets, and handlers.
package webui

import (
	"embed"
	"fmt"
	"html/template"
)

//go:embed templates static
var assetFS embed.FS

type pageData struct {
	Title                string
	Message              string
	Context              string
	Contexts             []string
	CSRF                 string
	ReviewCount          int
	ReviewCountUnknown   bool
	AdminUnlockedMinutes int
	Page                 string
	Memories             *memoriesView
	Detail               *detailView
	Edit                 *editView
	Review               *reviewView
	Profile              *profileView
	Stats                *statsView
}

const pageMemories = "memories"

func parseTemplates() (*template.Template, error) {
	tmpl, err := template.ParseFS(assetFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return tmpl, nil
}
