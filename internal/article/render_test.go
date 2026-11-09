package article

import (
	"strings"
	"testing"
)

func blockNode(t *testing.T, raw string) (ContentState, []Node) {
	t.Helper()
	state, err := ParseContentState(raw)
	if err != nil {
		t.Fatal(err)
	}
	images := Images{"img1": {ID: "img1", Namespace: "zen_doc", GroupID: 42, ImageName: "pub_a_img1"}}
	var nodes []Node
	for _, b := range state.Blocks {
		nodes = append(nodes, NodeFromBlock(b, state.EntityMap, images))
	}
	return state, nodes
}

func TestRenderBodyCoversEveryMarkdownBlockKind(t *testing.T) {
	_, nodes := blockNode(t, `{"blocks":[
{"key":"a","text":"Жирный и курсив","type":"unstyled","depth":0,"inlineStyleRanges":[{"offset":0,"length":6,"style":"BOLD"},{"offset":9,"length":6,"style":"ITALIC"}],"entityRanges":[],"data":{}},
{"key":"b","text":"Заголовок","type":"header-two","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"c","text":"Подзаголовок","type":"header-three","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"d","text":"Цитата","type":"blockquote","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"e","text":"раз","type":"ordered-list-item","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"f","text":"два","type":"ordered-list-item","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"g","text":"пункт","type":"unordered-list-item","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"h","text":"","type":"unstyled","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}},
{"key":"i","text":"подпись","type":"atomic:image","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{"image":{"id":"img1"}}},
{"key":"j","text":"ссылка тут","type":"unstyled","depth":0,"inlineStyleRanges":[],"entityRanges":[{"key":0,"offset":0,"length":6}],"data":{}}
],"entityMap":{"0":{"type":"LINK","mutability":"MUTABLE","data":{"url":"https://example.com/a"}}}}`)
	want := "**Жирный** и *курсив*\n\n## Заголовок\n\n### Подзаголовок\n\n> Цитата\n\n1. раз\n2. два\n\n- пункт\n\n<!-- dzen:empty -->\n\n![](https://avatars.dzeninfra.ru/get-zen_doc/42/pub_a_img1/scale_1200 \"подпись\")\n\n[ссылка](https://example.com/a) тут\n"
	if got := RenderBody(nodes); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOffsetsAreCodePointsNotUTF16(t *testing.T) {
	_, nodes := blockNode(t, `{"blocks":[{"key":"a","text":"😀 важно дальше","type":"unstyled","depth":0,"inlineStyleRanges":[{"offset":2,"length":5,"style":"BOLD"}],"entityRanges":[],"data":{}}],"entityMap":{}}`)
	if got := RenderNode(nodes[0]); got != "😀 **важно** дальше" {
		t.Fatal(got)
	}
	parsed, diags := ParseBody("😀 **важно** дальше")
	if len(diags) != 0 || parsed[0].Styles[0] != (StyleRange{Offset: 2, Length: 5, Style: "BOLD"}) {
		t.Fatalf("%+v %v", parsed, diags)
	}
}

func TestLineStartsAndPunctuationSurviveRoundTrip(t *testing.T) {
	for _, value := range []string{"1. не список", "- не пункт", "# не заголовок", "a*b*c [x] <y> `z` & ~~q~~", "snake_case и _края_", "---", "> нет", "!важно", `\слэш`, "строка\nвторая", "a b"} {
		value := value
		t.Run(value, func(t *testing.T) {
			n := checked(Node{Kind: "unstyled", Text: value}, Node{Kind: KindRaw})
			if n.Kind != "unstyled" {
				t.Fatalf("fell back to raw: %q", RenderNode(Node{Kind: "unstyled", Text: value}))
			}
			parsed, diags := ParseBody(RenderNode(n))
			if len(diags) != 0 || len(parsed) != 1 || parsed[0].Text != value {
				t.Fatalf("got %+v %v", parsed, diags)
			}
		})
	}
}

func TestStyleEdgeWhitespaceMovesOutsideMarkers(t *testing.T) {
	n := Node{Kind: "unstyled", Text: "до жирный после", Styles: []StyleRange{{Offset: 2, Length: 8, Style: "BOLD"}}}
	if got := RenderNode(n); got != "до **жирный** после" {
		t.Fatal(got)
	}
}

func TestNestedLinkAndStyles(t *testing.T) {
	n := Node{Kind: "unstyled", Text: "текст ссылки", Styles: []StyleRange{{Offset: 0, Length: 12, Style: "BOLD"}}, Links: []Link{{Offset: 6, Length: 6, URL: "https://example.com"}}}
	if got := RenderNode(n); got != "**текст** [**ссылки**](https://example.com)" {
		t.Fatal(got)
	}
}

func TestBlocksWithoutMarkdownFormBecomeRawAnchors(t *testing.T) {
	cases := map[string]string{
		"unknown type":   `{"key":"x1","text":"","type":"atomic:poll","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{"pollId":1}}`,
		"nested list":    `{"key":"x1","text":"a","type":"unordered-list-item","depth":1,"inlineStyleRanges":[],"entityRanges":[],"data":{}}`,
		"styled caption": `{"key":"x1","text":"a","type":"atomic:image","depth":0,"inlineStyleRanges":[{"offset":0,"length":1,"style":"BOLD"}],"entityRanges":[],"data":{"image":{"id":"img1"}}}`,
		"whitespace":     `{"key":"x1","text":"  ","type":"unstyled","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}}`,
		"embed card":     `{"key":"x1","text":"","type":"atomic:embed","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{"embedData":{"type":"yandex-zen-publication","title":"t"}}}`,
	}
	for name, block := range cases {
		block := block
		t.Run(name, func(t *testing.T) {
			_, nodes := blockNode(t, `{"blocks":[`+block+`],"entityMap":{}}`)
			if nodes[0].Kind != KindRaw || nodes[0].RawKey != "x1" {
				t.Fatalf("%+v", nodes[0])
			}
			if !strings.Contains(RenderNode(nodes[0]), `dzen:raw key="x1"`) {
				t.Fatal(RenderNode(nodes[0]))
			}
		})
	}
}

func TestParseBodyFailsClosed(t *testing.T) {
	cases := map[string]string{
		"h1":            "# Заголовок\n",
		"h4":            "#### Заголовок\n",
		"table":         "| a |\n|---|\n| b |\n",
		"code fence":    "```\nx\n```\n",
		"inline code":   "текст `кода`\n",
		"rule":          "***\n",
		"raw html":      "<div>x</div>\n",
		"nested list":   "- a\n  - b\n",
		"strikethrough": "~~нет~~\n",
		"mixed image":   "текст ![](a.png)\n",
		"bad link":      "[x](ftp://a)\n",
		"bad directive": "<!-- dzen:nope -->\n",
	}
	for name, src := range cases {
		src := src
		t.Run(name, func(t *testing.T) {
			if _, diags := ParseBody(src); len(diags) == 0 {
				t.Fatalf("%q was accepted", src)
			}
		})
	}
}

func TestGalleryAcceptsTightForm(t *testing.T) {
	nodes, diags := ParseBody("<!-- dzen:gallery -->\n![](a.png \"раз\")\n![](b.png)\n<!-- /dzen:gallery -->\n")
	if len(diags) != 0 || len(nodes) != 1 || nodes[0].Kind != KindGallery || len(nodes[0].Gallery) != 2 || nodes[0].Gallery[0].Caption != "раз" {
		t.Fatalf("%+v %v", nodes, diags)
	}
}

func TestDiagnosticsReportTheAuthorsLineAfterAGallery(t *testing.T) {
	_, diags := ParseBody("<!-- dzen:gallery -->\n![](a.png)\n<!-- /dzen:gallery -->\n`код`\n")
	if len(diags) != 1 || diags[0].Line != 4 {
		t.Fatalf("%+v", diags)
	}
}

func TestParseContentStateAcceptsBothWireShapes(t *testing.T) {
	inner := `{"blocks":[{"key":"a","text":"x","type":"unstyled","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}}],"entityMap":{}}`
	wrapped, err := ParseContentState(`{"draftJsState":` + inner + `,"mentions":[]}`)
	if err != nil || len(wrapped.Blocks) != 1 {
		t.Fatalf("%+v %v", wrapped, err)
	}
	empty, err := ParseContentState("")
	if err != nil || len(empty.Blocks) != 0 {
		t.Fatalf("%+v %v", empty, err)
	}
	out, err := wrapped.Marshal()
	if err != nil || out != inner {
		t.Fatalf("flat output differs: %s %v", out, err)
	}
}
