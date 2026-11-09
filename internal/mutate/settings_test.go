package mutate

import (
	"testing"

	"github.com/adinvadim/dzencrawl/internal/article"
)

// Dzen discards a save where comments are off but visible, or on but invisible.
func TestMergeSettingsKeepsCommentSwitchesConsistent(t *testing.T) {
	on := article.Settings{Comments: "on", Visibility: "visible"}
	off := article.Settings{Comments: "off", Visibility: "invisible"}
	cases := []struct {
		name               string
		comments, visible  string
		current            article.Settings
		wantComments, want string
	}{
		{"pulled file turns comments off", "off", "visible", on, "off", "invisible"},
		{"pulled file hides comments", "on", "invisible", on, "off", "invisible"},
		{"pulled file turns comments back on", "on", "invisible", off, "on", "visible"},
		{"pulled file makes comments visible", "off", "visible", off, "on", "visible"},
		{"subscribers only is left alone", "on", "subscribe-visible", on, "on", "subscribe-visible"},
		{"new article with comments off", "off", "", article.Settings{}, "off", "invisible"},
		{"new article with defaults", "", "", article.Settings{}, "on", "visible"},
		{"only comments on over a hidden article", "on", "", off, "on", "visible"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			front := article.FrontMatter{Title: "T", Comments: c.comments, CommentsVisibility: c.visible}
			got := MergeSettings(front, c.current)
			if got.Comments != c.wantComments || got.Visibility != c.want {
				t.Fatalf("got %s/%s, want %s/%s", got.Comments, got.Visibility, c.wantComments, c.want)
			}
		})
	}
}
