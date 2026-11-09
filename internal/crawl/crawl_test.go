package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/store"
)

func page(items ...[2]string) dzen.ChannelPage {
	var out dzen.ChannelPage
	out.PageCount = 2
	for _, item := range items {
		entry := out.Items
		entry = append(entry, struct {
			PublicationObjectID string `json:"publication_object_id"`
			Type                string `json:"type"`
			Title               string `json:"title"`
			Link                string `json:"link"`
			Views               int64  `json:"views"`
			TimeToReadSeconds   int64  `json:"timeToReadSeconds"`
			CreationTime        string `json:"creation_time"`
			SocialInfo          struct {
				Likes    int64 `json:"likesCount"`
				Comments int64 `json:"commentCount"`
			} `json:"socialInfo"`
		}{PublicationObjectID: item[0], Type: item[1], Link: "https://dzen.ru/a/" + item[0]})
		out.Items = entry
	}
	return out
}

func articleHTML(t *testing.T, id string) string {
	t.Helper()
	state, _ := json.Marshal(map[string]any{"draftJsState": map[string]any{"blocks": []any{map[string]any{"key": "a", "type": "unstyled", "text": "Текст " + id, "depth": 0, "inlineStyleRanges": []any{}, "entityRanges": []any{}, "data": map[string]any{}}}, "entityMap": map[string]any{}}, "mentions": []any{}})
	root := map[string]any{"ssrData": map[string]any{"publishersResponse": map[string]any{"data": map[string]any{"data": map[string]any{"publication": map[string]any{"id": id, "publisherId": "p", "content": map[string]any{"preview": map[string]any{"title": "Статья " + id}, "articleContent": map[string]any{"contentState": string(state)}}}}}}}}
	payload, _ := json.Marshal(root)
	return "var _params=(" + string(payload) + ");"
}

type fakePublic struct {
	t       *testing.T
	pages   map[int]dzen.ChannelPage
	failing map[string]bool
	fetched []string
	pageErr map[int]error
	asked   []int
}

func (f *fakePublic) ChannelPage(_ context.Context, _ string, n int) (dzen.ChannelPage, error) {
	f.asked = append(f.asked, n)
	if err := f.pageErr[n]; err != nil {
		return dzen.ChannelPage{}, err
	}
	return f.pages[n], nil
}

func (f *fakePublic) FetchArticle(_ context.Context, link string) (string, error) {
	f.fetched = append(f.fetched, link)
	id := link[len("https://dzen.ru/a/"):]
	if f.failing[id] {
		return "", errors.New("boom")
	}
	return articleHTML(f.t, id), nil
}

func (f *fakePublic) AllComments(_ context.Context, _ dzen.Article, initial []dzen.Comment, _ dzen.CommentContext) ([]dzen.Comment, bool, error) {
	return initial, true, nil
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func options() Options {
	return Options{Channel: "chan", Now: time.Now, Log: io.Discard}
}

func TestNonArticleItemsAreSkippedAndABadArticleDegradesTheRun(t *testing.T) {
	st := open(t)
	f := &fakePublic{t: t, failing: map[string]bool{"bad": true}, pages: map[int]dzen.ChannelPage{1: page([2]string{"good", "card"}, [2]string{"gif1", "gif"}, [2]string{"vid", "short_video"}, [2]string{"bad", "card"}), 2: page([2]string{"later", "card"})}}
	result, err := SyncChannel(context.Background(), st, f, options())
	if err != nil {
		t.Fatal(err)
	}
	if result.Records != 2 || !result.Degraded() || len(result.Warnings) != 1 {
		t.Fatalf("%+v", result)
	}
	for _, link := range f.fetched {
		if link == "https://dzen.ru/a/gif1" || link == "https://dzen.ru/a/vid" {
			t.Fatalf("fetched non-article %s", link)
		}
	}
}

func TestFailedPageResumesFromCheckpoint(t *testing.T) {
	st := open(t)
	f := &fakePublic{t: t, pages: map[int]dzen.ChannelPage{1: page([2]string{"one", "card"}), 2: page([2]string{"two", "card"})}, pageErr: map[int]error{2: errors.New("down")}}
	if _, err := SyncChannel(context.Background(), st, f, options()); err == nil {
		t.Fatal("expected failure")
	}
	f.pageErr, f.asked = nil, nil
	result, err := SyncChannel(context.Background(), st, f, options())
	if err != nil || result.Records != 1 || f.asked[0] != 2 {
		t.Fatalf("%+v %v asked=%v", result, err, f.asked)
	}
	f.asked = nil
	if _, err := SyncChannel(context.Background(), st, f, options()); err != nil || f.asked[0] != 1 {
		t.Fatalf("%v asked=%v", err, f.asked)
	}
}

func TestRepeatSyncIsIdempotent(t *testing.T) {
	st := open(t)
	f := &fakePublic{t: t, pages: map[int]dzen.ChannelPage{1: page([2]string{"one", "card"}), 2: page([2]string{"two", "card"})}}
	if _, err := SyncChannel(context.Background(), st, f, options()); err != nil {
		t.Fatal(err)
	}
	f.fetched = nil
	result, err := SyncChannel(context.Background(), st, f, options())
	if err != nil || result.Records != 0 || len(f.fetched) != 0 {
		t.Fatalf("%+v %v %v", result, err, f.fetched)
	}
	status, _ := st.Status(context.Background())
	if status.Articles != 2 {
		t.Fatalf("%+v", status)
	}
}

type fakeMine struct {
	byState map[string][]dzen.Publication
	failing string
}

func (f *fakeMine) ListPublications(_ context.Context, _, state string, _ int) ([]dzen.Publication, error) {
	return f.byState[state], nil
}

func (f *fakeMine) GetPublication(_ context.Context, _, id string) (dzen.Publication, error) {
	if id == f.failing {
		return dzen.Publication{}, errors.New("gone")
	}
	return dzen.Publication{ID: id, PublisherID: "p", Title: "T " + id, Version: 4, ContentState: `{"blocks":[],"entityMap":{}}`, CommonURL: ""}, nil
}

func TestSyncMineArchivesEveryStateAndKeepsRevisions(t *testing.T) {
	st := open(t)
	f := &fakeMine{failing: "bad", byState: map[string][]dzen.Publication{"draft": {{ID: "d"}}, "pending": {{ID: "p"}}, "published": {{ID: "x"}, {ID: "bad"}}}}
	result, err := SyncMine(context.Background(), st, f, MineOptions{PublisherID: "p", Now: time.Now, Log: io.Discard})
	if err != nil || result.Records != 3 || !result.Degraded() {
		t.Fatalf("%+v %v", result, err)
	}
	var states, revisions int
	_ = st.DB().QueryRow(`select count(distinct state) from articles`).Scan(&states)
	_ = st.DB().QueryRow(`select count(*) from publication_revisions`).Scan(&revisions)
	if states != 3 || revisions != 3 {
		t.Fatal(states, revisions)
	}
}
