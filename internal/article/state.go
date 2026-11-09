// Package article converts between Dzen's Draft.js documents and the Markdown
// files agents edit. Markdown is a view: unchanged blocks are sent back from
// the pulled snapshot byte for byte.
package article

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// StyleRange is one Draft.js inline style range, in Unicode code points.
type StyleRange struct {
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	Style  string `json:"style"`
}

// EntityRange points a text range at an entry of the entity map.
type EntityRange struct {
	Key    int `json:"key"`
	Offset int `json:"offset"`
	Length int `json:"length"`
}

// Block is a Draft.js block. Raw holds the original JSON when it came from Dzen.
type Block struct {
	Key      string          `json:"key"`
	Text     string          `json:"text"`
	Type     string          `json:"type"`
	Depth    int             `json:"depth"`
	Styles   []StyleRange    `json:"inlineStyleRanges"`
	Entities []EntityRange   `json:"entityRanges"`
	Data     map[string]any  `json:"data"`
	Raw      json.RawMessage `json:"-"`
}

// Entity is a Draft.js entity. Raw holds the original JSON when it came from Dzen.
type Entity struct {
	Type       string          `json:"type"`
	Mutability string          `json:"mutability"`
	Data       map[string]any  `json:"data"`
	Raw        json.RawMessage `json:"-"`
}

// ContentState is a parsed Draft.js document.
type ContentState struct {
	Blocks    []Block
	EntityMap map[string]Entity
}

// ParseContentState accepts both shapes Dzen returns: flat {blocks, entityMap}
// from the studio and {draftJsState: {...}} from public pages. An empty string
// is a new, empty draft.
func ParseContentState(raw string) (ContentState, error) {
	out := ContentState{EntityMap: map[string]Entity{}}
	if raw == "" {
		return out, nil
	}
	var probe struct {
		Draft     json.RawMessage            `json:"draftJsState"`
		Blocks    []json.RawMessage          `json:"blocks"`
		EntityMap map[string]json.RawMessage `json:"entityMap"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return out, fmt.Errorf("decode contentState: %w", err)
	}
	if len(probe.Draft) > 0 {
		probe.Blocks, probe.EntityMap = nil, nil
		if err := json.Unmarshal(probe.Draft, &probe); err != nil {
			return out, fmt.Errorf("decode draftJsState: %w", err)
		}
	}
	for _, rawBlock := range probe.Blocks {
		var b Block
		if err := json.Unmarshal(rawBlock, &b); err != nil {
			return out, fmt.Errorf("decode block: %w", err)
		}
		b.Raw = append(json.RawMessage(nil), rawBlock...)
		out.Blocks = append(out.Blocks, b)
	}
	for key, rawEntity := range probe.EntityMap {
		var e Entity
		if err := json.Unmarshal(rawEntity, &e); err != nil {
			return out, fmt.Errorf("decode entity %s: %w", key, err)
		}
		e.Raw = append(json.RawMessage(nil), rawEntity...)
		out.EntityMap[key] = e
	}
	return out, nil
}

// Marshal writes the flat {blocks, entityMap} form Dzen accepts on save.
func (c ContentState) Marshal() (string, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"blocks":[`)
	for i, b := range c.Blocks {
		if i > 0 {
			buf.WriteByte(',')
		}
		data, err := marshalBlock(b)
		if err != nil {
			return "", err
		}
		buf.Write(data)
	}
	buf.WriteString(`],"entityMap":{`)
	keys := make([]string, 0, len(c.EntityMap))
	for k := range c.EntityMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return numericKey(keys[i]) < numericKey(keys[j]) })
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, _ := json.Marshal(k)
		buf.Write(name)
		buf.WriteByte(':')
		data := []byte(c.EntityMap[k].Raw)
		if len(data) == 0 {
			var err error
			if data, err = json.Marshal(c.EntityMap[k]); err != nil {
				return "", err
			}
		}
		buf.Write(data)
	}
	buf.WriteString(`}}`)
	return buf.String(), nil
}

func marshalBlock(b Block) ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	if b.Styles == nil {
		b.Styles = []StyleRange{}
	}
	if b.Entities == nil {
		b.Entities = []EntityRange{}
	}
	if b.Data == nil {
		b.Data = map[string]any{}
	}
	return json.Marshal(b)
}

func numericKey(k string) int {
	n, err := strconv.Atoi(k)
	if err != nil {
		return 1 << 30
	}
	return n
}

// ImageInfo is an uploaded image from the publication's image directory.
type ImageInfo struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	GroupID   int64  `json:"groupId"`
	ImageName string `json:"imageName"`
}

// URL returns the stable preview URL used in Markdown.
func (i ImageInfo) URL() string {
	ns := i.Namespace
	if ns == "" {
		ns = "zen_doc"
	}
	return fmt.Sprintf("https://avatars.dzeninfra.ru/get-%s/%d/%s/scale_1200", ns, i.GroupID, i.ImageName)
}
