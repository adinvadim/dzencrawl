package mutate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adinvadim/dzencrawl/internal/apperr"
	"github.com/adinvadim/dzencrawl/internal/article"
	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/studio"
)

// MaxImageBytes is Dzen's documented upload limit.
const MaxImageBytes = 30 << 20

// Reader is the live view a mutation consults.
type Reader interface {
	GetPublication(ctx context.Context, publisherID, publicationID string) (dzen.Publication, error)
	User(ctx context.Context) (studio.User, error)
}

// Service performs the authoring workflows for one account.
type Service struct {
	Reader      Reader
	Runner      *Runner
	PublisherID string
	Rid         func() string
	Sleep       func(ctx context.Context, d time.Duration) error
	Now         func() time.Time
}

// PushOptions controls a push or publish.
type PushOptions struct {
	DryRun       bool
	Force        bool
	Yes          bool
	ImportRemote bool
}

// PushResult describes what a push did or would do.
type PushResult struct {
	Outcome       string           `json:"outcome"`
	PublicationID string           `json:"publication_id,omitempty"`
	Status        string           `json:"status,omitempty"`
	Version       int64            `json:"version,omitempty"`
	URL           string           `json:"url,omitempty"`
	Changes       []article.Change `json:"changes"`
	Uploads       []string         `json:"uploads,omitempty"`
	Changed       bool             `json:"changed"`
	BodySHA256    string           `json:"body_sha256,omitempty"`
	File          string           `json:"file,omitempty"`
	JournalID     string           `json:"journal_id,omitempty"`
	Warnings      []string         `json:"warnings,omitempty"`
}

// Validated parses an article file, failing on the first problem.
func Validated(src []byte) (article.Document, error) {
	doc, diags := article.ParseFile(src)
	if len(diags) != 0 {
		e := apperr.New(diags[0].Code, "%s", diags[0].Error())
		e.Diagnostics = diags
		return doc, e
	}
	return doc, nil
}

