package store

import (
	"context"
	"path/filepath"
	"testing"

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
