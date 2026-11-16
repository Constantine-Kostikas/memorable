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
	ListURL              string
	Context              string
	Contexts             []string
	CSRF                 string
	ReviewCount          int
	ReviewCountUnknown   bool
	AdminUnlockedMinutes int
	AdminLockedMessage   string
	Page                 string
	Memories             *memoriesView
	Review               *reviewView
	Profile              *profileView
	Stats                *statsView
	Prune                *pruneView
}

const pageMemories = "memories"

func parseTemplates() (*template.Template, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{"typeClass": typeClass}).ParseFS(assetFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return tmpl, nil
}

// typeClass maps a memory type name to the suffix of its type-tab-* and chip-type-* classes in input.css.
func typeClass(typeName string) string {
	switch typeName {
	case "project", "user", "feedback", "reference":
		return typeName
	default:
		return "other"
	}
}
