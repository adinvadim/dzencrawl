package store

import (
	"context"
	"path/filepath"
	"testing"

	crawlstore "github.com/openclaw/crawlkit/store"

	"github.com/adinvadim/dzencrawl/internal/dzen"
)

func TestArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := dzen.Article{ID: "a1", Channel: "channel", PublisherID: "p1", Title: "Модный гардероб", URL: "https://dzen.ru/a/a1", Markdown: "Базовый текст", Views: 10, CommentsCount: 1, CommentsComplete: true}
	comments := []dzen.Comment{{ID: "c1", ArticleID: "a1", AuthorName: "Анна", Text: "Полезный комментарий"}}
	if err := st.UpsertChannel(ctx, "channel", "Канал", "https://dzen.ru/channel", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertArticle(ctx, a, comments, 2); err != nil {
		t.Fatal(err)
	}
	results, err := st.Search(ctx, "Базовый", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "a1" {
		t.Fatalf("results=%+v", results)
	}
	if err := st.SaveEmbedding(ctx, "a1", "test", "hash", []float32{1, 2, 3}, 3); err != nil {
		t.Fatal(err)
	}
	embeddings, err := st.Embeddings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddings) != 1 || len(embeddings[0].Vector) != 3 {
		t.Fatalf("embeddings=%+v", embeddings)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Articles != 1 || status.Comments != 1 || status.Embeddings != 1 {
		t.Fatalf("status=%+v", status)
	}
}

func TestPartialCommentFetchKeepsArchivedComments(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := dzen.Article{ID: "a1", Channel: "c", Title: "t", URL: "u", Markdown: "m", CommentsComplete: true}
	full := []dzen.Comment{{ID: "c1", ArticleID: "a1", Text: "one"}, {ID: "c2", ArticleID: "a1", Text: "two"}}
	if err := st.UpsertArticle(ctx, a, full, 1); err != nil {
		t.Fatal(err)
	}
	a.CommentsComplete = false
	if err := st.UpsertArticle(ctx, a, []dzen.Comment{{ID: "c1", ArticleID: "a1", Text: "one edited"}}, 2); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Comments(ctx, "a1")
	if len(got) != 2 || got[0].Text != "one edited" {
		t.Fatalf("%+v", got)
	}
	a.CommentsComplete = true
	if err := st.UpsertArticle(ctx, a, []dzen.Comment{{ID: "c1", ArticleID: "a1", Text: "only"}}, 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Comments(ctx, "a1"); len(got) != 1 {
		t.Fatalf("complete fetch did not replace: %+v", got)
	}
}

func TestReadOnlySQLRejectsEveryWriteSpelling(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertArticle(ctx, dzen.Article{ID: "a", Channel: "c", Title: "t", URL: "u", Markdown: "m"}, nil, 1); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"delete from articles", "with x as (select 1) delete from articles", "select 1; delete from articles"} {
		if rows, closer, err := st.ReadOnlySQL(ctx, q); err == nil {
			for rows.Next() {
			}
			rows.Close()
			closer()
		}
	}
	status, _ := st.Status(ctx)
	if status.Articles != 1 {
		t.Fatal("a write statement deleted archived data")
	}
}

func TestSearchAcceptsArbitraryUserText(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, q := range []string{"foo-bar", "foo, bar", "c++", `"незакрытая`} {
		if _, err := st.Search(ctx, q, 5); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
}

func TestLegacySchemaMigratesInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	base, err := crawlstore.Open(ctx, crawlstore.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`create table channels (id text primary key,title text,url text not null,synced_at integer not null)`,
		`create table articles (id text primary key,channel_id text not null,publisher_id text,title text not null,url text not null unique,markdown text not null,published_at integer,modified_at integer,views integer not null default 0,views_till_end integer not null default 0,time_to_read_seconds integer not null default 0,likes integer not null default 0,comments_count integer not null default 0,comments_complete integer not null default 0,raw_json text,synced_at integer not null)`,
		`insert into articles(id,channel_id,title,url,markdown,synced_at) values('old','c','t','u','m',1)`,
		`create table sync_state (key text primary key,value text,synced_at integer not null)`,
		`insert into sync_state values('last_sync','c',77)`,
	} {
		if _, err := base.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	base.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	status, err := st.Status(ctx)
	if err != nil || status.Articles != 1 || status.LastSyncAt != 77 {
		t.Fatalf("%+v %v", status, err)
	}
	got, err := st.Articles(ctx)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if err := st.Checkpoints().Set(ctx, "public", "feed", "c", `{"page":2}`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err = Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
}

func TestJournalRecordsIntentAndOutcome(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.JournalBegin(ctx, JournalEntry{OpID: "op", Profile: "p", Kind: "save", Target: "t", BodySHA256: "h", BaseVersion: 3}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.JournalFinish(ctx, "op", "outcome_unknown", "lost", 2); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := st.DB().QueryRow(`select status from mutation_journal where op_id='op'`).Scan(&status); err != nil || status != "outcome_unknown" {
		t.Fatal(status, err)
	}
}
