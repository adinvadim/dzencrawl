package article

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const pulledState = `{"blocks":[
{"key":"para1","text":"Первый абзац","type":"unstyled","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{},"customServerField":"keep"},
{"key":"poll1","text":"","type":"atomic:poll","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{"pollId":7}},
{"key":"link1","text":"читать здесь","type":"unstyled","depth":0,"inlineStyleRanges":[{"offset":0,"length":6,"style":"UNDERLINE"}],"entityRanges":[{"key":3,"offset":7,"length":5}],"data":{}},
{"key":"mark1","text":"","type":"marker","depth":0,"inlineStyleRanges":[],"entityRanges":[],"data":{}}
],"entityMap":{"3":{"type":"LINK","mutability":"MUTABLE","data":{"url":"https://example.com","badSeoLink":true}}}}`

func snapshot() *Snapshot {
	return &Snapshot{ContentState: pulledState, Images: Images{}, Uploads: map[string]string{}}
}

func pullBody(t *testing.T, s *Snapshot) string {
	t.Helper()
	state, err := ParseContentState(s.ContentState)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []Node
	for _, b := range state.Blocks {
		if b.Type != "marker" {
			nodes = append(nodes, NodeFromBlock(b, state.EntityMap, s.Images))
		}
	}
	return RenderBody(nodes)
}

func parsed(t *testing.T, body string) []Node {
	t.Helper()
	nodes, diags := ParseBody(body)
	if len(diags) != 0 {
		t.Fatalf("%v", diags)
	}
	return nodes
}

func noImages(string) (string, error) { return "", errors.New("unexpected image") }

func TestUntouchedPullIsANoOpThatPreservesEverythingVerbatim(t *testing.T) {
	s := snapshot()
	plan, diags := Build(parsed(t, pullBody(t, s)), s, noImages)
	if diags != nil {
		t.Fatal(diags)
	}
	if plan.Changed || len(plan.Changes) != 0 {
		t.Fatalf("expected no changes: %+v", plan.Changes)
	}
	out, err := plan.State.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"customServerField":"keep"`, `"pollId":7`, `"badSeoLink":true`, `"key":"mark1"`, `"UNDERLINE"`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %s in %s", want, out)
		}
	}
}

func TestEditingOneParagraphKeepsOtherBlocksAndItsKey(t *testing.T) {
	s := snapshot()
	body := strings.Replace(pullBody(t, s), "Первый абзац", "Первый абзац, но **новый**", 1)
	plan, diags := Build(parsed(t, body), s, noImages)
	if diags != nil {
		t.Fatal(diags)
	}
	if !plan.Changed || len(plan.Changes) != 1 || plan.Changes[0].Op != "edit" {
		t.Fatalf("%+v", plan.Changes)
	}
	first := plan.State.Blocks[0]
	if first.Key != "para1" || first.Text != "Первый абзац, но новый" || first.Styles[0].Style != "BOLD" {
		t.Fatalf("%+v", first)
	}
	out, _ := plan.State.Marshal()
	if !strings.Contains(out, `"badSeoLink":true`) || !strings.Contains(out, `"pollId":7`) {
		t.Fatal(out)
	}
}

func TestRemovingRawAnchorIsReportedInThePlan(t *testing.T) {
	s := snapshot()
	body := strings.Replace(pullBody(t, s), "<!-- dzen:raw key=\"poll1\" type=\"atomic:poll\" -->\n\n", "", 1)
	plan, diags := Build(parsed(t, body), s, noImages)
	if diags != nil {
		t.Fatal(diags)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Op != "remove" || !strings.Contains(plan.Changes[0].Detail, "poll1") {
		t.Fatalf("%+v", plan.Changes)
	}
}

func TestMovingRawAnchorKeepsTheBlock(t *testing.T) {
	s := snapshot()
	anchor := "<!-- dzen:raw key=\"poll1\" type=\"atomic:poll\" -->"
	body := "[новое начало](https://example.org)\n\n" + anchor + "\n\nПервый абзац\n"
	plan, diags := Build(parsed(t, body), s, noImages)
	if diags != nil {
		t.Fatal(diags)
	}
	out, _ := plan.State.Marshal()
	if !strings.Contains(out, `"pollId":7`) {
		t.Fatal(out)
	}
}

func TestUnknownRawAnchorFailsClosed(t *testing.T) {
	_, diags := Build(parsed(t, `<!-- dzen:raw key="ghost" type="x" -->`), snapshot(), noImages)
	if len(diags) == 0 {
		t.Fatal("accepted a raw anchor without a base block")
	}
	_, diags = Build(parsed(t, `<!-- dzen:raw key="poll1" type="x" -->`), nil, noImages)
	if len(diags) == 0 {
		t.Fatal("accepted a raw anchor without a snapshot")
	}
}

func TestNewDocumentBuildsFlatStateWithoutSnapshot(t *testing.T) {
	plan, diags := Build(parsed(t, "Текст [ссылка](https://a.example) и **жирный**\n\n![](./a.png \"подпись\")\n"), nil, func(src string) (string, error) {
		if src != "./a.png" {
			t.Fatal(src)
		}
		return "img9", nil
	})
	if diags != nil {
		t.Fatal(diags)
	}
	raw, err := plan.State.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Blocks []struct {
			Type string         `json:"type"`
			Text string         `json:"text"`
			Data map[string]any `json:"data"`
		} `json:"blocks"`
		EntityMap map[string]struct {
			Type string `json:"type"`
		} `json:"entityMap"`
		Draft any `json:"draftJsState"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Draft != nil || len(doc.Blocks) != 2 || doc.Blocks[1].Type != "atomic:image" || doc.Blocks[1].Text != "подпись" || doc.EntityMap["0"].Type != "LINK" {
		t.Fatalf("%s", raw)
	}
}

