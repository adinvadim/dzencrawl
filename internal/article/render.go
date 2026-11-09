package article

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Node kinds. Text kinds carry Draft.js block types directly.
const (
	KindEmpty   = "empty"
	KindImage   = "image"
	KindGallery = "gallery"
	KindEmbed   = "embed"
	KindRaw     = "raw"
)

// Link is a LINK entity over a text range, in code points.
type Link struct {
	Offset int
	Length int
	URL    string
}

// GalleryItem is one image of a gallery.
type GalleryItem struct {
	Src     string
	Caption string
}

// Node is the shared representation of a block in both directions.
type Node struct {
	Kind    string
	Text    string
	Styles  []StyleRange
	Links   []Link
	Src     string
	Caption string
	Gallery []GalleryItem
	Embed   string
	RawKey  string
	RawType string
	Line    int
}

// Images resolves Dzen image ids to their directory entries.
type Images map[string]ImageInfo

// NodeFromBlock converts a Dzen block. Anything without a lossless Markdown
// form becomes a raw anchor; the block itself stays in the snapshot.
func NodeFromBlock(b Block, entities map[string]Entity, images Images) Node {
	raw := Node{Kind: KindRaw, RawKey: b.Key, RawType: b.Type}
	if b.Depth != 0 {
		return raw
	}
	switch b.Type {
	case "unstyled", "header-two", "header-three", "blockquote", "unordered-list-item", "ordered-list-item":
		if b.Type == "unstyled" && b.Text == "" && len(b.Styles) == 0 && len(b.Entities) == 0 && len(b.Data) == 0 {
			return Node{Kind: KindEmpty}
		}
		if strings.TrimSpace(b.Text) == "" || len(b.Data) != 0 {
			return raw
		}
		n := Node{Kind: b.Type, Text: b.Text}
		for _, s := range b.Styles {
			if s.Style == "BOLD" || s.Style == "ITALIC" {
				n.Styles = append(n.Styles, s)
			}
		}
		for _, r := range b.Entities {
			e, ok := entities[strconv.Itoa(r.Key)]
			if !ok || e.Type != "LINK" {
				return raw
			}
			url, _ := e.Data["url"].(string)
			if url == "" {
				return raw
			}
			n.Links = append(n.Links, Link{Offset: r.Offset, Length: r.Length, URL: url})
		}
		return checked(n, raw)
	case "atomic:image":
		if len(b.Styles) != 0 || len(b.Entities) != 0 {
			return raw
		}
		return checked(imageNode(b, images, raw), raw)
	case "atomic:embed":
		embed, _ := b.Data["embedData"].(map[string]any)
		url, _ := embed["postUrl"].(string)
		kind, _ := embed["type"].(string)
		if len(embed) != 2 || url == "" || embedType(url) != kind || b.Text != "" {
			return raw
		}
		return checked(Node{Kind: KindEmbed, Embed: url}, raw)
	}
	return raw
}

func imageNode(b Block, images Images, raw Node) Node {
	url := func(id string) string {
		info, ok := images[id]
		if !ok || info.GroupID == 0 || info.ImageName == "" {
			return ""
		}
		return info.URL()
	}
	if list, ok := b.Data["images"].([]any); ok && len(list) > 0 {
		contents, _ := b.Data["contents"].(map[string]any)
		n := Node{Kind: KindGallery}
		if b.Text != "" {
			return raw
		}
		for _, item := range list {
			entry, _ := item.(map[string]any)
			id, _ := entry["id"].(string)
			src := url(id)
			if src == "" {
				return raw
			}
			caption, _ := contents[id].(string)
			n.Gallery = append(n.Gallery, GalleryItem{Src: src, Caption: caption})
		}
		return n
	}
	image, _ := b.Data["image"].(map[string]any)
	id, _ := image["id"].(string)
	src := url(id)
	if src == "" || len(b.Data) != 1 {
		return raw
	}
	return Node{Kind: KindImage, Src: src, Caption: b.Text}
}

// checked keeps a node only when its Markdown parses back to the same content.
func checked(n, raw Node) Node {
	nodes, diags := ParseBody(RenderNode(n))
	if len(diags) != 0 || len(nodes) != 1 || RenderNode(nodes[0]) != RenderNode(n) {
		return raw
	}
	return n
}

// RenderNode returns the canonical Markdown of one block. List numbers are
// not included so that a block's identity does not depend on its position.
func RenderNode(n Node) string {
	switch n.Kind {
	case KindEmpty:
		return "<!-- dzen:empty -->"
	case KindRaw:
		return fmt.Sprintf(`<!-- dzen:raw key=%q type=%q -->`, n.RawKey, n.RawType)
	case KindEmbed:
		return fmt.Sprintf(`<!-- dzen:embed url=%q -->`, n.Embed)
	case KindImage:
		return renderImage(n.Src, n.Caption)
	case KindGallery:
		var b strings.Builder
		b.WriteString("<!-- dzen:gallery -->\n\n")
		for _, item := range n.Gallery {
			b.WriteString(renderImage(item.Src, item.Caption) + "\n\n")
		}
		b.WriteString("<!-- /dzen:gallery -->")
		return b.String()
	}
	body := renderInline(n)
	switch n.Kind {
	case "header-two":
		return "## " + strings.ReplaceAll(body, "\\\n", " ")
	case "header-three":
		return "### " + strings.ReplaceAll(body, "\\\n", " ")
	case "blockquote":
		return "> " + strings.ReplaceAll(body, "\n", "\n> ")
	case "unordered-list-item":
		return "- " + strings.ReplaceAll(body, "\n", "\n  ")
	case "ordered-list-item":
		return "1. " + strings.ReplaceAll(body, "\n", "\n   ")
	}
	return body
}

