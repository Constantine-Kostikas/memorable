package webui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/constantine-kostikas/memorable/internal/memory"
)

const externalLinkRel = "noopener noreferrer nofollow"

// markdown renders GitHub Flavored Markdown with goldmark's safe defaults, so links or images with a
// javascript:, vbscript:, or non-image data: URL lose their destination. The parser has no raw HTML
// parsers, so HTML in the source stays literal text and renders escaped; goldmark's safe mode would
// drop it instead, losing placeholders like <name> and types like Vec<String> that agents write
// outside backticks. Single newlines stay line breaks: most memories are plain notes.
var markdown = goldmark.New(
	goldmark.WithParser(parser.NewParser(
		parser.WithBlockParsers(without(parser.DefaultBlockParsers(), parser.NewHTMLBlockParser())...),
		parser.WithInlineParsers(without(parser.DefaultInlineParsers(), parser.NewRawHTMLParser())...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
		parser.WithASTTransformers(util.Prioritized(linkRelTransformer{}, 100)),
	)),
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// without drops the parser of the same concrete type as unwanted from goldmark's defaults.
func without(parsers []util.PrioritizedValue, unwanted any) []util.PrioritizedValue {
	return slices.DeleteFunc(parsers, func(p util.PrioritizedValue) bool {
		return reflect.TypeOf(p.Value) == reflect.TypeOf(unwanted)
	})
}

// linkRelTransformer marks every link as untrusted, since memory content is written by agents.
type linkRelTransformer struct{}

func (linkRelTransformer) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindLink, ast.KindAutoLink:
			n.SetAttributeString("rel", []byte(externalLinkRel))
		}
		return ast.WalkContinue, nil
	})
}

// renderMarkdown is the only place memory content becomes markup. A conversion failure falls back to
// the escaped source, so content is never lost from view.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(src) + "</pre>") //nolint:gosec // G203: the source is escaped.
	}
	return template.HTML(buf.String()) //nolint:gosec // G203: goldmark without WithUnsafe omits raw HTML and drops dangerous URLs; see TestRenderMarkdownIsInert.
}

type previewView struct {
	HTML  template.HTML
	Empty bool
}

// handlePreview renders the editor's unsaved content. It reads nothing from the store and writes nothing.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request, rc requestContext, _ uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		s.failResponse(w, r, rc, fmt.Errorf("%w: malformed form", memory.ErrValidation))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderStatus(w, http.StatusOK, "markdown_preview", markdownPreview(r.PostFormValue("content")))
}

func markdownPreview(content string) previewView {
	return previewView{HTML: renderMarkdown(content), Empty: strings.TrimSpace(content) == ""}
}
