package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/article"
	"github.com/adinvadim/dzencrawl/internal/captcha"
	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/mutate"
	"github.com/adinvadim/dzencrawl/internal/session"
	"github.com/adinvadim/dzencrawl/internal/studio"
)

var commands []command

func init() {
	commands = []command{
		{"push", "<file>", "Create or update a draft from a Markdown file. Rewrites the file to canonical form. An open browser editor can still overwrite the result.", []string{"dry-run", "force", "yes", "import-remote"}, runPush},
		{"pull", "<id|url>", "Download an article into a working copy.", []string{"dir", "force"}, runPull},
		{"diff", "<file>", "Show local edits against the snapshot, or with --remote, server changes.", []string{"remote"}, runDiff},
		{"status", "[file|id]", "Show the working copy and server state.", nil, runStatus},
		{"publish", "<file|id>", "Publish now, or schedule with --at. Without --at a scheduled article is published immediately; Dzen cannot return it to drafts.", []string{"at", "wait", "dry-run", "force", "yes", "import-remote"}, runPublish},
		{"draft list", "", "List drafts (live).", []string{"limit"}, listState("draft")},
		{"draft show", "<id>", "Show a draft.", []string{"format"}, runShow},
		{"draft delete", "<id>", "Delete a draft permanently.", []string{"yes", "dry-run"}, runDelete},
		{"article list", "", "List published or scheduled articles (--state published|pending).", []string{"limit", "state"}, runArticleList},
		{"article show", "<id|url>", "Show an article.", []string{"format"}, runShow},
		{"article discard-changes", "<id>", "Drop unpublished edits of a published article.", []string{"yes", "dry-run"}, runDiscard},
		{"article delete", "<id>", "Delete an article permanently. Dzen has no unpublish.", []string{"yes", "dry-run"}, runDelete},
		{"image upload", "<file|url>", "Upload an image to a publication and print its id.", []string{"to", "dry-run"}, runImage},
		{"comment list", "[article]", "List comments of an article, or with --channel the channel feed.", []string{"channel", "sort", "limit", "cursor"}, runCommentList},
		{"comment show", "<comment_id>", "Show one comment.", []string{"article"}, runCommentShow},
		{"comment post", "<article>", "Post a comment.", []string{"text", "stdin", "dry-run"}, runCommentPost},
		{"comment reply", "<comment_id>", "Reply to a comment.", []string{"article", "text", "stdin", "dry-run"}, runCommentReply},
		{"comment edit", "<comment_id>", "Edit your comment, within one hour.", []string{"article", "text", "stdin", "dry-run"}, runCommentEdit},
		{"comment delete", "<comment_id>", "Delete a comment.", []string{"article", "yes", "dry-run"}, commentAction("delete")},
		{"comment restore", "<comment_id>", "Restore a deleted comment.", []string{"article", "dry-run"}, commentAction("restore")},
		{"comment pin", "<comment_id>", "Pin a comment.", []string{"article", "dry-run"}, commentAction("pin")},
		{"comment unpin", "<comment_id>", "Unpin a comment.", []string{"article", "dry-run"}, commentAction("unpin")},
		{"comment react", "<comment_id>", "Set a reaction: like, dislike, cancel_*.", []string{"article", "reaction", "dry-run"}, runReact},
		{"comment report", "<comment_id>", "Report a comment: spam_or_fraud, obscene, inappropriate.", []string{"article", "reason", "dry-run"}, runReport},
		{"comment ban", "<uid>", "Ban a reader from the channel.", []string{"article", "delete-all", "yes", "dry-run"}, runBan},
		{"comment unban", "<uid>", "Lift a ban.", []string{"article", "dry-run"}, runUnban},
		{"comment banned", "", "List banned readers.", []string{"limit"}, runBanned},
		{"auth import", "", "Take an already signed-in session from Chrome.", []string{"browser", "browser-profile", "all"}, runAuthImport},
		{"auth login", "", "Sign in through a dedicated browser window.", nil, runAuthLogin},
		{"auth list", "", "List accounts.", nil, runAuthList},
		{"auth use", "<profile>", "Select the default account.", nil, runAuthUse},
		{"auth status", "", "Show the account; --live verifies it with Dzen.", []string{"live"}, runAuthStatus},
		{"auth logout", "<profile>", "Forget an account.", nil, runAuthLogout},
		{"crawl sync", "<channel>", "Archive a public channel, or with --mine your own articles.", []string{"max-articles", "refresh", "mine", "db"}, runCrawlSync},
		{"crawl status", "", "Show archive counts.", []string{"db"}, runCrawlStatus},
		{"crawl search", "<query>", "Full-text search the archive.", []string{"limit", "db"}, runCrawlSearch},
		{"crawl semantic-search", "<query>", "Vector search the archive.", []string{"db"}, runCrawlSemantic},
		{"crawl embed", "", "Create or update embeddings.", []string{"force", "db"}, runCrawlEmbed},
		{"crawl export-md", "", "Export the archive as Markdown files.", []string{"db"}, runCrawlExport},
		{"crawl sql", "<select>", "Run a read-only SQL query.", []string{"db"}, runCrawlSQL},
		{"init", "", "Write a starter config.", nil, runInit},
		{"doctor", "", "Check the configuration and, with an account, the Dzen contract.", nil, runDoctor},
		{"metadata", "", "Print the crawlkit control manifest.", nil, runMetadata},
		{"version", "", "Print the version.", nil, func(c *runContext) error { fmt.Fprintln(c.app.Stdout, c.app.Version); return nil }},
		{"completion", "<bash|zsh|fish>", "Print a shell completion script.", nil, runCompletion},
	}
}

