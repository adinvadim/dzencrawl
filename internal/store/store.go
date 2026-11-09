package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/openclaw/crawlkit/state"
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
	LastResult string `json:"last_result"`
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

// migrations run in order, once each. Version 1 is the original schema.
var migrations = []string{
	`create table if not exists channels (id text primary key,title text,url text not null,synced_at integer not null);
	create table if not exists articles (id text primary key,channel_id text not null,publisher_id text,title text not null,url text not null unique,markdown text not null,published_at integer,modified_at integer,views integer not null default 0,views_till_end integer not null default 0,time_to_read_seconds integer not null default 0,likes integer not null default 0,comments_count integer not null default 0,comments_complete integer not null default 0,raw_json text,synced_at integer not null);
	create index if not exists articles_channel_published on articles(channel_id,published_at desc);
	create table if not exists comments (id text primary key,article_id text not null,parent_id text,author_id text,author_name text,text text not null,created_at integer,likes integer not null default 0,dislikes integer not null default 0,raw_json text,synced_at integer not null,foreign key(article_id) references articles(id) on delete cascade);
	create index if not exists comments_article on comments(article_id,created_at);
	create virtual table if not exists article_fts using fts5(article_id unindexed,title,body);
	create virtual table if not exists comment_fts using fts5(comment_id unindexed,article_id unindexed,body);
	create table if not exists embeddings (article_id text primary key,model text not null,dimensions integer not null,content_hash text not null,vector blob not null,embedded_at integer not null,foreign key(article_id) references articles(id) on delete cascade);
	create table if not exists sync_state (key text primary key,value text,synced_at integer not null);`,
	`alter table sync_state rename to archive_sync_state;
	alter table articles add column state text not null default 'published';
	alter table articles add column provider_version integer not null default 0;
	create table if not exists publication_revisions (publication_id text not null,content_sha256 text not null,content_state text not null,provider_version integer not null default 0,title text not null,observed_at integer not null,primary key(publication_id,content_sha256));
	create table if not exists sync_runs (id integer primary key autoincrement,scope text not null,result text not null,records integer not null default 0,warnings integer not null default 0,error text,started_at integer not null,finished_at integer);
	create table if not exists mutation_journal (op_id text primary key,profile text not null,kind text not null,target text not null,body_sha256 text not null,base_version integer not null default 0,status text not null,detail text,created_at integer not null,updated_at integer not null);`,
}

