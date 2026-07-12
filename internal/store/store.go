package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	crawlstore "github.com/openclaw/crawlkit/store"
	"github.com/openclaw/crawlkit/vector"
)

type Store struct {
	db   *sql.DB
	path string
}
type Status struct {
	DBPath     string `json:"db_path"`
	DBBytes    int64  `json:"db_bytes"`
	Channels   int    `json:"channels"`
	Articles   int    `json:"articles"`
	Comments   int    `json:"comments"`
	Embeddings int    `json:"embeddings"`
	LastSyncAt int64  `json:"last_sync_at"`
}
type SearchResult struct {
	ID, Title, URL, Snippet string
	Score                   float64
}
type EmbeddingRow struct {
	ArticleID, Title, URL, Model, ContentHash string
	Dimensions                                int
	Vector                                    []float32
}

func Open(path string) (*Store, error) {
	base, err := crawlstore.Open(context.Background(), crawlstore.Options{Path: path})
	if err != nil {
		return nil, err
	}
	s := &Store{db: base.DB(), path: path}
	if err := s.init(context.Background()); err != nil {
		s.db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) init(ctx context.Context) error {
	for _, q := range []string{
		`pragma foreign_keys=on`, `pragma journal_mode=wal`, `pragma synchronous=normal`, `pragma busy_timeout=5000`,
		`create table if not exists channels (id text primary key,title text,url text not null,synced_at integer not null)`,
		`create table if not exists articles (id text primary key,channel_id text not null,publisher_id text,title text not null,url text not null unique,markdown text not null,published_at integer,modified_at integer,views integer not null default 0,views_till_end integer not null default 0,time_to_read_seconds integer not null default 0,likes integer not null default 0,comments_count integer not null default 0,comments_complete integer not null default 0,raw_json text,synced_at integer not null)`,
		`create index if not exists articles_channel_published on articles(channel_id,published_at desc)`,
		`create table if not exists comments (id text primary key,article_id text not null,parent_id text,author_id text,author_name text,text text not null,created_at integer,likes integer not null default 0,dislikes integer not null default 0,raw_json text,synced_at integer not null,foreign key(article_id) references articles(id) on delete cascade)`,
		`create index if not exists comments_article on comments(article_id,created_at)`,
		`create virtual table if not exists article_fts using fts5(article_id unindexed,title,body)`,
		`create virtual table if not exists comment_fts using fts5(comment_id unindexed,article_id unindexed,body)`,
		`create table if not exists embeddings (article_id text primary key,model text not null,dimensions integer not null,content_hash text not null,vector blob not null,embedded_at integer not null,foreign key(article_id) references articles(id) on delete cascade)`,
		`create table if not exists sync_state (key text primary key,value text,synced_at integer not null)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertChannel(ctx context.Context, id, title, url string, now int64) error {
	_, err := s.db.ExecContext(ctx, `insert into channels(id,title,url,synced_at) values(?,?,?,?) on conflict(id) do update set title=excluded.title,url=excluded.url,synced_at=excluded.synced_at`, id, title, url, now)
	return err
}
func (s *Store) HasArticle(ctx context.Context, url string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from articles where url=?`, url).Scan(&n)
	return n > 0, err
}
func (s *Store) UpsertArticle(ctx context.Context, a dzen.Article, comments []dzen.Comment, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `insert into articles(id,channel_id,publisher_id,title,url,markdown,published_at,modified_at,views,views_till_end,time_to_read_seconds,likes,comments_count,comments_complete,raw_json,synced_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set channel_id=excluded.channel_id,publisher_id=excluded.publisher_id,title=excluded.title,url=excluded.url,markdown=excluded.markdown,published_at=excluded.published_at,modified_at=excluded.modified_at,views=excluded.views,views_till_end=excluded.views_till_end,time_to_read_seconds=excluded.time_to_read_seconds,likes=excluded.likes,comments_count=excluded.comments_count,comments_complete=excluded.comments_complete,raw_json=excluded.raw_json,synced_at=excluded.synced_at`, a.ID, a.Channel, a.PublisherID, a.Title, a.URL, a.Markdown, a.PublishedAt, a.ModifiedAt, a.Views, a.ViewsTillEnd, a.TimeToReadSeconds, a.Likes, a.CommentsCount, boolInt(a.CommentsComplete), a.RawJSON, now)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `delete from comments where article_id=?`, a.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `delete from comment_fts where article_id=?`, a.ID); err != nil {
		return err
	}
	for _, c := range comments {
		_, err = tx.ExecContext(ctx, `insert into comments(id,article_id,parent_id,author_id,author_name,text,created_at,likes,dislikes,raw_json,synced_at) values(?,?,?,?,?,?,?,?,?,?,?)`, c.ID, a.ID, c.ParentID, c.AuthorID, c.AuthorName, c.Text, c.CreatedAt, c.Likes, c.Dislikes, c.RawJSON, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into comment_fts(comment_id,article_id,body) values(?,?,?)`, c.ID, a.ID, c.Text)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `delete from article_fts where article_id=?`, a.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into article_fts(article_id,title,body) values(?,?,?)`, a.ID, a.Title, a.Markdown); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into sync_state(key,value,synced_at) values('last_sync',?,?) on conflict(key) do update set value=excluded.value,synced_at=excluded.synced_at`, a.Channel, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Articles(ctx context.Context) ([]dzen.Article, error) {
	rows, err := s.db.QueryContext(ctx, `select id,publisher_id,channel_id,title,url,markdown,published_at,modified_at,views,views_till_end,time_to_read_seconds,likes,comments_count,comments_complete,raw_json from articles order by published_at desc,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dzen.Article
	for rows.Next() {
		var a dzen.Article
		var complete int
		if err := rows.Scan(&a.ID, &a.PublisherID, &a.Channel, &a.Title, &a.URL, &a.Markdown, &a.PublishedAt, &a.ModifiedAt, &a.Views, &a.ViewsTillEnd, &a.TimeToReadSeconds, &a.Likes, &a.CommentsCount, &complete, &a.RawJSON); err != nil {
			return nil, err
		}
		a.CommentsComplete = complete != 0
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) Comments(ctx context.Context, articleID string) ([]dzen.Comment, error) {
	rows, err := s.db.QueryContext(ctx, `select id,article_id,parent_id,author_id,author_name,text,created_at,likes,dislikes,raw_json from comments where article_id=? order by created_at,id`, articleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dzen.Comment
	for rows.Next() {
		var x dzen.Comment
		if err := rows.Scan(&x.ID, &x.ArticleID, &x.ParentID, &x.AuthorID, &x.AuthorName, &x.Text, &x.CreatedAt, &x.Likes, &x.Dislikes, &x.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) Search(ctx context.Context, q string, limit int) ([]SearchResult, error) {
	rows, err := s.db.QueryContext(ctx, `select a.id,a.title,a.url,snippet(article_fts,2,'[',']','...',24) from article_fts join articles a on a.id=article_fts.article_id where article_fts match ? order by bm25(article_fts) limit ?`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchResult
	for rows.Next() {
		var x SearchResult
		if err := rows.Scan(&x.ID, &x.Title, &x.URL, &x.Snippet); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) SaveEmbedding(ctx context.Context, articleID, model, hash string, vec []float32, now int64) error {
	blob, err := vector.EncodeFloat32(vec)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into embeddings(article_id,model,dimensions,content_hash,vector,embedded_at) values(?,?,?,?,?,?) on conflict(article_id) do update set model=excluded.model,dimensions=excluded.dimensions,content_hash=excluded.content_hash,vector=excluded.vector,embedded_at=excluded.embedded_at`, articleID, model, len(vec), hash, blob, now)
	return err
}
func (s *Store) EmbeddingHash(ctx context.Context, id string) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `select content_hash from embeddings where article_id=?`, id).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
}
func (s *Store) Embeddings(ctx context.Context) ([]EmbeddingRow, error) {
	rows, err := s.db.QueryContext(ctx, `select e.article_id,a.title,a.url,e.model,e.dimensions,e.content_hash,e.vector from embeddings e join articles a on a.id=e.article_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbeddingRow
	for rows.Next() {
		var x EmbeddingRow
		var b []byte
		if err := rows.Scan(&x.ArticleID, &x.Title, &x.URL, &x.Model, &x.Dimensions, &x.ContentHash, &b); err != nil {
			return nil, err
		}
		x.Vector, err = vector.DecodeFloat32(b)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) Status(ctx context.Context) (Status, error) {
	x := Status{DBPath: s.path}
	for _, p := range []struct {
		q string
		d *int
	}{{`select count(*) from channels`, &x.Channels}, {`select count(*) from articles`, &x.Articles}, {`select count(*) from comments`, &x.Comments}, {`select count(*) from embeddings`, &x.Embeddings}} {
		if err := s.db.QueryRowContext(ctx, p.q).Scan(p.d); err != nil {
			return x, err
		}
	}
	_ = s.db.QueryRowContext(ctx, `select coalesce(max(synced_at),0) from sync_state`).Scan(&x.LastSyncAt)
	if info, err := os.Stat(s.path); err == nil {
		x.DBBytes = info.Size()
	}
	return x, nil
}
func (s *Store) ReadOnlySQL(ctx context.Context, q string) (*sql.Rows, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(q)), "select") && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(q)), "with") {
		return nil, fmt.Errorf("only SELECT/WITH queries are allowed")
	}
	return s.db.QueryContext(ctx, q)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