// RenderBody joins blocks into the canonical article body.
func RenderBody(nodes []Node) string {
	var b strings.Builder
	number := 0
	for i, n := range nodes {
		text := RenderNode(n)
		if n.Kind == "ordered-list-item" {
			number++
			text = strconv.Itoa(number) + text[1:]
		} else {
			number = 0
		}
		if i > 0 {
			prev := nodes[i-1].Kind
			switch {
			case prev == n.Kind && (n.Kind == "unordered-list-item" || n.Kind == "ordered-list-item"):
				b.WriteString("\n")
			case prev == n.Kind && n.Kind == "blockquote":
				b.WriteString("\n>\n")
			default:
				b.WriteString("\n\n")
			}
		}
		b.WriteString(text)
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	return b.String()
}

func renderImage(src, caption string) string {
	dest := src
	if strings.ContainsAny(src, " ()<>") {
		dest = "<" + src + ">"
	}
	if caption == "" {
		return "![](" + dest + ")"
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(caption)
	return "![](" + dest + ` "` + escaped + `")`
}

type attr struct {
	link         int
	bold, italic bool
}

func renderInline(n Node) string {
	text := []rune(n.Text)
	attrs := make([]attr, len(text))
	for i := range attrs {
		attrs[i].link = -1
	}
	apply := func(offset, length int, set func(*attr)) {
		for i := max(offset, 0); i < offset+length && i < len(attrs); i++ {
			set(&attrs[i])
		}
	}
	for _, s := range n.Styles {
		style := s.Style
		apply(s.Offset, s.Length, func(a *attr) {
			if style == "BOLD" {
				a.bold = true
			} else {
				a.italic = true
			}
		})
	}
	for i, l := range n.Links {
		index := i
		apply(l.Offset, l.Length, func(a *attr) { a.link = index })
	}
	var out strings.Builder
	spans(len(text), func(i, j int) bool { return attrs[i].link == attrs[j].link }, func(start, end int) {
		if link := attrs[start].link; link >= 0 {
			out.WriteString(wrap(text, start, end, "[", "]("+linkDest(n.Links[link].URL)+")", func(from, to int) string {
				return renderBold(text, attrs, from, to)
			}))
			return
		}
		out.WriteString(renderBold(text, attrs, start, end))
	})
	return escapeLineStarts(trimLines(out.String()))
}

func renderBold(text []rune, attrs []attr, from, to int) string {
	var out strings.Builder
	spans(to-from, func(i, j int) bool { return attrs[from+i].bold == attrs[from+j].bold }, func(start, end int) {
		start, end = start+from, end+from
		if attrs[start].bold {
			out.WriteString(wrap(text, start, end, "**", "**", func(a, b int) string { return renderItalic(text, attrs, a, b) }))
			return
		}
		out.WriteString(renderItalic(text, attrs, start, end))
	})
	return out.String()
}

func renderItalic(text []rune, attrs []attr, from, to int) string {
	var out strings.Builder
	spans(to-from, func(i, j int) bool { return attrs[from+i].italic == attrs[from+j].italic }, func(start, end int) {
		start, end = start+from, end+from
		if attrs[start].italic {
			out.WriteString(wrap(text, start, end, "*", "*", func(a, b int) string { return escapeText(text, a, b) }))
			return
		}
		out.WriteString(escapeText(text, start, end))
	})
	return out.String()
}

func spans(n int, same func(i, j int) bool, emit func(start, end int)) {
	for start := 0; start < n; {
		end := start + 1
		for end < n && same(start, end) {
			end++
		}
		emit(start, end)
		start = end
	}
}

// wrap moves whitespace outside the markers, where CommonMark requires it.
func wrap(text []rune, start, end int, open, close string, inner func(from, to int) string) string {
	from, to := start, end
	for from < to && unicode.IsSpace(text[from]) {
		from++
	}
	for to > from && unicode.IsSpace(text[to-1]) {
		to--
	}
	if from == to {
		return escapeText(text, start, end)
	}
	return escapeText(text, start, from) + open + inner(from, to) + close + escapeText(text, to, end)
}

func linkDest(url string) string {
	if strings.ContainsAny(url, " <>") || strings.Count(url, "(") != strings.Count(url, ")") {
		return "<" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(url) + ">"
	}
	return url
}

func escapeText(text []rune, from, to int) string {
	var out strings.Builder
	for i := from; i < to; i++ {
		r := text[i]
		switch r {
		case '\\', '*', '[', ']', '<', '>', '`', '&', '~', '!':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '_':
			if i == 0 || i+1 >= len(text) || !isWord(text[i-1]) || !isWord(text[i+1]) {
				out.WriteByte('\\')
			}
			out.WriteRune(r)
		case '\n':
			out.WriteString("\\\n")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// trimLines strips ASCII spaces at line edges, which Markdown cannot carry.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		hard := strings.HasSuffix(line, `\`) && i+1 < len(lines)
		if hard {
			line = strings.TrimSuffix(line, `\`)
		}
		line = strings.Trim(line, " \t")
		if hard {
			line += `\`
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

var (
	orderedStart = regexp.MustCompile(`^(\d{1,9})([.)])(\s|$)`)
	blockStart   = regexp.MustCompile(`^(#{1,6}|[-+])(\s|$)`)
	ruleLine     = regexp.MustCompile(`^(-[-\s]*|=+\s*)$`)
)

func escapeLineStarts(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		switch {
		case orderedStart.MatchString(line):
			lines[i] = orderedStart.ReplaceAllString(line, `$1\$2$3`)
		case blockStart.MatchString(line), ruleLine.MatchString(line):
			lines[i] = `\` + line
		}
	}
	return strings.Join(lines, "\n")
}