func (s *Store) init(ctx context.Context) error {
	for _, q := range []string{`pragma journal_mode=wal`, `pragma synchronous=normal`, `pragma busy_timeout=5000`, `create table if not exists schema_migrations (version integer primary key,applied_at integer not null)`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for i, migration := range migrations {
		version := i + 1
		var applied int
		if err := s.db.QueryRowContext(ctx, `select count(*) from schema_migrations where version=?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied != 0 {
			continue
		}
		if err := s.applyMigration(ctx, version, migration); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
	}
	return state.EnsureSchema(ctx, s.db)
}

func (s *Store) applyMigration(ctx context.Context, version int, statements string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version == 2 {
		// Archives created by an interrupted upgrade may already hold new names.
		var old int
		if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='sync_state' and sql like '%synced_at%'`).Scan(&old); err != nil {
			return err
		}
		if old == 0 {
			return fmt.Errorf("sync_state table is missing or already migrated")
		}
	}
	if _, err := tx.ExecContext(ctx, statements); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into schema_migrations(version,applied_at) values(?,strftime('%s','now'))`, version); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertChannel(ctx context.Context, id, title, url string, now int64) error {
	_, err := s.db.ExecContext(ctx, `insert into channels(id,title,url,synced_at) values(?,?,?,?) on conflict(id) do update set title=excluded.title,url=excluded.url,synced_at=excluded.synced_at`, id, title, url, now)
	return err
}

// ArticleState reports whether an article is archived and whether its
// comments are complete.
func (s *Store) ArticleState(ctx context.Context, id, url string) (exists, complete bool, err error) {
	var total, full int
	err = s.db.QueryRowContext(ctx, `select count(*),coalesce(sum(comments_complete),0) from articles where id=? or url=?`, id, url).Scan(&total, &full)
	return total > 0, full > 0, err
}

// UpsertArticle stores an article. Existing comments are replaced only when
// the new set is complete; a partial fetch merges and keeps what was archived.
func (s *Store) UpsertArticle(ctx context.Context, a dzen.Article, comments []dzen.Comment, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `insert into articles(id,channel_id,publisher_id,title,url,markdown,published_at,modified_at,views,views_till_end,time_to_read_seconds,likes,comments_count,comments_complete,raw_json,synced_at,state,provider_version) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set state=excluded.state,provider_version=excluded.provider_version,channel_id=excluded.channel_id,publisher_id=excluded.publisher_id,title=excluded.title,url=excluded.url,markdown=excluded.markdown,published_at=excluded.published_at,modified_at=excluded.modified_at,views=excluded.views,views_till_end=excluded.views_till_end,time_to_read_seconds=excluded.time_to_read_seconds,likes=excluded.likes,comments_count=excluded.comments_count,comments_complete=excluded.comments_complete,raw_json=excluded.raw_json,synced_at=excluded.synced_at`, a.ID, a.Channel, a.PublisherID, a.Title, a.URL, a.Markdown, a.PublishedAt, a.ModifiedAt, a.Views, a.ViewsTillEnd, a.TimeToReadSeconds, a.Likes, a.CommentsCount, boolInt(a.CommentsComplete), a.RawJSON, now, stateOrDefault(a.State), a.ProviderVersion)
	if err != nil {
		return err
	}
	if a.CommentsComplete {
		if _, err = tx.ExecContext(ctx, `delete from comments where article_id=?`, a.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `delete from comment_fts where article_id=?`, a.ID); err != nil {
			return err
		}
	}
	for _, c := range comments {
		_, err = tx.ExecContext(ctx, `insert into comments(id,article_id,parent_id,author_id,author_name,text,created_at,likes,dislikes,raw_json,synced_at) values(?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set article_id=excluded.article_id,parent_id=excluded.parent_id,author_id=excluded.author_id,author_name=excluded.author_name,text=excluded.text,created_at=excluded.created_at,likes=excluded.likes,dislikes=excluded.dislikes,raw_json=excluded.raw_json,synced_at=excluded.synced_at`, c.ID, a.ID, c.ParentID, c.AuthorID, c.AuthorName, c.Text, c.CreatedAt, c.Likes, c.Dislikes, c.RawJSON, now)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `delete from comment_fts where comment_id=?`, c.ID); err != nil {
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
	_, err = tx.ExecContext(ctx, `insert into archive_sync_state(key,value,synced_at) values('last_sync',?,?) on conflict(key) do update set value=excluded.value,synced_at=excluded.synced_at`, a.Channel, now)
	if err != nil {
		return err
	}
	if a.ContentState != "" {
		sum := sha256.Sum256([]byte(a.ContentState))
		_, err = tx.ExecContext(ctx, `insert into publication_revisions(publication_id,content_sha256,content_state,provider_version,title,observed_at) values(?,?,?,?,?,?) on conflict do nothing`, a.ID, hex.EncodeToString(sum[:]), a.ContentState, a.ProviderVersion, a.Title, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Checkpoints exposes crawlkit's per-collection state store.
func (s *Store) Checkpoints() *state.Store { return state.New(s.db) }

// BeginRun records the start of a sync and returns its id.
func (s *Store) BeginRun(ctx context.Context, scope string, now int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `insert into sync_runs(scope,result,started_at) values(?, 'running', ?)`, scope, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun records a sync outcome: ok, degraded or failed.
func (s *Store) FinishRun(ctx context.Context, id int64, result string, records, warnings int, cause string, now int64) error {
	_, err := s.db.ExecContext(ctx, `update sync_runs set result=?,records=?,warnings=?,error=?,finished_at=? where id=?`, result, records, warnings, cause, now, id)
	return err
}

// JournalEntry is one mutation intent, recorded before the request is sent.
type JournalEntry struct {
	OpID, Profile, Kind, Target, BodySHA256 string
	BaseVersion                             int64
}

// JournalBegin records an intent as dispatched-pending.
func (s *Store) JournalBegin(ctx context.Context, e JournalEntry, now int64) error {
	_, err := s.db.ExecContext(ctx, `insert into mutation_journal(op_id,profile,kind,target,body_sha256,base_version,status,created_at,updated_at) values(?,?,?,?,?,?,'planned',?,?)`, e.OpID, e.Profile, e.Kind, e.Target, e.BodySHA256, e.BaseVersion, now, now)
	return err
}

// JournalFinish records the final outcome of a mutation.
func (s *Store) JournalFinish(ctx context.Context, opID, status, detail string, now int64) error {
	_, err := s.db.ExecContext(ctx, `update mutation_journal set status=?,detail=?,updated_at=? where op_id=?`, status, detail, now, opID)
	return err
}

func (s *Store) Articles(ctx context.Context) ([]dzen.Article, error) {
	rows, err := s.db.QueryContext(ctx, `select id,coalesce(publisher_id,''),channel_id,title,url,markdown,coalesce(published_at,0),coalesce(modified_at,0),views,views_till_end,time_to_read_seconds,likes,comments_count,comments_complete,coalesce(raw_json,'') from articles order by published_at desc,id`)
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
	rows, err := s.db.QueryContext(ctx, `select id,article_id,coalesce(parent_id,''),coalesce(author_id,''),coalesce(author_name,''),text,coalesce(created_at,0),likes,dislikes,coalesce(raw_json,'') from comments where article_id=? order by created_at,id`, articleID)
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
	match := crawlstore.FTS5TokenQuery(q)
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `select a.id,a.title,a.url,snippet(article_fts,2,'[',']','...',24) from article_fts join articles a on a.id=article_fts.article_id where article_fts match ? order by bm25(article_fts) limit ?`, match, limit)
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
	_ = s.db.QueryRowContext(ctx, `select coalesce(max(synced_at),0) from archive_sync_state`).Scan(&x.LastSyncAt)
	_ = s.db.QueryRowContext(ctx, `select result from sync_runs order by id desc limit 1`).Scan(&x.LastResult)
	if info, err := os.Stat(s.path); err == nil {
		x.DBBytes = info.Size()
	}
	return x, nil
}

// ReadOnlySQL runs a query on a connection SQLite itself keeps read-only, so a
// statement that writes fails however it is spelled.
func (s *Store) ReadOnlySQL(ctx context.Context, q string) (*sql.Rows, func() error, error) {
	ro, err := crawlstore.OpenReadOnly(ctx, s.path)
	if err != nil {
		return nil, nil, err
	}
	rows, err := ro.DB().QueryContext(ctx, q)
	if err != nil {
		ro.Close()
		return nil, nil, err
	}
	return rows, ro.Close, nil
}
func stateOrDefault(v string) string {
	if v == "" {
		return "published"
	}
	return v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