func (c *runContext) sessions() session.Store { return session.Store{Dir: c.cfg.SessionsDir} }

func (c *runContext) studioClient(sess session.Session) (*studio.Client, error) {
	delay, _ := time.ParseDuration(c.cfg.Dzen.StudioDelay)
	ua := sess.UserAgent
	if ua == "" {
		ua = session.DefaultUserAgent
	}
	client := studio.New(session.Header(sess.Cookies), ua, delay)
	if c.app.BaseURL != "" {
		client.Base = c.app.BaseURL
	}
	client.Sleep = c.app.Sleep
	return client, nil
}

func (c *runContext) loadSession() (session.Session, error) {
	store := c.sessions()
	name := c.value("profile")
	if name == "" {
		name = strings.TrimSpace(os.Getenv("DZEN_PROFILE"))
	}
	if name == "" {
		var err error
		if name, err = store.Current(); err != nil {
			return session.Session{}, err
		}
	}
	c.profile = name
	return store.Load(name)
}

type account struct {
	sess    session.Session
	client  *studio.Client
	service *mutate.Service
}

func (c *runContext) buildAccount(sess session.Session) (*account, error) {
	client, err := c.studioClient(sess)
	if err != nil {
		return nil, err
	}
	st, err := c.store()
	if err != nil {
		return nil, err
	}
	runner := &mutate.Runner{Client: client, Journal: st, Profile: sess.Profile, Pauses: mutate.DefaultPauses, Sleep: c.app.Sleep, Now: c.app.Now}
	if !c.has("no-captcha") {
		runner.Captcha = captcha.New(c.cfg.Captcha.CredentialProcess)
	}
	return &account{sess: sess, client: client, service: &mutate.Service{Reader: client, Runner: runner, PublisherID: sess.PublisherID, Rid: randomRid, Sleep: c.app.Sleep, Now: c.app.Now}}, nil
}

// withAccount runs fn with the session. An expired imported session is taken
// again from Chrome once; only the rejected request is repeated.
func (c *runContext) withAccount(fn func(*account) error) error {
	sess, err := c.loadSession()
	if err != nil {
		return err
	}
	acc, err := c.buildAccount(sess)
	if err != nil {
		return err
	}
	if sess.Source.Kind == "chrome" {
		acc.client.RefreshSession = func(context.Context) (string, error) {
			refreshed, err := c.reimport(sess)
			if err != nil {
				return "", err
			}
			sess = refreshed
			return session.Header(refreshed.Cookies), nil
		}
	}
	return fn(acc)
}

// eachCandidate offers sessions from cheapest to costliest, stopping as soon
// as visit accepts one. CDP needs no permission; only if it yields nothing is
// the cookie database read, which may ask for Keychain access.
func (c *runContext) eachCandidate(opts session.ImportOptions, previous session.Source, visit func(session.Candidate) bool) error {
	if c.app.Importer != nil {
		candidates, err := c.app.Importer(c.ctx, opts, previous)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if visit(candidate) {
				return nil
			}
		}
		return nil
	}
	if opts.BrowserProfile == "" {
		if candidate, err := session.FromCDP(c.ctx, opts.Root); err == nil && visit(candidate) {
			return nil
		}
	}
	profiles := []string{opts.BrowserProfile}
	if opts.BrowserProfile == "" {
		var err error
		if profiles, err = session.BrowserProfiles(opts.Root); err != nil {
			return err
		}
	}
	for _, profile := range profiles {
		candidate, err := session.FromDatabase(c.ctx, opts, profile)
		if err != nil {
			return err
		}
		if visit(candidate) {
			return nil
		}
	}
	return nil
}

func (c *runContext) verifyCandidate(candidate session.Candidate) (studio.User, []session.Cookie, error) {
	compact := session.Compact(candidate.Cookies)
	if len(compact) == 0 {
		return studio.User{}, nil, apperr.New("not_authenticated", "no Dzen cookies were found")
	}
	client, err := c.studioClient(session.Session{Cookies: compact, UserAgent: candidate.UserAgent})
	if err != nil {
		return studio.User{}, nil, err
	}
	user, err := client.User(c.ctx)
	return user, compact, err
}

func (c *runContext) reimport(previous session.Session) (session.Session, error) {
	root, err := session.ChromeRoot()
	if err != nil {
		return previous, err
	}
	var found *session.Session
	var mismatch error
	err = c.eachCandidate(session.ImportOptions{Root: root, BrowserProfile: previous.Source.BrowserProfile, NoInput: true}, previous.Source, func(candidate session.Candidate) bool {
		user, compact, verifyErr := c.verifyCandidate(candidate)
		if verifyErr != nil {
			return false
		}
		if user.Login != previous.Login {
			mismatch = apperr.New("principal_mismatch", "Chrome is now signed in to a different account")
			return false
		}
		next := previous
		next.Cookies, next.SavedAt = compact, c.app.Now()
		found = &next
		return true
	})
	if found == nil {
		if mismatch != nil {
			return previous, mismatch
		}
		if err != nil {
			return previous, err
		}
		return previous, apperr.New("session_expired", "Chrome has no valid session for this account")
	}
	return *found, c.sessions().Save(*found)
}

func randomRid() string {
	buf := make([]byte, 7)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (c *runContext) readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, apperr.New("usage_error", "cannot read %s", path)
	}
	return data, nil
}