func TestParseFileTakesTitleFromLeadingH1AndNumbersLines(t *testing.T) {
	doc, diags := ParseFile([]byte("# Заголовок статьи\n\nТекст\n"))
	if len(diags) != 0 || doc.Front.Title != "Заголовок статьи" || len(doc.Nodes) != 1 {
		t.Fatalf("%+v %v", doc, diags)
	}
	_, diags = ParseFile([]byte("---\ntitle: \"t\"\n---\n\nтекст\n\n`код`\n"))
	if len(diags) != 1 || diags[0].Line != 7 {
		t.Fatalf("%+v", diags)
	}
}

func TestParseFileRejectsUnknownFrontMatterAndBadLimits(t *testing.T) {
	for name, src := range map[string]string{
		"unknown field": "---\ntitle: t\ntags: [a]\n---\n\nтекст\n",
		"no title":      "текст\n",
		"long title":    "---\ntitle: \"" + strings.Repeat("я", MaxTitle+1) + "\"\n---\n\nтекст\n",
		"link in title": "---\ntitle: \"см https://a.b\"\n---\n\nтекст\n",
		"bad comments":  "---\ntitle: t\ncomments: maybe\n---\n\nтекст\n",
	} {
		src := src
		t.Run(name, func(t *testing.T) {
			if _, diags := ParseFile([]byte(src)); len(diags) == 0 {
				t.Fatal("accepted")
			}
		})
	}
}

func TestFormatRoundTripsFrontMatter(t *testing.T) {
	snippet := `с "кавычками": да`
	yes := true
	front := FrontMatter{Title: "Заголовок: с двоеточием", Snippet: &snippet, Comments: "on", CommentsVisibility: "visible", NativeAd: &yes, Dzen: &Meta{Schema: 1, PublicationID: "p", PublisherID: "q", Version: 4, Status: "draft"}}
	doc, diags := ParseFile(Format(front, "Текст\n"))
	if len(diags) != 0 {
		t.Fatal(diags)
	}
	if doc.Front.Title != front.Title || *doc.Front.Snippet != snippet || *doc.Front.Dzen != *front.Dzen || !*doc.Front.NativeAd {
		t.Fatalf("%+v", doc.Front)
	}
}

func TestSnapshotImageRecognition(t *testing.T) {
	info := ImageInfo{ID: "img1", Namespace: "zen_doc", GroupID: 5, ImageName: "pub_x_img1"}
	s := &Snapshot{Images: Images{"img1": info}}
	if id, ok := s.IsBaseImage(info.URL()); !ok || id != "img1" {
		t.Fatal(id, ok)
	}
	if id, ok := s.IsBaseImage("https://avatars.dzeninfra.ru/get-zen_doc/5/pub_x_img1/orig"); !ok || id != "img1" {
		t.Fatal(id, ok)
	}
	if _, ok := s.IsBaseImage("https://avatars.dzeninfra.ru/get-zen_doc/9/other/orig"); ok {
		t.Fatal("foreign image treated as base")
	}
}

func TestMovedParagraphKeepsItsOriginalBlock(t *testing.T) {
	s := snapshot()
	anchor := "<!-- dzen:raw key=\"poll1\" type=\"atomic:poll\" -->"
	body := "[читать здесь](https://example.com)\n\n" + anchor + "\n\nПервый абзац\n"
	state, _ := ParseContentState(s.ContentState)
	node := NodeFromBlock(state.Blocks[2], state.EntityMap, nil)
	body = strings.Replace(body, "[читать здесь](https://example.com)", RenderNode(node), 1)
	plan, diags := Build(parsed(t, body), s, noImages)
	if diags != nil {
		t.Fatal(diags)
	}
	out, _ := plan.State.Marshal()
	if !strings.Contains(out, `"UNDERLINE"`) || !strings.Contains(out, `"customServerField":"keep"`) || plan.State.Blocks[0].Key != "link1" {
		t.Fatalf("%s", out)
	}
	if !plan.Changed {
		t.Fatal("a reorder is a change")
	}
}

func TestRepeatedRawAnchorFailsClosed(t *testing.T) {
	anchor := "<!-- dzen:raw key=\"poll1\" type=\"atomic:poll\" -->\n\n"
	if _, diags := Build(parsed(t, anchor+anchor), snapshot(), noImages); len(diags) == 0 {
		t.Fatal("two copies of one block were accepted")
	}
}