func loadSnapshotFor(file string, doc article.Document) (*article.Snapshot, error) {
	if doc.Front.Dzen == nil || doc.Front.Dzen.PublicationID == "" {
		return nil, nil
	}
	snap, err := article.LoadSnapshot(article.SnapshotPath(file, doc.Front.Dzen.PublicationID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, apperr.New("usage_error", "snapshot for %s is missing; run: dzen pull %s", doc.Front.Dzen.PublicationID, doc.Front.Dzen.PublicationID)
	}
	if err != nil {
		return nil, err
	}
	if snap.PublicationID != doc.Front.Dzen.PublicationID || snap.PublisherID != doc.Front.Dzen.PublisherID {
		return nil, apperr.New("validation_identity", "dzen.publication_id or dzen.publisher_id was changed by hand")
	}
	return &snap, nil
}

// settingsChanges lists front-matter edits relative to the snapshot.
func settingsChanges(front article.FrontMatter, base *article.Snapshot) []article.Change {
	want := MergeSettings(front, base.Settings)
	have := base.Settings
	if have.Comments == "" {
		have.Comments = "on"
	}
	if have.Visibility == "" {
		have.Visibility = want.Visibility
	}
	var out []article.Change
	note := func(name, from, to string) {
		if from != to {
			out = append(out, article.Change{Op: "settings", Detail: fmt.Sprintf("%s: %q -> %q", name, from, to)})
		}
	}
	note("title", have.Title, want.Title)
	note("snippet", have.Snippet, want.Snippet)
	note("snippet_frozen", fmt.Sprint(have.SnippetFrozen), fmt.Sprint(want.SnippetFrozen))
	note("comments", have.Comments, want.Comments)
	note("comments_visibility", have.Visibility, want.Visibility)
	note("comments_title", have.CommentsTitle, want.CommentsTitle)
	note("native_ad", fmt.Sprint(have.NativeAds), fmt.Sprint(want.NativeAds))
	if front.Cover != "" {
		if id, ok := base.IsBaseImage(front.Cover); !ok || id != have.CoverID {
			note("cover", have.CoverID, front.Cover)
		}
	}
	return out
}

// checkImage validates a local image against Dzen's documented limits.
func checkImage(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return apperr.New("validation_image", "image %s was not found", path)
	}
	if info.Size() > MaxImageBytes {
		return apperr.New("validation_image_too_large", "image %s is larger than 30 MB", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		if strings.EqualFold(filepath.Ext(path), ".webp") {
			return nil
		}
		return apperr.New("validation_image", "image %s has an unsupported format", path)
	}
	if config.Width < 300 {
		return apperr.New("validation_image_too_small", "image %s (%s) is %d px wide; Dzen needs at least 300", path, format, config.Width)
	}
	return nil
}

func resolvePath(file, src string) string {
	if filepath.IsAbs(src) {
		return src
	}
	return filepath.Join(filepath.Dir(file), src)
}

func isRemote(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

// localImageCheck rejects any image that cannot be sent, before any network.
func localImageCheck(file string, doc article.Document, base *article.Snapshot, opts PushOptions) ([]string, error) {
	sources := article.ImageSources(doc.Nodes)
	if doc.Front.Cover != "" {
		if err := checkCover(file, doc.Front.Cover, sources, base); err != nil {
			return nil, err
		}
		sources = append(sources, doc.Front.Cover)
	}
	var uploads []string
	for _, src := range sources {
		switch {
		case strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "http://"):
			return nil, apperr.New("validation_image", "image source %q is not allowed", src)
		case article.IsDzenImageURL(src):
			if _, ok := base.IsBaseImage(src); !ok {
				return nil, apperr.New("validation_image", "image %s does not belong to this publication; Dzen refuses to reuse image ids", src)
			}
		case isRemote(src):
			if !opts.ImportRemote {
				return nil, apperr.New("validation_image", "remote image %s needs --import-remote", src)
			}
			uploads = append(uploads, src)
		default:
			if err := checkImage(resolvePath(file, src)); err != nil {
				return nil, err
			}
			uploads = append(uploads, src)
		}
	}
	return uploads, nil
}

// checkCover rejects a cover that is not an image of the body: Dzen answers OK
// to such a save and silently drops the cover. A cover the article already has
// stays valid, since older articles can carry one from outside their body.
func checkCover(file, cover string, sources []string, base *article.Snapshot) error {
	locate := func(src string) string {
		if isRemote(src) {
			return src
		}
		return resolvePath(file, src)
	}
	for _, src := range sources {
		if locate(src) == locate(cover) {
			return nil
		}
	}
	if id, ok := base.IsBaseImage(cover); ok && id == base.Settings.CoverID {
		return nil
	}
	return apperr.New("validation_cover_not_in_body", "cover %s must also be an image in the article body; Dzen discards any other cover", cover)
}

// Plan builds the push plan with no network or credential access.
func Plan(file string, src []byte, opts PushOptions) (PushResult, error) {
	doc, err := Validated(src)
	if err != nil {
		return PushResult{}, err
	}
	base, err := loadSnapshotFor(file, doc)
	if err != nil {
		return PushResult{}, err
	}
	uploads, err := localImageCheck(file, doc, base, opts)
	if err != nil {
		return PushResult{}, err
	}
	plan, diags := article.Build(doc.Nodes, base, func(src string) (string, error) {
		if id, ok := base.IsBaseImage(src); ok {
			return id, nil
		}
		return "pending-upload", nil
	})
	if len(diags) != 0 {
		e := apperr.New(diags[0].Code, "%s", diags[0].Error())
		e.Diagnostics = diags
		return PushResult{}, e
	}
	body, err := plan.State.Marshal()
	if err != nil {
		return PushResult{}, err
	}
	sum := sha256.Sum256([]byte(body))
	result := PushResult{Outcome: "dry_run_ok", Changes: plan.Changes, Uploads: uploads, Changed: plan.Changed || base == nil, BodySHA256: hex.EncodeToString(sum[:]), File: file}
	if base != nil {
		result.PublicationID = base.PublicationID
		for _, change := range settingsChanges(doc.Front, base) {
			result.Changes, result.Changed = append(result.Changes, change), true
		}
	}
	return result, nil
}

func (s *Service) query(extra ...string) url.Values {
	q := url.Values{"publisherId": {s.PublisherID}, "clientRid": {s.Rid()}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

func (s *Service) jsonRequest(path string, query url.Values, body any) (studio.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return studio.Request{}, err
	}
	return studio.Request{Method: http.MethodPost, Path: path, Query: query, Body: data, ContentType: "application/json"}, nil
}

// conflict compares the snapshot with the server; Dzen never checks versions.
func conflict(base *article.Snapshot, remote dzen.Publication) bool {
	return remote.Version != base.Version || remote.ModTime != base.ModTime
}

func isPublished(p dzen.Publication) bool { return p.Status == "published" }

// MergeSettings applies the file's fields over the publication's settings.
func MergeSettings(front article.FrontMatter, current article.Settings) article.Settings {
	out := current
	out.Title = front.Title
	if front.Snippet != nil {
		out.Snippet, out.SnippetFrozen = *front.Snippet, true
	} else {
		out.SnippetFrozen = false
	}
	if front.Comments != "" {
		out.Comments = front.Comments
	}
	if front.CommentsVisibility != "" {
		out.Visibility = front.CommentsVisibility
	}
	if front.NativeAd != nil {
		out.NativeAds = *front.NativeAd
	}
	if front.CommentsTitle != nil {
		out.CommentsTitle = *front.CommentsTitle
	}
	if out.Comments == "" {
		out.Comments = "on"
	}
	if out.Visibility == "" {
		out.Visibility = "visible"
	}
	// Dzen treats comments as off exactly when they are invisible. When the
	// two disagree the file changed only one of them, and that change wins.
	if (out.Comments == "off") != (out.Visibility == "invisible") {
		if current.Comments == "off" {
			out.Comments, out.Visibility = "on", "visible"
		} else {
			out.Comments, out.Visibility = "off", "invisible"
		}
	}
	return out
}

// SettingsOf reads a publication's settings.
func SettingsOf(p dzen.Publication) article.Settings {
	return article.Settings{Title: p.Title, Snippet: p.Snippet, SnippetFrozen: p.SnippetFrozen, CoverID: p.CoverID, Comments: p.CommentsFlagState, Visibility: p.VisibleComments, NativeAds: p.HasNativeAds, CommentsTitle: p.CustomCommentsTitle}
}

func saveBody(p dzen.Publication, settings article.Settings, contentState string, delayed string, at string) map[string]any {
	preview := map[string]any{"title": settings.Title, "snippet": settings.Snippet, "galleryPreviewImages": []any{}}
	if settings.CoverID != "" {
		preview["image"] = map[string]any{"id": settings.CoverID}
	}
	tariffs := json.RawMessage(p.PremiumTariffs)
	if len(tariffs) == 0 {
		tariffs = json.RawMessage(`[]`)
	}
	visibility := p.VisibilityType
	if visibility == "" {
		visibility = "all"
	}
	body := map[string]any{
		"id": p.ID, "preview": preview, "snippetFrozen": settings.SnippetFrozen, "hasNativeAds": settings.NativeAds,
		"commentsFlagState": settings.Comments, "delayedPublicationFlagState": delayed, "visibleComments": settings.Visibility,
		"visibilityType": visibility, "premiumTariffs": tariffs, "customCommentsTitle": settings.CommentsTitle,
		"articleContent": map[string]any{"contentState": contentState},
		"tagsInput":      map[string]any{"tags": []any{}, "detectedTagsShown": false},
	}
	if delayed == "on" {
		body["requestedPublishTime"] = at
	}
	return body
}

type blockFact struct {
	Type   string
	Text   string
	Styles []StyleFact
	Links  []string
	Data   string
}

// StyleFact is one inline style of a block, for read-after-write comparison.
type StyleFact struct {
	Offset, Length int
	Style          string
}

func facts(raw string) ([]blockFact, bool) {
	state, err := article.ParseContentState(raw)
	if err != nil {
		return nil, false
	}
	var out []blockFact
	for _, b := range state.Blocks {
		if b.Type == "marker" {
			continue
		}
		fact := blockFact{Type: b.Type, Text: b.Text}
		for _, s := range b.Styles {
			fact.Styles = append(fact.Styles, StyleFact{Offset: s.Offset, Length: s.Length, Style: s.Style})
		}
		sort.Slice(fact.Styles, func(i, j int) bool {
			return fmt.Sprint(fact.Styles[i]) < fmt.Sprint(fact.Styles[j])
		})
		for _, r := range b.Entities {
			entity := state.EntityMap[strconv.Itoa(r.Key)]
			url, _ := entity.Data["url"].(string)
			fact.Links = append(fact.Links, fmt.Sprintf("%d:%d:%s", r.Offset, r.Length, url))
		}
		sort.Strings(fact.Links)
		if len(b.Data) != 0 {
			data, _ := json.Marshal(b.Data)
			fact.Data = string(data)
		}
		out = append(out, fact)
	}
	return out, true
}

// sameContent proves a save landed: the stored document must equal what was
// sent, including styles, links and images.
func sameContent(remote, sent string) bool {
	a, ok := facts(remote)
	if !ok {
		return false
	}
	b, ok := facts(sent)
	return ok && reflect.DeepEqual(a, b)
}

// landed reports whether a read shows the requested document, settings and
// publication state.
func landed(now dzen.Publication, baseVersion int64, sent string, settings article.Settings, publishing bool, delayed, at string) bool {
	if now.Version <= baseVersion || !sameContent(now.ContentState, sent) {
		return false
	}
	got := SettingsOf(now)
	if got.Title != settings.Title || got.Comments != settings.Comments || got.Visibility != settings.Visibility || got.NativeAds != settings.NativeAds || got.CommentsTitle != settings.CommentsTitle || got.CoverID != settings.CoverID || got.SnippetFrozen != settings.SnippetFrozen {
		return false
	}
	if settings.SnippetFrozen && got.Snippet != settings.Snippet {
		return false
	}
	if !publishing {
		return true
	}
	if now.Status != "published" || now.DelayedFlag != delayed {
		return false
	}
	return delayed != "on" || sameInstant(now.RequestedPublishTime, at)
}

func sameInstant(a, b string) bool {
	x, errX := time.Parse(time.RFC3339, a)
	y, errY := time.Parse(time.RFC3339, b)
	return errX == nil && errY == nil && x.Equal(y)
}

// createDraft reads the new draft straight from Dzen's answer, so nothing can
// fail between its creation and recording its identity.
func (s *Service) createDraft(ctx context.Context, title string) (dzen.Publication, string, error) {
	req, err := s.jsonRequest("/editor-api/v2/add-publication", s.query(), map[string]any{"title": title, "publisherId": s.PublisherID, "publicationType": "article"})
	if err != nil {
		return dzen.Publication{}, "", err
	}
	out, err := s.Runner.Run(ctx, Op{Kind: "add-publication", Target: s.PublisherID, Request: req})
	if err != nil {
		return dzen.Publication{}, out.JournalID, err
	}
	p, err := dzen.DecodePublication(out.Body, article.Images{})
	if err != nil || p.ID == "" {
		return dzen.Publication{}, out.JournalID, apperr.New("contract_drift", "add-publication response has no id")
	}
	if p.PublisherID == "" {
		p.PublisherID = s.PublisherID
	}
	return p, out.JournalID, nil
}

// uploader caches uploads by content so that repeating a push uploads nothing.
type uploader struct {
	s       *Service
	file    string
	pubID   string
	uploads map[string]string
	base    *article.Snapshot
	sent    []string
}

func (u *uploader) resolve(ctx context.Context, src string) (string, error) {
	if id, ok := u.base.IsBaseImage(src); ok {
		return id, nil
	}
	if isRemote(src) {
		if id := u.uploads["url:"+src]; id != "" {
			return id, nil
		}
		id, err := u.s.UploadURL(ctx, u.pubID, src)
		if err != nil {
			return "", refusedImage(src, err)
		}
		u.uploads["url:"+src] = id
		u.sent = append(u.sent, src)
		return id, nil
	}
	path := resolvePath(u.file, src)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", apperr.New("validation_image", "image %s was not found", src)
	}
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	if id := u.uploads[key]; id != "" {
		return id, nil
	}
	id, err := u.s.UploadFile(ctx, u.pubID, filepath.Base(path), data)
	if err != nil {
		return "", refusedImage(src, err)
	}
	u.uploads[key] = id
	u.sent = append(u.sent, src)
	return id, nil
}

// refusedImage reports an upload that yielded no image id. The article itself
// has not been saved yet, so its outcome is known even if the upload's is not.
func refusedImage(src string, err error) error {
	if !apperr.Is(err, "outcome_unknown") {
		return err
	}
	return apperr.New("remote_validation", "Dzen did not accept image %s and returned no image id; the article was not saved. Try another file, or download a remote image and use a local path", src)
}

// UploadFile uploads image bytes to a publication. Only an orphan image can
// result from a repeat, so this is the one mutation allowed a single retry.
func (s *Service) UploadFile(ctx context.Context, publicationID, name string, data []byte) (string, error) {
	body, contentType, err := studio.MultipartImage(name, data)
	if err != nil {
		return "", err
	}
	req := studio.Request{Method: http.MethodPost, Path: "/editor-api/v2/add-image", Query: s.query("publicationId", publicationID), Body: body, ContentType: contentType}
	return s.upload(ctx, publicationID, req)
}

// UploadURL asks Dzen to fetch a public image.
func (s *Service) UploadURL(ctx context.Context, publicationID, source string) (string, error) {
	req, err := s.jsonRequest("/editor-api/v2/add-image-from-url", s.query("publicationId", publicationID, "url", source), map[string]any{})
	if err != nil {
		return "", err
	}
	return s.upload(ctx, publicationID, req)
}

func (s *Service) upload(ctx context.Context, publicationID string, req studio.Request) (string, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := s.Runner.Run(ctx, Op{Kind: "add-image", Target: publicationID, Request: req})
		if err != nil {
			last = err
			if apperr.Is(err, "outcome_unknown") || apperr.Is(err, "server_unavailable") {
				continue
			}
			return "", err
		}
		var image struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(out.Body, &image); err != nil || image.ID == "" {
			return "", apperr.New("contract_drift", "add-image response has no id")
		}
		return image.ID, nil
	}
	return "", last
}

