package article

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

// Documented Dzen limits, checked before any network call.
const (
	MaxTitle    = 140
	MaxBody     = 100000
	MaxSnippet  = 1000
	MaxCaption  = 2000
	MaxImages   = 100
	MaxGallery  = 30
	SchemaLevel = 1
)

// Meta is the machine-managed part of the front matter.
type Meta struct {
	Schema        int    `yaml:"schema" json:"schema"`
	PublicationID string `yaml:"publication_id" json:"publication_id"`
	PublisherID   string `yaml:"publisher_id" json:"publisher_id"`
	Version       int64  `yaml:"version" json:"version"`
	Status        string `yaml:"status" json:"status"`
}

// FrontMatter holds the author fields of an article file.
type FrontMatter struct {
	Title              string  `yaml:"title"`
	Cover              string  `yaml:"cover"`
	Snippet            *string `yaml:"snippet"`
	Comments           string  `yaml:"comments"`
	CommentsVisibility string  `yaml:"comments_visibility"`
	NativeAd           *bool   `yaml:"native_ad"`
	CommentsTitle      *string `yaml:"comments_title"`
	Dzen               *Meta   `yaml:"dzen"`
}

// Document is a parsed article file.
type Document struct {
	Front FrontMatter
	Body  string
	Nodes []Node
}

var (
	titleLink = regexp.MustCompile(`https?://|\]\(`)
	leadingH1 = regexp.MustCompile(`^\s*#[ \t]+(.+?)[ \t]*(\n|$)`)
)

// ParseFile parses an article file and applies the local fail-closed rules.
func ParseFile(src []byte) (Document, []Diagnostic) {
	var doc Document
	var diags []Diagnostic
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	body := src
	bodyLine := 1
	if bytes.HasPrefix(src, []byte("---\n")) {
		end := bytes.Index(src[4:], []byte("\n---"))
		if end < 0 {
			return doc, []Diagnostic{{Line: 1, Code: "validation_front_matter", Message: "front matter is not closed with ---"}}
		}
		raw := src[4 : 4+end]
		rest := src[4+end+4:]
		rest = bytes.TrimPrefix(rest, []byte("\n"))
		bodyLine = bytes.Count(src[:len(src)-len(rest)], []byte("\n")) + 1
		body = rest
		if err := yaml.UnmarshalWithOptions(raw, &doc.Front, yaml.Strict()); err != nil {
			return doc, []Diagnostic{{Line: 1, Code: "validation_front_matter", Message: yaml.FormatError(err, false, false)}}
		}
	}
	text := string(body)
	if match := leadingH1.FindStringSubmatchIndex(text); match != nil && doc.Front.Title == "" {
		doc.Front.Title = text[match[2]:match[3]]
		text = strings.Repeat("\n", strings.Count(text[:match[1]], "\n")) + text[match[1]:]
	}
	doc.Body = text
	nodes, parseDiags := ParseBody(text)
	for _, d := range parseDiags {
		d.Line += bodyLine - 1
		diags = append(diags, d)
	}
	for i := range nodes {
		nodes[i].Line += bodyLine - 1
	}
	doc.Nodes = nodes
	diags = append(diags, validate(doc)...)
	return doc, diags
}