func pushHuman(result mutate.PushResult) func(io.Writer) {
	return func(w io.Writer) {
		for _, change := range result.Changes {
			fmt.Fprintf(w, "%s\t%s\n", change.Op, change.Detail)
		}
		for _, upload := range result.Uploads {
			fmt.Fprintf(w, "upload\t%s\n", upload)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(w, "warning\t%s\n", warning)
		}
		if !result.Changed && result.Outcome == "confirmed" {
			fmt.Fprintln(w, "no changes")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\tversion=%d\n", result.Outcome, result.PublicationID, result.Status, result.Version)
		if result.URL != "" {
			fmt.Fprintln(w, result.URL)
		}
	}
}

func pushOptions(c *runContext) mutate.PushOptions {
	return mutate.PushOptions{Force: c.has("force"), Yes: c.has("yes"), ImportRemote: c.has("import-remote")}
}

func runPush(c *runContext) error {
	if err := c.need(1, "push <file>"); err != nil {
		return err
	}
	file := c.args[0]
	src, err := c.readFile(file)
	if err != nil {
		return err
	}
	opts := pushOptions(c)
	if c.has("dry-run") {
		plan, err := mutate.Plan(file, src, opts)
		if err != nil {
			return err
		}
		return c.emit(plan.Outcome, plan, pushHuman(plan))
	}
	if _, err := mutate.Validated(src); err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		opts.Yes = opts.Yes || (publishedTarget(acc, file, src) && c.confirm("Saving removes this published article from the feed until the edits are published. Continue?"))
		result, err := acc.service.Push(c.ctx, file, src, opts)
		c.journal = result.JournalID
		if err != nil {
			return err
		}
		return c.emit(result.Outcome, result, pushHuman(result))
	})
}

func publishedTarget(acc *account, file string, src []byte) bool {
	doc, err := mutate.Validated(src)
	if err != nil || doc.Front.Dzen == nil {
		return false
	}
	return doc.Front.Dzen.Status == "published"
}

func runPublish(c *runContext) error {
	if err := c.need(1, "publish <file|id>"); err != nil {
		return err
	}
	var opts mutate.PublishOptions
	opts.PushOptions = pushOptions(c)
	if raw := c.value("at"); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return apperr.New("usage_error", "--at must be RFC 3339, for example 2026-11-20T09:00:00+03:00")
		}
		if _, err := mutate.ScheduleWindow(at, c.app.Now()); err != nil {
			return err
		}
		opts.At = &at
	}
	if raw := c.value("wait"); raw != "" {
		wait, err := time.ParseDuration(raw)
		if err != nil {
			return apperr.New("usage_error", "--wait must be a duration such as 5m")
		}
		opts.Wait = wait
	}
	target := c.args[0]
	_, statErr := os.Stat(target)
	isFile := statErr == nil
	var src []byte
	if isFile {
		var err error
		if src, err = c.readFile(target); err != nil {
			return err
		}
	} else if !hexID.MatchString(target) {
		return apperr.New("usage_error", "%s is neither a file nor a publication id", target)
	}
	if c.has("dry-run") {
		if !isFile {
			return c.emit("dry_run_ok", map[string]any{"publication_id": target}, func(w io.Writer) { fmt.Fprintln(w, "would publish", target) })
		}
		plan, err := mutate.Plan(target, src, opts.PushOptions)
		if err != nil {
			return err
		}
		return c.emit(plan.Outcome, plan, pushHuman(plan))
	}
	if isFile {
		if _, err := mutate.Validated(src); err != nil {
			return err
		}
	}
	opts.Yes = c.confirm("Publish this article?")
	return c.withAccount(func(acc *account) error {
		var result mutate.PushResult
		var err error
		if isFile {
			result, err = acc.service.Publish(c.ctx, target, src, opts)
		} else {
			result, err = acc.service.PublishExisting(c.ctx, target, opts)
		}
		c.journal = result.JournalID
		if err != nil {
			return err
		}
		return c.emit(result.Outcome, result, pushHuman(result))
	})
}

var (
	hexID     = regexp.MustCompile(`^[0-9a-f]{24}$`)
	hexSuffix = regexp.MustCompile(`([0-9a-f]{24})(?:/edit)?/?$`)
	urlSlug   = regexp.MustCompile(`/a/([A-Za-z0-9_-]+)`)
)

// publicationID accepts a hex id, a studio URL or a public article URL, whose
// slug is the base64url encoding of the id.
func publicationID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if hexID.MatchString(raw) {
		return raw, nil
	}
	clean := strings.SplitN(strings.SplitN(raw, "?", 2)[0], "#", 2)[0]
	if match := hexSuffix.FindStringSubmatch(clean); match != nil {
		return match[1], nil
	}
	if match := urlSlug.FindStringSubmatch(clean); match != nil {
		if decoded, err := base64.RawURLEncoding.DecodeString(match[1]); err == nil && len(decoded) == 12 {
			return hex.EncodeToString(decoded), nil
		}
	}
	return "", apperr.New("usage_error", "cannot find a publication id in %q", raw)
}

var fileUnsafe = regexp.MustCompile(`[^\pL\pN._-]+`)

func slugFile(p dzen.Publication) string {
	name := strings.Trim(fileUnsafe.ReplaceAllString(strings.ToLower(p.Title), "-"), "-")
	if runes := []rune(name); len(runes) > 60 {
		name = strings.Trim(string(runes[:60]), "-")
	}
	if name == "" {
		name = p.ID
	}
	return name + ".md"
}