// send is the shared save-or-publish path.
func (s *Service) send(ctx context.Context, file string, src []byte, opts PushOptions, kind string, delayed, at string) (PushResult, dzen.Publication, error) {
	doc, err := Validated(src)
	if err != nil {
		return PushResult{}, dzen.Publication{}, err
	}
	base, err := loadSnapshotFor(file, doc)
	if err != nil {
		return PushResult{}, dzen.Publication{}, err
	}
	if _, err := localImageCheck(file, doc, base, opts); err != nil {
		return PushResult{}, dzen.Publication{}, err
	}
	result := PushResult{File: file, Changes: []article.Change{}}
	var remote dzen.Publication
	publishing := kind != "save"
	if base != nil {
		if remote, err = s.Reader.GetPublication(ctx, base.PublisherID, base.PublicationID); err != nil {
			return result, remote, err
		}
		if conflict(base, remote) && !opts.Force {
			return result, remote, apperr.New("revision_conflict", "the article changed on Dzen since it was pulled (version %d, now %d); run dzen diff --remote, then pull or use --force", base.Version, remote.Version)
		}
		if !publishing && isPublished(remote) && !opts.Yes {
			return result, remote, apperr.New("needs_confirmation", "this article is published: saving removes it from the feed until the edits are published; pass --yes")
		}
		base.Images = mergeImages(base.Images, remote.Images)
	} else {
		remote, result.JournalID, err = s.createDraft(ctx, doc.Front.Title)
		if err != nil {
			return result, remote, err
		}
		result.PublicationID = remote.ID
		created, err := persistIdentity(file, doc, remote)
		if err != nil {
			return result, remote, err
		}
		base = &created
	}
	uploads := map[string]string{}
	if base != nil && base.Uploads != nil {
		uploads = base.Uploads
	}
	up := &uploader{s: s, file: file, pubID: remote.ID, uploads: uploads, base: base}
	resolved := map[string]string{}
	for _, source := range article.ImageSources(doc.Nodes) {
		id, err := up.resolve(ctx, source)
		if err != nil {
			return result, remote, err
		}
		resolved[source] = id
	}
	settings := MergeSettings(doc.Front, SettingsOf(remote))
	if doc.Front.Cover != "" {
		if settings.CoverID, err = up.resolve(ctx, doc.Front.Cover); err != nil {
			return result, remote, err
		}
	}
	if len(up.sent) > 0 {
		base.Uploads = uploads
		if err := article.SaveSnapshot(article.SnapshotPath(file, base.PublicationID), *base); err != nil {
			return result, remote, err
		}
	}
	plan, diags := article.Build(doc.Nodes, base, func(src string) (string, error) { return resolved[src], nil })
	if len(diags) != 0 {
		e := apperr.New(diags[0].Code, "%s", diags[0].Error())
		e.Diagnostics = diags
		return result, remote, e
	}
	result.Changes, result.Changed, result.Uploads = plan.Changes, plan.Changed, up.sent
	result.PublicationID = remote.ID
	contentState := remote.ContentState
	if plan.Changed {
		if contentState, err = plan.State.Marshal(); err != nil {
			return result, remote, err
		}
	}
	settingsChanged := settings != SettingsOf(remote)
	if settingsChanged {
		result.Changes = append(result.Changes, settingsChanges(doc.Front, base)...)
		result.Changed = true
	}
	if !publishing && !result.Changed {
		if conflict(base, remote) {
			if err := WriteWorkingCopy(file, remote, uploads, false); err != nil {
				return result, remote, err
			}
		}
		result.Outcome, result.Status, result.Version = "confirmed", remote.Status, remote.Version
		return result, remote, nil
	}
	if contentState == "" {
		return result, remote, apperr.New("validation_body_empty", "the article has no body")
	}
	currentDelayed, requested := delayed, at
	if !publishing {
		currentDelayed = "off"
		if remote.DelayedFlag == "on" {
			currentDelayed, requested = "on", remote.RequestedPublishTime
		}
	}
	path := "/editor-api/v2/update-publication-content"
	if publishing {
		path += "-and-publish"
	}
	req, err := s.jsonRequest(path, s.query(), saveBody(remote, settings, contentState, currentDelayed, requested))
	if err != nil {
		return result, remote, err
	}
	sent := contentState
	baseVersion := remote.Version
	out, err := s.Runner.Run(ctx, Op{Kind: kind, Target: remote.ID, Request: req, BaseVersion: baseVersion, Verify: func(ctx context.Context) (Proof, error) {
		now, err := s.Reader.GetPublication(ctx, s.PublisherID, remote.ID)
		if err != nil {
			return Unproven, err
		}
		if landed(now, baseVersion, sent, settings, publishing, currentDelayed, requested) {
			return Applied, nil
		}
		return Unproven, nil
	}})
	result.JournalID = out.JournalID
	if err != nil {
		if strings.HasPrefix(apperr.As(err).Code, "captcha_") {
			s.keepSavedContent(ctx, file, remote.ID, sent, uploads)
		}
		return result, remote, err
	}
	var saved struct {
		Result string `json:"result"`
	}
	if out.Body != nil && (json.Unmarshal(out.Body, &saved) != nil || saved.Result != "OK") {
		return result, remote, apperr.New("contract_drift", "save response has no OK result")
	}
	after, err := s.Reader.GetPublication(ctx, s.PublisherID, remote.ID)
	if err != nil {
		return result, remote, err
	}
	if err := WriteWorkingCopy(file, after, uploads, doc.Front.Cover != ""); err != nil {
		return result, after, err
	}
	result.Warnings = settingsWarnings(settings, SettingsOf(after))
	result.Outcome, result.Status, result.Version, result.URL = "confirmed", after.Status, after.Version, after.CommonURL
	return result, after, nil
}

