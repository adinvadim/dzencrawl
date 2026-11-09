package article

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCorpusRoundTrip replays real public articles when DZEN_CORPUS points at a
// directory of captured publication JSON. The corpus holds third-party text,
// so it stays outside the repository.
func TestCorpusRoundTrip(t *testing.T) {
	dir := os.Getenv("DZEN_CORPUS")
	if dir == "" {
		t.Skip("DZEN_CORPUS is not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus files: %v", err)
	}
	blocks, raw := 0, 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Publication struct {
				Content struct {
					ArticleContent struct {
						ContentState string `json:"contentState"`
					} `json:"articleContent"`
				} `json:"content"`
			} `json:"publication"`
			Images Images `json:"images"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		snap := &Snapshot{ContentState: doc.Publication.Content.ArticleContent.ContentState, Images: doc.Images}
		state, err := ParseContentState(snap.ContentState)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		var nodes []Node
		for _, b := range state.Blocks {
			if b.Type == "marker" {
				continue
			}
			n := NodeFromBlock(b, state.EntityMap, doc.Images)
			blocks++
			if n.Kind == KindRaw {
				raw++
			}
			nodes = append(nodes, n)
		}
		body := RenderBody(nodes)
		parsed, diags := ParseBody(body)
		if len(diags) != 0 {
			t.Errorf("%s: %v", filepath.Base(file), diags[0])
			continue
		}
		plan, diags := Build(parsed, snap, func(src string) (string, error) { t.Errorf("%s: image would upload: %s", file, src); return "", nil })
		if diags != nil {
			t.Errorf("%s: %v", filepath.Base(file), diags[0])
			continue
		}
		if plan.Changed {
			t.Errorf("%s: %d spurious changes, first: %+v", filepath.Base(file), len(plan.Changes), plan.Changes[0])
			continue
		}
		out, err := plan.State.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		again, _ := ParseContentState(out)
		if len(again.Blocks) != len(state.Blocks) {
			t.Errorf("%s: block count changed", filepath.Base(file))
		}
		for i := range state.Blocks {
			if string(again.Blocks[i].Raw) != string(state.Blocks[i].Raw) {
				t.Errorf("%s: block %d changed", filepath.Base(file), i)
				break
			}
		}
	}
	t.Logf("%d articles, %d blocks, %d raw anchors", len(files), blocks, raw)
}