func runPull(c *runContext) error {
	if err := c.need(1, "pull <id|url>"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	dir := c.value("dir")
	if dir == "" {
		dir = "."
	}
	return c.withAccount(func(acc *account) error {
		p, err := acc.client.GetPublication(c.ctx, acc.sess.PublisherID, id)
		if err != nil {
			return err
		}
		if p.Type != "article" {
			return apperr.New("usage_error", "only articles are supported, this is a %s", p.Type)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		file := filepath.Join(dir, slugFile(p))
		if !c.has("force") {
			if err := refuseLocalEdits(file, id); err != nil {
				return err
			}
		}
		uploads := map[string]string{}
		if snap, err := article.LoadSnapshot(article.SnapshotPath(file, id)); err == nil {
			uploads = snap.Uploads
		}
		if err := mutate.WriteWorkingCopy(file, p, uploads, false); err != nil {
			return err
		}
		return c.emit("confirmed", map[string]any{"file": file, "publication_id": id, "version": p.Version, "status": p.Status}, func(w io.Writer) { fmt.Fprintln(w, file) })
	})
}

// refuseLocalEdits protects a working copy with unsent changes.
func refuseLocalEdits(file, id string) error {
	src, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	plan, err := mutate.Plan(file, src, mutate.PushOptions{ImportRemote: true})
	if err != nil || plan.Changed {
		return apperr.New("revision_conflict", "%s has local edits; push them or pass --force", file)
	}
	if plan.PublicationID != "" && plan.PublicationID != id {
		return apperr.New("revision_conflict", "%s belongs to another publication", file)
	}
	return nil
}

func runDiff(c *runContext) error {
	if err := c.need(1, "diff <file>"); err != nil {
		return err
	}
	file := c.args[0]
	src, err := c.readFile(file)
	if err != nil {
		return err
	}
	doc, err := mutate.Validated(src)
	if err != nil {
		return err
	}
	if doc.Front.Dzen == nil || doc.Front.Dzen.PublicationID == "" {
		return apperr.New("usage_error", "%s has not been pushed yet", file)
	}
	snap, err := article.LoadSnapshot(article.SnapshotPath(file, doc.Front.Dzen.PublicationID))
	if err != nil {
		return apperr.New("usage_error", "snapshot is missing; run: dzen pull %s", doc.Front.Dzen.PublicationID)
	}
	baseBody, err := snapshotBody(snap)
	if err != nil {
		return err
	}
	baseText := settingsText(snap.Settings, coverURL(snap.Images, snap.Settings.CoverID)) + baseBody
	if c.has("remote") {
		return c.withAccount(func(acc *account) error {
			p, err := acc.client.GetPublication(c.ctx, snap.PublisherID, snap.PublicationID)
			if err != nil {
				return err
			}
			_, remoteBody, err := mutate.Render(p)
			if err != nil {
				return err
			}
			text := unifiedDiff("base", "remote", baseText, settingsText(mutate.SettingsOf(p), coverURL(p.Images, p.CoverID))+remoteBody)
			return c.emit("confirmed", map[string]any{"diff": text, "base_version": snap.Version, "remote_version": p.Version}, func(w io.Writer) { fmt.Fprint(w, text) })
		})
	}
	localCover := doc.Front.Cover
	if localCover == "" {
		localCover = coverURL(snap.Images, snap.Settings.CoverID)
	}
	text := unifiedDiff("base", file, baseText, settingsText(mutate.MergeSettings(doc.Front, snap.Settings), localCover)+article.RenderBody(doc.Nodes))
	return c.emit("confirmed", map[string]any{"diff": text}, func(w io.Writer) { fmt.Fprint(w, text) })
}

func coverURL(images article.Images, id string) string {
	if info, ok := images[id]; ok && info.GroupID != 0 {
		return info.URL()
	}
	return ""
}

// settingsText shows the fields outside the body as diffable lines.
func settingsText(s article.Settings, cover string) string {
	return fmt.Sprintf("title: %s\ncover: %s\nsnippet: %s (frozen: %v)\ncomments: %s\ncomments_visibility: %s\ncomments_title: %s\nnative_ad: %v\n\n", s.Title, cover, s.Snippet, s.SnippetFrozen, s.Comments, s.Visibility, s.CommentsTitle, s.NativeAds)
}

func snapshotBody(snap article.Snapshot) (string, error) {
	state, err := article.ParseContentState(snap.ContentState)
	if err != nil {
		return "", apperr.New("contract_drift", "snapshot content changed shape")
	}
	var nodes []article.Node
	for _, b := range state.Blocks {
		if b.Type != "marker" {
			nodes = append(nodes, article.NodeFromBlock(b, state.EntityMap, snap.Images))
		}
	}
	return article.RenderBody(nodes), nil
}

// maxDiffCells bounds the diff table; a larger changed region prints whole.
const maxDiffCells = 40_000_000

// unifiedDiff returns the changed lines, without context lines.
func unifiedDiff(fromName, toName, from, to string) string {
	if from == to {
		return ""
	}
	a, b := strings.Split(from, "\n"), strings.Split(to, "\n")
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	if len(a)*len(b) > maxDiffCells {
		var whole strings.Builder
		fmt.Fprintf(&whole, "--- %s\n+++ %s\n", fromName, toName)
		for _, line := range a {
			whole.WriteString("-" + line + "\n")
		}
		for _, line := range b {
			whole.WriteString("+" + line + "\n")
		}
		return whole.String()
	}
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
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromName, toName)
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i++
			j++
		case j < len(b) && (i == len(a) || table[i][j+1] >= table[i+1][j]):
			out.WriteString("+" + b[j] + "\n")
			j++
		default:
			out.WriteString("-" + a[i] + "\n")
			i++
		}
	}
	return out.String()
}

