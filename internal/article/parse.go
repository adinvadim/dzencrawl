package article

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Diagnostic is a reason a file cannot be sent, with its 1-based line.
type Diagnostic struct {
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (d Diagnostic) Error() string {
	return fmt.Sprintf("line %d: %s: %s", d.Line, d.Code, d.Message)
}

var (
	md            = goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.Table))
	directiveLine = regexp.MustCompile(`^\s*<!--\s*/?dzen:[^>]*-->\s*$`)
	directiveBody = regexp.MustCompile(`^<!--\s*(/?dzen:[a-z]+)((?:\s+[a-z_]+="[^"]*")*)\s*-->$`)
	directiveAttr = regexp.MustCompile(`([a-z_]+)="([^"]*)"`)
)

type parser struct {
	src     []byte
	nodes   []Node
	diags   []Diagnostic
	gallery *Node
}

// ParseBody parses canonical or hand-written article Markdown.
func ParseBody(src string) ([]Node, []Diagnostic) {
	source := isolateDirectives([]byte(src))
	p := &parser{src: source}
	root := md.Parser().Parse(text.NewReader(source))
	for child := root.FirstChild(); child != nil; child = child.NextSibling() {
		p.block(child)
	}
	if p.gallery != nil {
		p.fail(p.gallery.Line, "markdown_unsupported", "unterminated dzen:gallery")
	}
	return p.nodes, p.diags
}