func validate(doc Document) []Diagnostic {
	var out []Diagnostic
	fail := func(code, format string, args ...any) {
		out = append(out, Diagnostic{Line: 1, Code: code, Message: fmt.Sprintf(format, args...)})
	}
	front := doc.Front
	switch n := utf8.RuneCountInString(front.Title); {
	case n == 0:
		fail("validation_title_missing", "set title in front matter or start the body with # Title")
	case n > MaxTitle:
		fail("validation_title_too_long", "title has %d characters, the limit is %d", n, MaxTitle)
	}
	if titleLink.MatchString(front.Title) {
		fail("validation_title_has_link", "Dzen does not allow links in titles")
	}
	if front.Snippet != nil && utf8.RuneCountInString(*front.Snippet) > MaxSnippet {
		fail("validation_snippet_too_long", "snippet is longer than %d characters", MaxSnippet)
	}
	if front.Comments != "" && front.Comments != "on" && front.Comments != "off" {
		fail("validation_comments", "comments must be on or off")
	}
	switch front.CommentsVisibility {
	case "", "visible", "invisible", "subscribe-visible":
	default:
		fail("validation_comments_visibility", "comments_visibility must be visible, invisible or subscribe-visible")
	}
	bodyLen, images := 0, 0
	for _, n := range doc.Nodes {
		bodyLen += utf8.RuneCountInString(n.Text)
		switch n.Kind {
		case KindImage:
			images++
			if utf8.RuneCountInString(n.Caption) > MaxCaption {
				out = append(out, Diagnostic{Line: n.Line, Code: "validation_caption_too_long", Message: "image caption is too long"})
			}
		case KindEmbed:
			images++
		case KindGallery:
			images += len(n.Gallery)
			if len(n.Gallery) > MaxGallery {
				out = append(out, Diagnostic{Line: n.Line, Code: "validation_gallery_too_large", Message: fmt.Sprintf("a gallery holds at most %d images", MaxGallery)})
			}
			for _, item := range n.Gallery {
				if utf8.RuneCountInString(item.Caption) > MaxCaption {
					out = append(out, Diagnostic{Line: n.Line, Code: "validation_caption_too_long", Message: "image caption is too long"})
				}
			}
		}
	}
	if bodyLen > MaxBody {
		fail("validation_body_too_long", "body has %d characters, the limit is %d", bodyLen, MaxBody)
	}
	if images > MaxImages {
		fail("validation_too_many_attachments", "article has %d images, the limit is %d", images, MaxImages)
	}
	return out
}

// Format writes an article file in canonical form.
func Format(front FrontMatter, body string) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	write := func(key, value string) { b.WriteString(key + ": " + value + "\n") }
	write("title", quote(front.Title))
	if front.Cover != "" {
		write("cover", quote(front.Cover))
	}
	if front.Snippet != nil {
		write("snippet", quote(*front.Snippet))
	}
	if front.Comments != "" {
		write("comments", quote(front.Comments))
	}
	if front.CommentsVisibility != "" {
		write("comments_visibility", front.CommentsVisibility)
	}
	if front.NativeAd != nil {
		write("native_ad", fmt.Sprint(*front.NativeAd))
	}
	if front.CommentsTitle != nil {
		write("comments_title", quote(*front.CommentsTitle))
	}
	if front.Dzen != nil {
		b.WriteString("dzen:\n")
		fmt.Fprintf(&b, "  schema: %d\n  publication_id: %s\n  publisher_id: %s\n  version: %d\n  status: %s\n", front.Dzen.Schema, quote(front.Dzen.PublicationID), quote(front.Dzen.PublisherID), front.Dzen.Version, front.Dzen.Status)
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	return b.Bytes()
}

// quote returns a YAML double-quoted scalar. JSON strings are valid YAML.
func quote(value string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	return strings.TrimSpace(b.String())
}

// Settings are the publication fields carried outside the body.
type Settings struct {
	Title         string `json:"title"`
	Snippet       string `json:"snippet"`
	SnippetFrozen bool   `json:"snippet_frozen"`
	CoverID       string `json:"cover_id"`
	Comments      string `json:"comments"`
	Visibility    string `json:"comments_visibility"`
	NativeAds     bool   `json:"native_ads"`
	CommentsTitle string `json:"comments_title"`
}

// Snapshot is the untouched server state a working copy was pulled from.
type Snapshot struct {
	Schema        int               `json:"schema"`
	PublicationID string            `json:"publication_id"`
	PublisherID   string            `json:"publisher_id"`
	Version       int64             `json:"version"`
	ModTime       int64             `json:"mod_time"`
	Status        string            `json:"status"`
	ContentState  string            `json:"content_state"`
	Images        Images            `json:"images"`
	Settings      Settings          `json:"settings"`
	Uploads       map[string]string `json:"uploads"`
	CoverSource   string            `json:"cover_source,omitempty"`
}

// SnapshotPath returns the snapshot location for a working copy file.
func SnapshotPath(file, publicationID string) string {
	return filepath.Join(filepath.Dir(file), ".dzen", publicationID+".json")
}

// LoadSnapshot reads a snapshot; a missing file returns os.ErrNotExist.
func LoadSnapshot(path string) (Snapshot, error) {
	var s Snapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("decode snapshot %s: %w", path, err)
	}
	if s.Uploads == nil {
		s.Uploads = map[string]string{}
	}
	return s, nil
}

// SaveSnapshot atomically writes a snapshot next to the working copy. It holds
// draft text, so the directory is private and ignored by git.
func SaveSnapshot(path string, s Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(ignore, []byte("*\n"), 0o600); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o600)
}

// AtomicWrite replaces path through a temporary file in the same directory.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dzen-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