func runStatus(c *runContext) error {
	if len(c.args) > 1 {
		return apperr.New("usage_error", "usage: dzen status [file|id]")
	}
	if len(c.args) == 0 {
		return c.withAccount(func(acc *account) error {
			drafts, err := acc.client.ListPublications(c.ctx, acc.sess.PublisherID, "draft", 1000)
			if err != nil {
				return err
			}
			pending, err := acc.client.ListPublications(c.ctx, acc.sess.PublisherID, "pending", 1000)
			if err != nil {
				return err
			}
			data := map[string]any{"login": acc.sess.Login, "publisher_id": acc.sess.PublisherID, "drafts": len(drafts), "pending": len(pending)}
			return c.emit("confirmed", data, func(w io.Writer) {
				fmt.Fprintf(w, "%s\tdrafts=%d\tpending=%d\n", acc.sess.Login, len(drafts), len(pending))
			})
		})
	}
	target := c.args[0]
	if src, err := os.ReadFile(target); err == nil {
		return statusFile(c, target, src)
	}
	id, err := publicationID(target)
	if err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		p, err := acc.client.GetPublication(c.ctx, acc.sess.PublisherID, id)
		if err != nil {
			return err
		}
		return c.emit("confirmed", publicationSummary(p), func(w io.Writer) { summaryLine(w, p) })
	})
}

func statusFile(c *runContext, file string, src []byte) error {
	plan, err := mutate.Plan(file, src, mutate.PushOptions{ImportRemote: true})
	if err != nil {
		return err
	}
	if plan.PublicationID == "" {
		return c.emit("confirmed", map[string]any{"file": file, "local": "new"}, func(w io.Writer) { fmt.Fprintln(w, "new\tnot pushed yet") })
	}
	local := "clean"
	if plan.Changed {
		local = "modified"
	}
	snap, err := article.LoadSnapshot(article.SnapshotPath(file, plan.PublicationID))
	if err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		p, err := acc.client.GetPublication(c.ctx, snap.PublisherID, snap.PublicationID)
		if err != nil {
			return err
		}
		state := local
		if p.Version != snap.Version || p.ModTime != snap.ModTime {
			state = "conflict"
		}
		data := publicationSummary(p)
		data["file"], data["local"], data["changes"] = file, state, plan.Changes
		return c.emit("confirmed", data, func(w io.Writer) {
			fmt.Fprintf(w, "%s\t", state)
			summaryLine(w, p)
		})
	})
}

func publicationSummary(p dzen.Publication) map[string]any {
	return map[string]any{"id": p.ID, "title": p.Title, "status": p.Status, "draft_status": p.DraftStatus, "version": p.Version, "url": p.CommonURL, "scheduled_for": p.RequestedPublishTime, "delayed": p.DelayedFlag}
}

func summaryLine(w io.Writer, p dzen.Publication) {
	fmt.Fprintf(w, "%s\t%s\t%s\tv%d\t%s\n", p.ID, p.Status, p.DraftStatus, p.Version, p.Title)
}

func listState(state string) func(*runContext) error {
	return func(c *runContext) error { return listPublications(c, state) }
}

func runArticleList(c *runContext) error {
	state := c.value("state")
	if state == "" {
		state = "published"
	}
	if state != "published" && state != "pending" {
		return apperr.New("usage_error", "--state must be published or pending")
	}
	return listPublications(c, state)
}

func listPublications(c *runContext, state string) error {
	limit, err := c.limit(100)
	if err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		items, err := acc.client.ListPublications(c.ctx, acc.sess.PublisherID, state, limit)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(items))
		for _, p := range items {
			out = append(out, publicationSummary(p))
		}
		return c.emit("confirmed", map[string]any{"items": out, "next_cursor": nil}, func(w io.Writer) {
			for _, p := range items {
				summaryLine(w, p)
			}
		})
	})
}

func runShow(c *runContext) error {
	if err := c.need(1, c.command+" <id>"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	format := c.value("format")
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		return apperr.New("usage_error", "--format must be md or json")
	}
	return c.withAccount(func(acc *account) error {
		p, err := acc.client.GetPublication(c.ctx, acc.sess.PublisherID, id)
		if err != nil {
			return err
		}
		front, body, err := mutate.Render(p)
		if err != nil {
			return err
		}
		data := publicationSummary(p)
		data["markdown"] = body
		if format == "json" && !c.jsonMode() {
			return writeJSON(c.app.Stdout, data)
		}
		return c.emit("confirmed", data, func(w io.Writer) { _, _ = w.Write(article.Format(front, body)) })
	})
}

func destructive(c *runContext, what, target string) (done bool, err error) {
	if c.has("dry-run") {
		return true, c.emit("dry_run_ok", map[string]any{"action": what, "target": target}, func(w io.Writer) { fmt.Fprintln(w, "would", what, target) })
	}
	return false, nil
}

func runDelete(c *runContext) error {
	if err := c.need(1, c.command+" <id>"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	if done, err := destructive(c, "delete", id); done {
		return err
	}
	yes := c.confirm("Delete " + id + " permanently? Dzen cannot restore it.")
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.Delete(c.ctx, id, yes)
		c.journal = out.JournalID
		if err != nil {
			return err
		}
		return c.emit("confirmed", map[string]any{"deleted": id}, func(w io.Writer) { fmt.Fprintln(w, "deleted", id) })
	})
}

func runDiscard(c *runContext) error {
	if err := c.need(1, "article discard-changes <id>"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	if done, err := destructive(c, "discard changes of", id); done {
		return err
	}
	yes := c.confirm("Discard the unpublished edits of " + id + "?")
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.DiscardChanges(c.ctx, id, yes)
		c.journal = out.JournalID
		if err != nil {
			return err
		}
		return c.emit("confirmed", map[string]any{"publication_id": id}, func(w io.Writer) { fmt.Fprintln(w, "discarded", id) })
	})
}

