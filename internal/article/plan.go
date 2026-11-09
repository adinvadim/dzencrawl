package article

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Change is one line of a push plan.
type Change struct {
	Op     string `json:"op"`
	Detail string `json:"detail"`
}

// Plan is the document a push would send.
type Plan struct {
	State   ContentState
	Changes []Change
	Changed bool
}

// ImageResolver maps a Markdown image source to an uploaded Dzen image id.
type ImageResolver func(src string) (string, error)

// IsBaseImage reports the image id of a URL that belongs to the snapshot.
func (s *Snapshot) IsBaseImage(src string) (string, bool) {
	if s == nil {
		return "", false
	}
	for id, info := range s.Images {
		if info.GroupID != 0 && strings.HasPrefix(src, strings.TrimSuffix(info.URL(), "scale_1200")) {
			return id, true
		}
	}
	return "", false
}

// IsDzenImageURL reports whether a source points at Dzen's image host.
func IsDzenImageURL(src string) bool {
	return strings.Contains(src, "avatars.dzeninfra.ru/") || strings.Contains(src, "avatars.mds.yandex.net/")
}

// ImageSources lists the sources of every image in the given nodes.
func ImageSources(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		switch n.Kind {
		case KindImage:
			out = append(out, n.Src)
		case KindGallery:
			for _, item := range n.Gallery {
				out = append(out, item.Src)
			}
		}
	}
	return out
}

// Build aligns the parsed file with the snapshot and assembles the document.
// A block whose canonical Markdown is unchanged is reused verbatim.
func Build(nodes []Node, base *Snapshot, resolve ImageResolver) (Plan, []Diagnostic) {
	var plan Plan
	var diags []Diagnostic
	baseState := ContentState{EntityMap: map[string]Entity{}}
	var images Images
	if base != nil {
		var err error
		if baseState, err = ParseContentState(base.ContentState); err != nil {
			return plan, []Diagnostic{{Line: 1, Code: "unexpected_response_shape", Message: err.Error()}}
		}
		images = base.Images
	}
	var baseBlocks []Block
	var marker *Block
	for i := range baseState.Blocks {
		if baseState.Blocks[i].Type == "marker" {
			marker = &baseState.Blocks[i]
			continue
		}
		baseBlocks = append(baseBlocks, baseState.Blocks[i])
	}
	baseNodes := make([]Node, len(baseBlocks))
	baseCanon := make([]string, len(baseBlocks))
	baseIndexByKey := map[string]int{}
	for i, b := range baseBlocks {
		baseNodes[i] = NodeFromBlock(b, baseState.EntityMap, images)
		baseCanon[i] = RenderNode(baseNodes[i])
		baseIndexByKey[b.Key] = i
	}
	localCanon := make([]string, len(nodes))
	rawSeen := map[string]bool{}
	for i, n := range nodes {
		localCanon[i] = RenderNode(n)
		if n.Kind != KindRaw {
			continue
		}
		if rawSeen[n.RawKey] {
			diags = append(diags, Diagnostic{Line: n.Line, Code: "markdown_unsupported", Message: "dzen:raw key " + n.RawKey + " appears twice"})
		}
		rawSeen[n.RawKey] = true
		if base == nil {
			diags = append(diags, Diagnostic{Line: n.Line, Code: "markdown_unsupported", Message: "dzen:raw needs the pulled snapshot; run dzen pull"})
		} else if _, ok := baseIndexByKey[n.RawKey]; !ok {
			diags = append(diags, Diagnostic{Line: n.Line, Code: "markdown_unsupported", Message: "dzen:raw key " + n.RawKey + " is not in the snapshot"})
		}
	}
	if len(diags) != 0 {
		return plan, diags
	}

	match := lcs(baseCanon, localCanon)
	source := make([]int, len(nodes))
	edited := make([]int, len(nodes))
	for i := range nodes {
		source[i], edited[i] = -1, -1
	}
	usedBase := make([]bool, len(baseBlocks))
	for _, m := range match {
		source[m[1]] = m[0]
		usedBase[m[0]] = true
	}
	moved := false
	for i, n := range nodes {
		if n.Kind == KindRaw && source[i] < 0 {
			source[i] = baseIndexByKey[n.RawKey]
			usedBase[source[i]] = true
			moved = true
		}
	}
	// A block moved to another position keeps its original JSON too.
	for i := range nodes {
		if source[i] >= 0 {
			continue
		}
		for b := range baseBlocks {
			if !usedBase[b] && baseCanon[b] == localCanon[i] {
				source[i], usedBase[b], moved = b, true, true
				break
			}
		}
	}
	pairEdits(nodes, baseBlocks, baseNodes, match, source, edited, usedBase)

	keys := map[string]bool{}
	for _, b := range baseBlocks {
		keys[b.Key] = true
	}
	if marker != nil {
		keys[marker.Key] = true
	}
	next := 0
	for k := range baseState.EntityMap {
		next = max(next, numericKey(k)+1)
	}
	out := ContentState{EntityMap: map[string]Entity{}}
	for i, n := range nodes {
		if src := source[i]; src >= 0 {
			block := baseBlocks[src]
			for _, r := range block.Entities {
				k := strconv.Itoa(r.Key)
				out.EntityMap[k] = baseState.EntityMap[k]
			}
			out.Blocks = append(out.Blocks, block)
			continue
		}
		var prior *Block
		key := ""
		if src := edited[i]; src >= 0 {
			prior = &baseBlocks[src]
			key = prior.Key
			plan.Changes = append(plan.Changes, Change{Op: "edit", Detail: fmt.Sprintf("line %d: %s", n.Line, describe(n))})
		} else {
			key = newKey(keys)
			plan.Changes = append(plan.Changes, Change{Op: "add", Detail: fmt.Sprintf("line %d: %s", n.Line, describe(n))})
		}
		block, err := nodeBlock(n, key, prior, resolve)
		if err != nil {
			diags = append(diags, Diagnostic{Line: n.Line, Code: "validation_image", Message: err.Error()})
			continue
		}
		for _, l := range n.Links {
			k := strconv.Itoa(next)
			next++
			out.EntityMap[k] = Entity{Type: "LINK", Mutability: "MUTABLE", Data: map[string]any{"url": l.URL, "href": l.URL}}
			block.Entities = append(block.Entities, EntityRange{Key: numericKey(k), Offset: l.Offset, Length: l.Length})
		}
		out.Blocks = append(out.Blocks, block)
	}
	if len(diags) != 0 {
		return plan, diags
	}
	for i, used := range usedBase {
		if used {
			continue
		}
		plan.Changes = append(plan.Changes, Change{Op: "remove", Detail: describe(baseNodes[i])})
	}
	if marker != nil {
		out.Blocks = append(out.Blocks, *marker)
	}
	plan.State = out
	plan.Changed = len(plan.Changes) != 0 || moved || reordered(match)
	if moved {
		plan.Changes = append(plan.Changes, Change{Op: "move", Detail: "blocks changed position"})
	}
	return plan, nil
}