// isolateDirectives puts blank lines around directive comments so that images
// on the following line are not swallowed into the comment's HTML block.
func isolateDirectives(src []byte) []byte {
	lines := bytes.Split(src, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for i, line := range lines {
		if directiveLine.Match(line) {
			out = append(out, line)
			if i+1 < len(lines) && len(bytes.TrimSpace(lines[i+1])) != 0 {
				out = append(out, nil)
			}
			continue
		}
		out = append(out, line)
		if i+1 < len(lines) && directiveLine.Match(lines[i+1]) && len(bytes.TrimSpace(line)) != 0 {
			out = append(out, nil)
		}
	}
	return bytes.Join(out, []byte("\n"))
}

func (p *parser) fail(line int, code, format string, args ...any) {
	p.diags = append(p.diags, Diagnostic{Line: line, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (p *parser) lineAt(offset int) int { return bytes.Count(p.src[:min(offset, len(p.src))], []byte("\n")) + 1 }

func (p *parser) lineOf(n ast.Node) int {
	for current := n; current != nil; current = current.FirstChild() {
		if current.Type() != ast.TypeInline && current.Lines().Len() > 0 {
			return p.lineAt(current.Lines().At(0).Start)
		}
		if t, ok := current.(*ast.Text); ok {
			return p.lineAt(t.Segment.Start)
		}
	}
	return 1
}

func (p *parser) add(n Node) {
	if p.gallery != nil {
		p.fail(n.Line, "markdown_unsupported", "only images are allowed inside dzen:gallery")
		return
	}
	p.nodes = append(p.nodes, n)
}

func (p *parser) block(n ast.Node) {
	line := p.lineOf(n)
	switch v := n.(type) {
	case *ast.Heading:
		switch v.Level {
		case 2:
			p.textBlock("header-two", v, line)
		case 3:
			p.textBlock("header-three", v, line)
		default:
			p.fail(line, "markdown_unsupported", "H%d is not supported by Dzen; use ## or ###", v.Level)
		}
	case *ast.Paragraph, *ast.TextBlock:
		p.paragraph(n, line)
	case *ast.Blockquote:
		for child := v.FirstChild(); child != nil; child = child.NextSibling() {
			if _, ok := child.(*ast.Paragraph); !ok {
				p.fail(p.lineOf(child), "markdown_unsupported", "a quote may contain only paragraphs")
				continue
			}
			p.textBlock("blockquote", child, p.lineOf(child))
		}
	case *ast.List:
		kind := "unordered-list-item"
		if v.IsOrdered() {
			kind = "ordered-list-item"
		}
		for item := v.FirstChild(); item != nil; item = item.NextSibling() {
			first := item.FirstChild()
			if first == nil || first.NextSibling() != nil {
				p.fail(p.lineOf(item), "markdown_unsupported", "a list item must be one paragraph; Dzen rejects nested lists")
				continue
			}
			p.textBlock(kind, first, p.lineOf(first))
		}
	case *ast.HTMLBlock:
		p.directive(v, line)
	case *ast.CodeBlock, *ast.FencedCodeBlock:
		p.fail(line, "markdown_unsupported", "code blocks are not supported by Dzen")
	case *ast.ThematicBreak:
		p.fail(line, "markdown_unsupported", "horizontal rules are not supported by Dzen")
	default:
		p.fail(line, "markdown_unsupported", "unsupported construct %s", n.Kind())
	}
}

func (p *parser) paragraph(n ast.Node, line int) {
	var images []*ast.Image
	only := true
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Image:
			images = append(images, v)
		case *ast.Text:
			if len(bytes.TrimSpace(v.Segment.Value(p.src))) != 0 {
				only = false
			}
		default:
			only = false
		}
	}
	if len(images) > 0 && only {
		if p.gallery != nil {
			for _, img := range images {
				caption, _ := p.imageCaption(img, line)
				p.gallery.Gallery = append(p.gallery.Gallery, GalleryItem{Src: unescape(img.Destination), Caption: caption})
			}
			return
		}
		if len(images) != 1 {
			p.fail(line, "markdown_unsupported", "put each image in its own paragraph, or use dzen:gallery")
			return
		}
		caption, _ := p.imageCaption(images[0], line)
		p.add(Node{Kind: KindImage, Src: unescape(images[0].Destination), Caption: caption, Line: line})
		return
	}
	p.textBlock("unstyled", n, line)
}

func (p *parser) imageCaption(img *ast.Image, line int) (string, bool) {
	if len(img.Title) == 0 {
		return "", true
	}
	return unescape(img.Title), true
}

func (p *parser) directive(n *ast.HTMLBlock, line int) {
	var raw bytes.Buffer
	for i := 0; i < n.Lines().Len(); i++ {
		segment := n.Lines().At(i)
		raw.Write(segment.Value(p.src))
	}
	if n.HasClosure() {
		raw.Write(n.ClosureLine.Value(p.src))
	}
	matches := directiveBody.FindStringSubmatch(strings.TrimSpace(raw.String()))
	if matches == nil {
		p.fail(line, "markdown_unsupported", "raw HTML is not supported by Dzen")
		return
	}
	attrs := map[string]string{}
	for _, pair := range directiveAttr.FindAllStringSubmatch(matches[2], -1) {
		attrs[pair[1]] = html.UnescapeString(pair[2])
	}
	switch matches[1] {
	case "dzen:empty":
		p.add(Node{Kind: KindEmpty, Line: line})
	case "dzen:gallery":
		if p.gallery != nil {
			p.fail(line, "markdown_unsupported", "nested dzen:gallery")
			return
		}
		p.gallery = &Node{Kind: KindGallery, Line: line}
	case "/dzen:gallery":
		if p.gallery == nil {
			p.fail(line, "markdown_unsupported", "dzen:gallery was not opened")
			return
		}
		gallery := *p.gallery
		p.gallery = nil
		if len(gallery.Gallery) == 0 {
			p.fail(line, "markdown_unsupported", "empty dzen:gallery")
			return
		}
		p.add(gallery)
	case "dzen:embed":
		if embedType(attrs["url"]) == "" {
			p.fail(line, "markdown_unsupported", "embed url must be YouTube or Instagram")
			return
		}
		p.add(Node{Kind: KindEmbed, Embed: attrs["url"], Line: line})
	case "dzen:raw":
		if attrs["key"] == "" {
			p.fail(line, "markdown_unsupported", "dzen:raw needs a key")
			return
		}
		p.add(Node{Kind: KindRaw, RawKey: attrs["key"], RawType: attrs["type"], Line: line})
	default:
		p.fail(line, "markdown_unsupported", "unknown directive %s", matches[1])
	}
}

func embedType(url string) string {
	switch {
	case strings.Contains(url, "youtube.com/") || strings.Contains(url, "youtu.be/"):
		return "youtube"
	case strings.Contains(url, "instagram.com/"):
		return "instagram"
	}
	return ""
}

type inlineState struct {
	p      *parser
	line   int
	text   []rune
	styles []StyleRange
	links  []Link
	ok     bool
}

func (p *parser) textBlock(kind string, n ast.Node, line int) {
	s := &inlineState{p: p, line: line, ok: true}
	s.walk(n)
	if !s.ok {
		return
	}
	value := string(s.text)
	if strings.TrimSpace(value) == "" {
		p.fail(line, "markdown_unsupported", "empty block; use <!-- dzen:empty --> for a blank paragraph")
		return
	}
	p.add(Node{Kind: kind, Text: value, Styles: s.styles, Links: s.links, Line: line})
}

func (s *inlineState) walk(n ast.Node) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Text:
			s.text = append(s.text, []rune(unescape(v.Segment.Value(s.p.src)))...)
			if v.HardLineBreak() {
				s.text = append(s.text, '\n')
			} else if v.SoftLineBreak() {
				s.text = append(s.text, ' ')
			}
		case *ast.String:
			s.text = append(s.text, []rune(string(v.Value))...)
		case *ast.Emphasis:
			style := "ITALIC"
			if v.Level == 2 {
				style = "BOLD"
			}
			start := len(s.text)
			s.walk(v)
			s.styles = append(s.styles, StyleRange{Offset: start, Length: len(s.text) - start, Style: style})
		case *ast.Link:
			s.addLink(v, unescape(v.Destination))
		case *ast.AutoLink:
			url := string(v.URL(s.p.src))
			start := len(s.text)
			s.text = append(s.text, []rune(url)...)
			s.checkLink(url, start, len(s.text)-start)
		case *ast.CodeSpan:
			s.reject("inline code is not supported by Dzen")
		case *ast.Image:
			s.reject("an image must be alone in its paragraph")
		case *ast.RawHTML:
			s.reject("raw HTML is not supported by Dzen")
		case *east.Strikethrough:
			s.reject("strikethrough is rejected by Dzen")
		default:
			s.reject("unsupported inline " + child.Kind().String())
		}
	}
}