func runImage(c *runContext) error {
	if err := c.need(1, "image upload <file|url> --to <publication_id>"); err != nil {
		return err
	}
	target, source := c.value("to"), c.args[0]
	if !hexID.MatchString(target) {
		return apperr.New("usage_error", "--to must be a publication id")
	}
	remote := strings.HasPrefix(source, "https://")
	if !remote {
		if _, err := os.Stat(source); err != nil {
			return apperr.New("usage_error", "cannot read %s", source)
		}
	}
	if done, err := destructive(c, "upload", source); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		var id string
		var err error
		if remote {
			id, err = acc.service.UploadURL(c.ctx, target, source)
		} else {
			var data []byte
			if data, err = os.ReadFile(source); err == nil {
				id, err = acc.service.UploadFile(c.ctx, target, filepath.Base(source), data)
			}
		}
		if err != nil {
			return err
		}
		return c.emit("confirmed", map[string]any{"image_id": id}, func(w io.Writer) { fmt.Fprintln(w, id) })
	})
}

func (c *runContext) text() (string, error) {
	if c.has("stdin") {
		data, err := io.ReadAll(c.app.Stdin)
		if err != nil {
			return "", err
		}
		return checkComment(strings.TrimRight(string(data), "\n"))
	}
	if value, ok := c.flags.values["text"]; ok {
		return checkComment(value)
	}
	return "", apperr.New("usage_error", "pass --text or --stdin")
}

func checkComment(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", apperr.New("usage_error", "the comment is empty")
	}
	if len([]rune(text)) > 2500 {
		return "", apperr.New("validation_comment_too_long", "comments are limited to 2500 characters")
	}
	return text, nil
}

func (c *runContext) articleFlag() (string, error) {
	return publicationID(c.value("article"))
}

func runCommentList(c *runContext) error {
	limit, err := c.limit(50)
	if err != nil {
		return err
	}
	sorting := c.value("sort")
	if sorting == "" {
		sorting = "newest"
	}
	if c.has("channel") {
		if len(c.args) != 0 {
			return apperr.New("usage_error", "--channel takes no article")
		}
		return c.withAccount(func(acc *account) error {
			raw, err := acc.client.ChannelComments(c.ctx, acc.sess.PublisherID)
			if err != nil {
				return err
			}
			return c.emit("confirmed", json.RawMessage(raw), func(w io.Writer) { fmt.Fprintf(w, "%s\n", raw) })
		})
	}
	if err := c.need(1, "comment list <article>"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		items, next, err := fetchComments(c, acc, id, sorting, limit, c.value("cursor"))
		if err != nil {
			return err
		}
		return c.emit("confirmed", map[string]any{"items": commentJSON(items), "next_cursor": next}, func(w io.Writer) {
			for _, item := range items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.ID, item.ParentID, item.AuthorName, item.Text)
			}
		})
	})
}

func commentJSON(items []dzen.Comment) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, x := range items {
		out = append(out, map[string]any{"id": x.ID, "parent_id": x.ParentID, "author_id": x.AuthorID, "author": x.AuthorName, "text": x.Text, "created_at": x.CreatedAt, "likes": x.Likes, "dislikes": x.Dislikes})
	}
	return out
}