func reordered(match [][2]int) bool {
	for i := 1; i < len(match); i++ {
		if match[i][0] < match[i-1][0] {
			return true
		}
	}
	return false
}

// lcs returns matched (base, local) index pairs in order.
func lcs(a, b []string) [][2]int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	var out [][2]int
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			out = append(out, [2]int{i, j})
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

// pairEdits treats unmatched neighbours of the same family as edits, so that
// a changed paragraph keeps its block key and image data.
func pairEdits(nodes []Node, baseBlocks []Block, baseNodes []Node, match [][2]int, source, edited []int, usedBase []bool) {
	gaps := append(append([][2]int{{-1, -1}}, match...), [2]int{len(baseBlocks), len(nodes)})
	for g := 1; g < len(gaps); g++ {
		var freeBase, freeLocal []int
		for i := gaps[g-1][0] + 1; i < gaps[g][0]; i++ {
			if !usedBase[i] {
				freeBase = append(freeBase, i)
			}
		}
		for j := gaps[g-1][1] + 1; j < gaps[g][1]; j++ {
			if source[j] < 0 {
				freeLocal = append(freeLocal, j)
			}
		}
		for n := 0; n < len(freeBase) && n < len(freeLocal); n++ {
			b, l := freeBase[n], freeLocal[n]
			if family(baseNodes[b].Kind) == family(nodes[l].Kind) {
				edited[l] = b
				usedBase[b] = true
			}
		}
	}
	_ = baseBlocks
}

func family(kind string) string {
	switch kind {
	case KindImage, KindGallery, KindEmbed, KindRaw, KindEmpty:
		return kind
	}
	return "text"
}

func nodeBlock(n Node, key string, prior *Block, resolve ImageResolver) (Block, error) {
	b := Block{Key: key, Type: n.Kind, Text: n.Text, Styles: append([]StyleRange{}, n.Styles...), Entities: []EntityRange{}, Data: map[string]any{}}
	sort.SliceStable(b.Styles, func(i, j int) bool { return b.Styles[i].Offset < b.Styles[j].Offset })
	switch n.Kind {
	case KindEmpty:
		b.Type, b.Text = "unstyled", ""
	case KindEmbed:
		b.Type = "atomic:embed"
		b.Data = map[string]any{"embedData": map[string]any{"type": embedType(n.Embed), "postUrl": n.Embed}}
	case KindImage:
		id, err := resolve(n.Src)
		if err != nil {
			return b, err
		}
		b.Type, b.Text = "atomic:image", n.Caption
		b.Data = map[string]any{"image": map[string]any{"id": id}}
	case KindGallery:
		images := make([]any, 0, len(n.Gallery))
		contents := map[string]any{}
		first := ""
		for _, item := range n.Gallery {
			id, err := resolve(item.Src)
			if err != nil {
				return b, err
			}
			if first == "" {
				first = id
			}
			images = append(images, map[string]any{"id": id})
			if item.Caption != "" {
				contents[id] = item.Caption
			}
		}
		b.Type, b.Text = "atomic:image", ""
		b.Data = map[string]any{"image": map[string]any{"id": first}, "images": images, "contents": contents}
	case KindRaw:
		return b, fmt.Errorf("dzen:raw key %s has no base block", n.RawKey)
	}
	return b, nil
}

func describe(n Node) string {
	switch n.Kind {
	case KindImage:
		return "image " + n.Src
	case KindGallery:
		return fmt.Sprintf("gallery of %d images", len(n.Gallery))
	case KindEmbed:
		return "embed " + n.Embed
	case KindRaw:
		return "raw block " + n.RawKey + " " + n.RawType
	case KindEmpty:
		return "empty paragraph"
	}
	text := []rune(n.Text)
	if len(text) > 48 {
		text = append(text[:48], '…')
	}
	return n.Kind + ": " + string(text)
}

const keyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func newKey(used map[string]bool) string {
	for {
		buf := make([]byte, 5)
		_, _ = rand.Read(buf)
		for i := range buf {
			buf[i] = keyAlphabet[int(buf[i])%len(keyAlphabet)]
		}
		key := string(buf)
		if !used[key] {
			used[key] = true
			return key
		}
	}
}