// keepSavedContent syncs the working copy after a captcha refusal: Dzen stores
// the content before asking, so without this the next attempt reports a false
// conflict.
func (s *Service) keepSavedContent(ctx context.Context, file, publicationID, sent string, uploads map[string]string) {
	ctx = context.WithoutCancel(ctx)
	now, err := s.Reader.GetPublication(ctx, s.PublisherID, publicationID)
	if err == nil && sameContent(now.ContentState, sent) {
		_ = WriteWorkingCopy(file, now, uploads, false)
	}
}

// settingsWarnings names requested settings Dzen did not keep.
func settingsWarnings(want, got article.Settings) []string {
	var out []string
	note := func(name string, same bool) {
		if !same {
			out = append(out, "Dzen did not keep the requested "+name)
		}
	}
	note("title", want.Title == got.Title)
	note("comments", want.Comments == got.Comments)
	note("comments_visibility", want.Visibility == got.Visibility)
	note("comments_title", want.CommentsTitle == got.CommentsTitle)
	note("native_ad", want.NativeAds == got.NativeAds)
	note("cover", want.CoverID == got.CoverID)
	if want.SnippetFrozen {
		note("snippet", want.Snippet == got.Snippet)
	}
	return out
}

func mergeImages(a, b article.Images) article.Images {
	out := article.Images{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// Push creates or updates a draft from a file.
func (s *Service) Push(ctx context.Context, file string, src []byte, opts PushOptions) (PushResult, error) {
	result, _, err := s.send(ctx, file, src, opts, "save", "off", "")
	return result, err
}

// PublishOptions adds publication timing to a push.
type PublishOptions struct {
	PushOptions
	At   *time.Time
	Wait time.Duration
}

// ScheduleWindow returns the time Dzen wants, or a local validation error.
func ScheduleWindow(at, now time.Time) (string, error) {
	delta := at.Sub(now)
	if delta < time.Hour || delta > 365*24*time.Hour {
		return "", apperr.New("validation_schedule_window", "scheduled time must be 1 hour to 365 days from now")
	}
	return at.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

// Publish publishes a file or an existing publication now or at a time.
func (s *Service) Publish(ctx context.Context, file string, src []byte, opts PublishOptions) (PushResult, error) {
	delayed, at := "off", ""
	if opts.At != nil {
		value, err := ScheduleWindow(*opts.At, s.Now())
		if err != nil {
			return PushResult{}, err
		}
		delayed, at = "on", value
	}
	if !opts.Yes {
		return PushResult{}, apperr.New("needs_confirmation", "publishing is visible to readers; pass --yes")
	}
	result, after, err := s.send(ctx, file, src, opts.PushOptions, "publish", delayed, at)
	if err != nil {
		return result, err
	}
	return s.settle(ctx, result, after, opts)
}

// PublishExisting publishes a publication without any file.
func (s *Service) PublishExisting(ctx context.Context, publicationID string, opts PublishOptions) (PushResult, error) {
	delayed, at := "off", ""
	if opts.At != nil {
		value, err := ScheduleWindow(*opts.At, s.Now())
		if err != nil {
			return PushResult{}, err
		}
		delayed, at = "on", value
	}
	if !opts.Yes {
		return PushResult{}, apperr.New("needs_confirmation", "publishing is visible to readers; pass --yes")
	}
	remote, err := s.Reader.GetPublication(ctx, s.PublisherID, publicationID)
	if err != nil {
		return PushResult{}, err
	}
	if remote.ContentState == "" {
		return PushResult{}, apperr.New("validation_body_empty", "the article has no body")
	}
	req, err := s.jsonRequest("/editor-api/v2/update-publication-content-and-publish", s.query(), saveBody(remote, SettingsOf(remote), remote.ContentState, delayed, at))
	if err != nil {
		return PushResult{}, err
	}
	baseVersion := remote.Version
	out, err := s.Runner.Run(ctx, Op{Kind: "publish", Target: publicationID, Request: req, BaseVersion: baseVersion, Verify: func(ctx context.Context) (Proof, error) {
		now, err := s.Reader.GetPublication(ctx, s.PublisherID, publicationID)
		if err != nil {
			return Unproven, err
		}
		if landed(now, baseVersion, remote.ContentState, SettingsOf(remote), true, delayed, at) {
			return Applied, nil
		}
		return Unproven, nil
	}})
	if err != nil {
		return PushResult{JournalID: out.JournalID}, err
	}
	var accepted struct {
		Result string `json:"result"`
	}
	if out.Body != nil && (json.Unmarshal(out.Body, &accepted) != nil || accepted.Result != "OK") {
		return PushResult{JournalID: out.JournalID}, apperr.New("contract_drift", "publish response has no OK result")
	}
	after, err := s.Reader.GetPublication(ctx, s.PublisherID, publicationID)
	if err != nil {
		return PushResult{JournalID: out.JournalID}, err
	}
	return s.settle(ctx, PushResult{PublicationID: publicationID, Changes: []article.Change{}, JournalID: out.JournalID}, after, opts)
}

// settle maps the post-publish state. Without a public URL Dzen is still
// processing the article, so success is only provisional.
func (s *Service) settle(ctx context.Context, result PushResult, after dzen.Publication, opts PublishOptions) (PushResult, error) {
	end := s.Now().Add(opts.Wait)
	for opts.At == nil && after.CommonURL == "" && s.Now().Before(end) {
		if err := s.Sleep(ctx, 5*time.Second); err != nil {
			return result, err
		}
		next, err := s.Reader.GetPublication(ctx, s.PublisherID, after.ID)
		if err != nil {
			return result, err
		}
		after = next
	}
	result.Status, result.Version, result.URL = after.Status, after.Version, after.CommonURL
	result.Outcome = "confirmed"
	if opts.At == nil && after.CommonURL == "" {
		result.Outcome = "accepted_pending"
	}
	return result, nil
}

// Delete permanently removes a publication and proves it is gone.
func (s *Service) Delete(ctx context.Context, publicationID string, yes bool) (Outcome, error) {
	if !yes {
		return Outcome{}, apperr.New("needs_confirmation", "deleting is irreversible; pass --yes")
	}
	query := url.Values{"id": {publicationID}, "publisherId": {s.PublisherID}}
	verify := func(ctx context.Context) (Proof, error) {
		_, err := s.Reader.GetPublication(ctx, s.PublisherID, publicationID)
		if apperr.Is(err, "not_found") {
			return Applied, nil
		}
		return Unproven, err
	}
	out, err := s.Runner.Run(ctx, Op{Kind: "delete-publication", Target: publicationID, Request: studio.Request{Method: http.MethodPost, Path: "/editor-api/v2/delete-publication", Query: query}, Verify: verify})
	if err != nil {
		return out, err
	}
	if proof, err := verify(ctx); err != nil || proof != Applied {
		return out, apperr.New("outcome_unknown", "delete was accepted but the publication can still be read")
	}
	return out, nil
}

// DiscardChanges drops unpublished edits of a published article.
func (s *Service) DiscardChanges(ctx context.Context, publicationID string, yes bool) (Outcome, error) {
	if !yes {
		return Outcome{}, apperr.New("needs_confirmation", "discarding edits is irreversible; pass --yes")
	}
	query := s.query("publicationId", publicationID)
	return s.Runner.Run(ctx, Op{Kind: "remove-unpublished-changes", Target: publicationID, Request: studio.Request{Method: http.MethodPost, Path: "/editor-api/v2/remove-unpublished-changes", Query: query}})
}

// CommentTarget addresses comment actions.
type CommentTarget struct {
	PublicationID string
	CommentID     string
}

func (s *Service) commentRequest(method, path string, body map[string]any) (studio.Request, error) {
	now := s.Now().UnixMilli()
	body["sessionTs"], body["clientTs"] = now, now
	if _, ok := body["deviceId"]; !ok {
		body["deviceId"] = ""
	}
	data, err := json.Marshal(body)
	if err != nil {
		return studio.Request{}, err
	}
	return studio.Request{Method: method, Path: "/api/comments/v2/" + path, Query: url.Values{"rid": {s.Rid()}}, Body: data, ContentType: "application/json", Headers: studio.CommentHeaders()}, nil
}

func (s *Service) comment(ctx context.Context, kind, target, method, path string, body map[string]any) (Outcome, error) {
	req, err := s.commentRequest(method, path, body)
	if err != nil {
		return Outcome{}, err
	}
	return s.Runner.Run(ctx, Op{Kind: kind, Target: target, Request: req, Check: CheckCommentStatus})
}

func (s *Service) document(publicationID string) map[string]any {
	return map[string]any{"documentId": "native:" + publicationID, "publicationPublisherId": s.PublisherID}
}

// PostComment writes a comment or, with a parent, a reply.
func (s *Service) PostComment(ctx context.Context, publicationID, text, rootID, replyTo string) (Outcome, error) {
	body := s.document(publicationID)
	body["publisherByChannel"], body["text"], body["imageIds"], body["lang"] = true, text, []string{}, "ru"
	if rootID != "" {
		body["rootCommentId"], body["replyToCommentId"] = numeric(rootID), numeric(replyTo)
	}
	return s.comment(ctx, "create-comment", publicationID, http.MethodPost, "create-comment", body)
}

// EditComment replaces a comment's text.
func (s *Service) EditComment(ctx context.Context, t CommentTarget, text string) (Outcome, error) {
	body := s.document(t.PublicationID)
	body["publisherByChannel"], body["text"], body["imageIds"], body["lang"] = true, text, []string{}, "ru"
	return s.comment(ctx, "edit-comment", t.CommentID, http.MethodPost, "edit-comment/"+url.PathEscape(t.CommentID), body)
}

// CommentAction runs delete, restore, pin or unpin.
func (s *Service) CommentAction(ctx context.Context, action string, t CommentTarget) (Outcome, error) {
	switch action {
	case "delete", "restore":
		return s.comment(ctx, action+"-comment", t.CommentID, http.MethodPost, action+"-comment/"+url.PathEscape(t.CommentID), s.document(t.PublicationID))
	case "pin", "unpin":
		body := map[string]any{"commentId": numeric(t.CommentID), "documentId": "native:" + t.PublicationID}
		return s.comment(ctx, action+"-comment", t.CommentID, http.MethodPut, action+"-comment", body)
	}
	return Outcome{}, fmt.Errorf("unknown comment action %q", action)
}

var reactionName = regexp.MustCompile(`^[a-z_]{2,40}$`)

// React sets a reaction, or files a report through its reaction reason.
func (s *Service) React(ctx context.Context, t CommentTarget, reaction string) (Outcome, error) {
	if !reactionName.MatchString(reaction) {
		return Outcome{}, apperr.New("usage_error", "invalid reaction %q", reaction)
	}
	body := s.document(t.PublicationID)
	body["reaction"] = reaction
	return s.comment(ctx, "react-to-comment", t.CommentID, http.MethodPost, "react-to-comment/"+url.PathEscape(t.CommentID), body)
}

// Ban blocks a commenter on the channel.
func (s *Service) Ban(ctx context.Context, publicationID, uid string, deleteAll, yes bool) (Outcome, error) {
	if !yes {
		return Outcome{}, apperr.New("needs_confirmation", "banning a reader needs confirmation; pass --yes")
	}
	body := s.document(publicationID)
	body["shouldDeleteAllComments"] = deleteAll
	return s.comment(ctx, "ban-user", uid, http.MethodPost, "ban-user/"+url.PathEscape(uid), body)
}

// Unban lifts a block.
func (s *Service) Unban(ctx context.Context, publicationID, uid string) (Outcome, error) {
	return s.comment(ctx, "unban-user", uid, http.MethodPost, "unban-user/"+url.PathEscape(uid), s.document(publicationID))
}

func numeric(id string) any {
	if n, err := strconv.ParseInt(id, 10, 64); err == nil {
		return n
	}
	return id
}

// Render builds an article file and snapshot body from a publication.
func Render(p dzen.Publication) (article.FrontMatter, string, error) {
	state, err := article.ParseContentState(p.ContentState)
	if err != nil {
		return article.FrontMatter{}, "", apperr.New("contract_drift", "contentState changed shape")
	}
	var nodes []article.Node
	for _, b := range state.Blocks {
		if b.Type != "marker" {
			nodes = append(nodes, article.NodeFromBlock(b, state.EntityMap, p.Images))
		}
	}
	front := article.FrontMatter{Title: p.Title, Comments: p.CommentsFlagState, CommentsVisibility: p.VisibleComments,
		Dzen: &article.Meta{Schema: article.SchemaLevel, PublicationID: p.ID, PublisherID: p.PublisherID, Version: p.Version, Status: p.Status}}
	if p.SnippetFrozen {
		snippet := p.Snippet
		front.Snippet = &snippet
	}
	if p.HasNativeAds {
		yes := true
		front.NativeAd = &yes
	}
	if p.CustomCommentsTitle != "" {
		title := p.CustomCommentsTitle
		front.CommentsTitle = &title
	}
	if info, ok := p.Images[p.CoverID]; ok && info.GroupID != 0 {
		front.Cover = info.URL()
	}
	return front, article.RenderBody(nodes), nil
}

// persistIdentity records a new draft on disk at once, keeping the author's
// body, so a failure later in the push resumes this draft on the next attempt.
func persistIdentity(file string, doc article.Document, p dzen.Publication) (article.Snapshot, error) {
	snapshot := article.Snapshot{Schema: article.SchemaLevel, PublicationID: p.ID, PublisherID: p.PublisherID, Version: p.Version, ModTime: p.ModTime, Status: p.Status, ContentState: p.ContentState, Images: p.Images, Settings: SettingsOf(p), Uploads: map[string]string{}}
	if snapshot.Images == nil {
		snapshot.Images = article.Images{}
	}
	if err := article.SaveSnapshot(article.SnapshotPath(file, p.ID), snapshot); err != nil {
		return snapshot, err
	}
	front := doc.Front
	front.Dzen = &article.Meta{Schema: article.SchemaLevel, PublicationID: p.ID, PublisherID: p.PublisherID, Version: p.Version, Status: p.Status}
	body := strings.TrimLeft(doc.Body, "\n")
	return snapshot, article.AtomicWrite(file, article.Format(front, body), 0o644)
}

// WriteWorkingCopy stores the canonical file and its snapshot atomically.
func WriteWorkingCopy(file string, p dzen.Publication, uploads map[string]string, _ bool) error {
	front, body, err := Render(p)
	if err != nil {
		return err
	}
	if uploads == nil {
		uploads = map[string]string{}
	}
	snapshot := article.Snapshot{Schema: article.SchemaLevel, PublicationID: p.ID, PublisherID: p.PublisherID, Version: p.Version, ModTime: p.ModTime, Status: p.Status, ContentState: p.ContentState, Images: p.Images, Settings: SettingsOf(p), Uploads: uploads}
	if err := article.SaveSnapshot(article.SnapshotPath(file, p.ID), snapshot); err != nil {
		return err
	}
	return article.AtomicWrite(file, article.Format(front, body), 0o644)
}
