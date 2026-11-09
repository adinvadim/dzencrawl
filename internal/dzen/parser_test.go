package dzen

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseArticle(t *testing.T) {
	state, _ := json.Marshal(map[string]any{"draftJsState": map[string]any{"blocks": []any{map[string]any{"type": "header-two", "text": "Раздел"}, map[string]any{"type": "unstyled", "text": "Текст статьи"}}}})
	root := map[string]any{"ssrData": map[string]any{
		"zenParams":          map[string]any{"zenReqid": "rid"},
		"socialMetaResponse": map[string]any{"items": []any{map[string]any{"metaInfo": map[string]any{"likeCount": 12, "commentsCount": 1}}}},
		"publishersResponse": map[string]any{"data": map[string]any{"data": map[string]any{
			"publisher":   map[string]any{"id": "publisher"},
			"publication": map[string]any{"id": "article", "publisherId": "publisher", "publishTime": 1700000000000, "modificationTime": 1700000001000, "commentsDocumentId": "native:article", "content": map[string]any{"preview": map[string]any{"title": "Заголовок", "timeToReadSeconds": 90}, "articleContent": map[string]any{"contentState": string(state)}}, "publicationStatistics": map[string]any{"views": 100, "viewsTillEnd": 75}},
		}}},
		"commentsForRobot": map[string]any{"serverTs": 1700000002000, "meta": map[string]any{}, "usersById": map[string]any{"7": map[string]any{"displayName": "Анна"}}, "metaByCommentId": map[string]any{"42": map[string]any{"likes": 3, "dislikes": 1, "childrenCount": 0}}, "items": []any{map[string]any{"entity": "comment", "entityData": map[string]any{"id": 42, "authorSafeUid": "7", "text": "Комментарий", "createdTs": 1700000003000}}}},
	}}
	payload, _ := json.Marshal(root)
	html := "<html><script>var _params=(" + string(payload) + ");</script></html>"
	article, comments, cc, err := ParseArticle(html, "https://dzen.ru/a/test", "example_channel")
	if err != nil {
		t.Fatal(err)
	}
	if article.Title != "Заголовок" || article.Views != 100 || article.CommentsCount != 1 || article.Likes != 12 {
		t.Fatalf("unexpected article: %+v", article)
	}
	if !strings.Contains(article.Markdown, "## Раздел") || !strings.Contains(article.Markdown, "Текст статьи") {
		t.Fatalf("bad fallback markdown: %s", article.Markdown)
	}
	if len(comments) != 1 || comments[0].AuthorName != "Анна" || comments[0].Likes != 3 {
		t.Fatalf("unexpected comments: %+v", comments)
	}
	if cc.DocumentID != "native:article" || cc.RID != "rid" {
		t.Fatalf("unexpected context: %+v", cc)
	}
}

func TestChannelName(t *testing.T) {
	for input, want := range map[string]string{"example_channel": "example_channel", "https://dzen.ru/example_channel": "example_channel", "dzen.ru/example_channel": "example_channel", "https://dzen.ru/id/5f00aa": "id/5f00aa"} {
		got, err := ChannelName(input)
		if err != nil || got != want {
			t.Fatalf("ChannelName(%q)=%q,%v", input, got, err)
		}
	}
	if _, err := ChannelName("https://example.com/x"); err == nil {
		t.Fatal("expected host validation")
	}
}

type recordingTransport struct{ requests int }

func (r *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.requests++
	return nil, errors.New("no network in this test")
}

func TestUnknownCommentCountIsNeverComplete(t *testing.T) {
	transport := &recordingTransport{}
	client := NewClient("ua", 0)
	client.HTTP = &http.Client{Transport: transport}
	if _, complete, err := client.AllComments(context.Background(), Article{}, nil, CommentContext{}); err != nil || complete {
		t.Fatal(complete, err)
	}
	if _, complete, err := client.AllComments(context.Background(), Article{CommentsCountKnown: true}, nil, CommentContext{DocumentID: "native:a"}); err != nil || !complete {
		t.Fatal(complete, err)
	}
	if transport.requests != 0 {
		t.Fatal("a known-empty article must not call the API")
	}
	_, complete, err := client.AllComments(context.Background(), Article{ID: "a", CommentsCountKnown: true, CommentsCount: 2}, []Comment{{ID: "kept"}}, CommentContext{DocumentID: "native:a"})
	if err == nil || complete {
		t.Fatal(complete, err)
	}
}

func TestErrorsDoNotCopyResponseBodies(t *testing.T) {
	client := NewClient("ua", 0)
	client.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("private-body")), Header: http.Header{}}, nil
	})}
	_, err := client.ChannelPage(context.Background(), "c", 1)
	if err == nil || strings.Contains(err.Error(), "private-body") {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIsArticleItem(t *testing.T) {
	if !IsArticleItem("card") || IsArticleItem("gif") || IsArticleItem("short_video") {
		t.Fatal("wrong classification")
	}
}