func fetchComments(c *runContext, acc *account, id, sorting string, limit int, cursor string) ([]dzen.Comment, string, error) {
	raw, next, err := acc.client.RootComments(c.ctx, acc.sess.PublisherID, id, sorting, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	items, withReplies, err := dzen.ParseComments(raw, id, "")
	if err != nil {
		return nil, "", apperr.New("contract_drift", "comments changed shape")
	}
	for _, root := range withReplies {
		child, err := acc.client.ChildComments(c.ctx, acc.sess.PublisherID, id, root)
		if err != nil {
			return nil, "", err
		}
		replies, _, err := dzen.ParseComments(child, id, root)
		if err != nil {
			return nil, "", apperr.New("contract_drift", "comments changed shape")
		}
		items = append(items, replies...)
	}
	return items, next, nil
}

func runCommentShow(c *runContext) error {
	if err := c.need(1, "comment show <comment_id> --article <id>"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	return c.withAccount(func(acc *account) error {
		item, err := findComment(c, acc, id, c.args[0])
		if err != nil {
			return err
		}
		return c.emit("confirmed", commentJSON([]dzen.Comment{item})[0], func(w io.Writer) { fmt.Fprintf(w, "%s\t%s\n", item.AuthorName, item.Text) })
	})
}

// findComment walks every comment page of an article until it finds the id.
func findComment(c *runContext, acc *account, publicationID, commentID string) (dzen.Comment, error) {
	cursor := ""
	for page := 0; page < 1000; page++ {
		items, next, err := fetchComments(c, acc, publicationID, "newest", 100, cursor)
		if err != nil {
			return dzen.Comment{}, err
		}
		for _, item := range items {
			if item.ID == commentID {
				return item, nil
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return dzen.Comment{}, apperr.New("not_found", "comment %s was not found", commentID)
}

func commentResult(c *runContext, id string, out mutate.Outcome, err error) error {
	c.journal = out.JournalID
	if err != nil {
		return err
	}
	var parsed struct {
		Items []struct {
			EntityData struct {
				ID json.Number `json:"id"`
			} `json:"entityData"`
		} `json:"items"`
	}
	dec := json.NewDecoder(strings.NewReader(string(out.Body)))
	dec.UseNumber()
	_ = dec.Decode(&parsed)
	if len(parsed.Items) > 0 && parsed.Items[0].EntityData.ID != "" {
		id = parsed.Items[0].EntityData.ID.String()
	}
	return c.emit("confirmed", map[string]any{"comment_id": id}, func(w io.Writer) { fmt.Fprintln(w, "ok", id) })
}

func runCommentPost(c *runContext) error {
	if err := c.need(1, "comment post <article> --text TEXT"); err != nil {
		return err
	}
	id, err := publicationID(c.args[0])
	if err != nil {
		return err
	}
	text, err := c.text()
	if err != nil {
		return err
	}
	if done, err := destructive(c, "comment on", id); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.PostComment(c.ctx, id, text, "", "")
		return commentResult(c, "", out, explainUnready(c, acc, id, err))
	})
}

func runCommentReply(c *runContext) error {
	if err := c.need(1, "comment reply <comment_id> --article <id> --text TEXT"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	text, err := c.text()
	if err != nil {
		return err
	}
	if done, err := destructive(c, "reply to", c.args[0]); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		root := rootOf(c, acc, id, c.args[0])
		out, err := acc.service.PostComment(c.ctx, id, text, root, c.args[0])
		return commentResult(c, "", out, explainUnready(c, acc, id, err))
	})
}

// explainUnready names the usual cause: Dzen refuses comments on an article
// until it has a public address.
func explainUnready(c *runContext, acc *account, publicationID string, err error) error {
	if !apperr.Is(err, "remote_validation") {
		return err
	}
	p, readErr := acc.client.GetPublication(c.ctx, acc.sess.PublisherID, publicationID)
	if readErr != nil || p.CommonURL != "" {
		return err
	}
	return apperr.New("remote_validation", "Dzen refuses comments until the article has a public URL; check dzen status %s later", publicationID)
}

// rootOf finds the thread root; replying to a root uses the comment itself.
func rootOf(c *runContext, acc *account, publicationID, commentID string) string {
	item, err := findComment(c, acc, publicationID, commentID)
	if err != nil || item.ParentID == "" {
		return commentID
	}
	return item.ParentID
}

func runCommentEdit(c *runContext) error {
	if err := c.need(1, "comment edit <comment_id> --article <id> --text TEXT"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	text, err := c.text()
	if err != nil {
		return err
	}
	if done, err := destructive(c, "edit", c.args[0]); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.EditComment(c.ctx, mutate.CommentTarget{PublicationID: id, CommentID: c.args[0]}, text)
		return commentResult(c, c.args[0], out, err)
	})
}

func commentAction(action string) func(*runContext) error {
	return func(c *runContext) error {
		if err := c.need(1, "comment "+action+" <comment_id> --article <id>"); err != nil {
			return err
		}
		id, err := c.articleFlag()
		if err != nil {
			return err
		}
		if done, err := destructive(c, action+" comment", c.args[0]); done {
			return err
		}
		if action == "delete" && !c.confirm("Delete comment "+c.args[0]+"?") {
			return apperr.New("needs_confirmation", "deleting a comment needs confirmation; pass --yes")
		}
		return c.withAccount(func(acc *account) error {
			out, err := acc.service.CommentAction(c.ctx, action, mutate.CommentTarget{PublicationID: id, CommentID: c.args[0]})
			return commentResult(c, c.args[0], out, err)
		})
	}
}

func react(c *runContext, reaction string) error {
	if err := c.need(1, "comment react <comment_id> --article <id>"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	if reaction == "" {
		return apperr.New("usage_error", "reaction is required")
	}
	if done, err := destructive(c, "set "+reaction+" on", c.args[0]); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.React(c.ctx, mutate.CommentTarget{PublicationID: id, CommentID: c.args[0]}, reaction)
		return commentResult(c, c.args[0], out, err)
	})
}

func runReact(c *runContext) error { return react(c, c.value("reaction")) }

func runReport(c *runContext) error {
	reason := c.value("reason")
	if reason != "spam_or_fraud" && reason != "obscene" && reason != "inappropriate" {
		return apperr.New("usage_error", "--reason must be spam_or_fraud, obscene or inappropriate")
	}
	return react(c, reason)
}

func runBan(c *runContext) error {
	if err := c.need(1, "comment ban <uid> --article <id>"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	if done, err := destructive(c, "ban", c.args[0]); done {
		return err
	}
	yes := c.confirm("Ban reader " + c.args[0] + " from the channel?")
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.Ban(c.ctx, id, c.args[0], c.has("delete-all"), yes)
		return commentResult(c, c.args[0], out, err)
	})
}

func runUnban(c *runContext) error {
	if err := c.need(1, "comment unban <uid> --article <id>"); err != nil {
		return err
	}
	id, err := c.articleFlag()
	if err != nil {
		return err
	}
	if done, err := destructive(c, "unban", c.args[0]); done {
		return err
	}
	return c.withAccount(func(acc *account) error {
		out, err := acc.service.Unban(c.ctx, id, c.args[0])
		return commentResult(c, c.args[0], out, err)
	})
}

func runBanned(c *runContext) error {
	return c.withAccount(func(acc *account) error {
		raw, err := acc.client.BannedUsers(c.ctx, acc.sess.PublisherID)
		if err != nil {
			return err
		}
		return c.emit("confirmed", json.RawMessage(raw), func(w io.Writer) { fmt.Fprintf(w, "%s\n", raw) })
	})
}