func (s *inlineState) addLink(n *ast.Link, url string) {
	start := len(s.text)
	s.walk(n)
	s.checkLink(url, start, len(s.text)-start)
}

func (s *inlineState) checkLink(url string, start, length int) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		s.reject("link must be http or https: " + url)
		return
	}
	s.links = append(s.links, Link{Offset: start, Length: length, URL: url})
}

func (s *inlineState) reject(message string) {
	s.ok = false
	s.p.fail(s.line, "markdown_unsupported", "%s", message)
}

var entityRef = regexp.MustCompile(`^&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[a-zA-Z][a-zA-Z0-9]{1,31});`)

// unescape resolves backslash escapes and entity references, as a CommonMark
// renderer would.
func unescape(value []byte) string {
	var out strings.Builder
	for i := 0; i < len(value); {
		c := value[i]
		if c == '\\' && i+1 < len(value) && isPunct(value[i+1]) {
			out.WriteByte(value[i+1])
			i += 2
			continue
		}
		if c == '&' {
			if match := entityRef.Find(value[i:]); match != nil {
				if resolved := html.UnescapeString(string(match)); resolved != string(match) {
					out.WriteString(resolved)
					i += len(match)
					continue
				}
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func isPunct(c byte) bool {
	return c >= '!' && c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}
