package views

import (
	"bytes"
	"context"
	"io"
	"log/slog"

	"github.com/a-h/templ"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// md renders LLM output. goldmark is safe by default: raw HTML in the input
// is dropped and links with dangerous schemes (javascript:, data: and the
// like) lose their href, so model or student text cannot inject markup.
// Never add html.WithUnsafe here. Hard wraps keep the model's line breaks.
var md = goldmark.New(goldmark.WithRendererOptions(
	html.WithHardWraps(),
	renderer.WithNodeRenderers(util.Prioritized(imageAltRenderer{}, 100)),
))

// imageAltRenderer displays the description without fetching the image URL.
type imageAltRenderer struct{}

func (imageAltRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, renderImageAlt)
}

func renderImageAlt(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, err := w.Write(util.EscapeHTML(node.Text(source)))
	return ast.WalkSkipChildren, err
}

// markdown renders LLM text (feedback, follow-up questions) as HTML.
// Student answers are not Markdown and stay plain text.
func markdown(source string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		var buf bytes.Buffer
		if err := md.Convert([]byte(source), &buf); err != nil {
			slog.Warn("markdown render failed, showing plain text", "error", err)
			_, err := io.WriteString(w, templ.EscapeString(source))
			return err
		}
		_, err := w.Write(buf.Bytes())
		return err
	})
}