func runAuthImport(c *runContext) error {
	if browser := c.value("browser"); browser != "" && browser != "chrome" {
		return apperr.New("usage_error", "only --browser chrome is supported")
	}
	if c.has("all") && c.value("profile") != "" {
		return apperr.New("usage_error", "--all creates one profile per account; do not combine it with --profile")
	}
	root, err := session.ChromeRoot()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var imported []map[string]any
	var lastErr, fatal error
	importErr := c.eachCandidate(session.ImportOptions{Root: root, BrowserProfile: c.value("browser-profile"), NoInput: c.has("no-input")}, session.Source{}, func(candidate session.Candidate) bool {
		user, compact, err := c.verifyCandidate(candidate)
		if err != nil {
			lastErr = err
			return false
		}
		if seen[user.Login] {
			return !c.has("all")
		}
		seen[user.Login] = true
		name := c.value("profile")
		if name == "" {
			name = user.Login
		}
		if previous, err := c.sessions().Load(name); err == nil && previous.Login != "" && previous.Login != user.Login {
			fatal = apperr.New("principal_mismatch", "profile %s belongs to another account", name)
			return true
		}
		ua := candidate.UserAgent
		if ua == "" {
			ua = session.DefaultUserAgent
		}
		if err := c.sessions().Save(session.Session{Profile: name, Login: user.Login, PublisherID: user.PublisherID, Source: candidate.Source, Cookies: compact, UserAgent: ua, SavedAt: c.app.Now()}); err != nil {
			fatal = err
			return true
		}
		imported = append(imported, map[string]any{"profile": name, "login": user.Login, "publisher_id": user.PublisherID})
		return !c.has("all")
	})
	if fatal != nil {
		return fatal
	}
	if importErr != nil && len(imported) == 0 {
		return importErr
	}
	if len(imported) == 0 {
		if lastErr != nil {
			return lastErr
		}
		return apperr.New("not_authenticated", "no signed-in Dzen session was found in Chrome")
	}
	return c.emit("confirmed", map[string]any{"imported": imported}, func(w io.Writer) {
		for _, item := range imported {
			fmt.Fprintf(w, "%s\t%s\n", item["profile"], item["login"])
		}
	})
}

func runAuthLogin(c *runContext) error {
	if c.has("no-input") || !c.app.IsTTY() {
		return apperr.New("not_authenticated", "dzen auth login needs a terminal; use dzen auth import")
	}
	name := c.value("profile")
	fmt.Fprintln(c.app.Stderr, "Sign in to Dzen in the window that opens.")
	var user studio.User
	var compact []session.Cookie
	candidate, err := session.LoginWindow(c.ctx, filepath.Join(c.cfg.SessionsDir, ".browser"), 5*time.Minute, func(candidate session.Candidate) bool {
		var verifyErr error
		user, compact, verifyErr = c.verifyCandidate(candidate)
		return verifyErr == nil
	})
	if err != nil {
		return err
	}
	if name == "" {
		name = user.Login
	}
	if previous, err := c.sessions().Load(name); err == nil && previous.Login != "" && previous.Login != user.Login {
		return apperr.New("principal_mismatch", "profile %s belongs to another account", name)
	}
	if err := c.sessions().Save(session.Session{Profile: name, Login: user.Login, PublisherID: user.PublisherID, Source: candidate.Source, Cookies: compact, UserAgent: candidate.UserAgent, SavedAt: c.app.Now()}); err != nil {
		return err
	}
	return c.emit("confirmed", map[string]any{"profile": name, "login": user.Login}, func(w io.Writer) { fmt.Fprintf(w, "%s\t%s\n", name, user.Login) })
}

func runAuthList(c *runContext) error {
	names, err := c.sessions().List()
	if err != nil {
		return err
	}
	current, _ := c.sessions().Current()
	var items []map[string]any
	for _, name := range names {
		sess, err := c.sessions().Load(name)
		if err != nil {
			return err
		}
		items = append(items, map[string]any{"profile": name, "login": sess.Login, "publisher_id": sess.PublisherID, "source": sess.Source.Kind, "current": name == current})
	}
	return c.emit("confirmed", map[string]any{"items": items}, func(w io.Writer) {
		for _, item := range items {
			marker := " "
			if item["current"] == true {
				marker = "*"
			}
			fmt.Fprintf(w, "%s %s\t%s\t%s\n", marker, item["profile"], item["login"], item["source"])
		}
	})
}

func runAuthUse(c *runContext) error {
	if err := c.need(1, "auth use <profile>"); err != nil {
		return err
	}
	if err := c.sessions().Use(c.args[0]); err != nil {
		return err
	}
	return c.emit("confirmed", map[string]any{"profile": c.args[0]}, func(w io.Writer) { fmt.Fprintln(w, c.args[0]) })
}

func runAuthStatus(c *runContext) error {
	sess, err := c.loadSession()
	if err != nil {
		return err
	}
	data := map[string]any{"profile": sess.Profile, "login": sess.Login, "publisher_id": sess.PublisherID, "source": sess.Source.Kind, "saved_at": sess.SavedAt.UTC().Format(time.RFC3339)}
	if c.has("live") {
		acc, err := c.buildAccount(sess)
		if err != nil {
			return err
		}
		user, err := acc.client.User(c.ctx)
		if err != nil {
			return err
		}
		if user.Login != sess.Login {
			return apperr.New("principal_mismatch", "the session now belongs to another account")
		}
		data["verified"] = true
	}
	return c.emit("confirmed", data, func(w io.Writer) {
		fmt.Fprintf(w, "%s\t%s\tverified=%v\n", sess.Profile, sess.Login, data["verified"] == true)
	})
}

func runAuthLogout(c *runContext) error {
	if err := c.need(1, "auth logout <profile>"); err != nil {
		return err
	}
	if err := c.sessions().Delete(c.args[0]); err != nil {
		return err
	}
	return c.emit("confirmed", map[string]any{"profile": c.args[0]}, func(w io.Writer) { fmt.Fprintln(w, "removed", c.args[0]) })
}
