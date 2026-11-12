package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adinvadim/dzencrawl/internal/article"
	"github.com/adinvadim/dzencrawl/internal/dzen"
	"github.com/adinvadim/dzencrawl/internal/session"
)

const (
	testCookie = "very-secret-cookie"
	pubHex     = "aaaaaaaaaaaaaaaaaaaaaaaa"
	publisher  = "bbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeDzen is an in-memory stand-in for the private author API.
type fakeDzen struct {
	*httptest.Server
	mu                 sync.Mutex
	requests           []string
	bodies             map[string][]byte
	state              string
	version            int64
	status             string
	login              string
	expired            int
	images             map[string]article.ImageInfo
	failUpload         int
	crashUpload        int
	expireOn           string
	frozen             bool
	badReadAfterDelete bool
	commentPages       bool
	captchaOnPublish   int
	threaded           bool
	failSaves          int
	noCommonURL        bool
	listed             int
}

func newFake(t *testing.T) *fakeDzen {
	t.Helper()
	f := &fakeDzen{bodies: map[string][]byte{}, status: "draft", login: "author", images: map[string]article.ImageInfo{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDzen) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expired > 0 && r.URL.Path != "/media-api/csrf-token" {
		f.expired--
		w.WriteHeader(401)
		return
	}
	if !strings.Contains(r.Header.Get("Cookie"), testCookie) {
		w.WriteHeader(401)
		return
	}
	if f.expireOn == r.URL.Path {
		f.expireOn = ""
		w.WriteHeader(401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.bodies[r.URL.Path] = body
	switch r.URL.Path {
	case "/media-api/csrf-token":
		fmt.Fprint(w, `{"result":"tok"}`)
	case "/editor-api/v2/user":
		fmt.Fprintf(w, `{"user":{"uid":1,"login":%q,"publisherId":%q},"publisher":{"id":%q}}`, f.login, publisher, publisher)
	case "/editor-api/v2/add-publication":
		f.version, f.state = 0, ""
		fmt.Fprintf(w, `{"id":%q,"publisherId":%q,"version":0,"privateData":{"status":"draft"},"content":{"type":"article","modTime":0,"preview":{"title":"Заголовок"},"articleContent":{"contentState":""}}}`, pubHex, publisher)
	case "/editor-api/v2/add-image":
		if f.crashUpload > 0 {
			f.crashUpload--
			w.WriteHeader(500)
			return
		}
		if f.failUpload > 0 {
			f.failUpload--
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"type":"validation-error"}]}`)
			return
		}
		f.images["img1"] = article.ImageInfo{ID: "img1", Namespace: "zen_doc", GroupID: 7, ImageName: "pub_a_img1"}
		fmt.Fprint(w, `{"id":"img1"}`)
	case "/editor-api/v2/update-publication-content", "/editor-api/v2/update-publication-content-and-publish":
		if f.failSaves > 0 {
			f.failSaves--
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"type":"validation-error"}]}`)
			return
		}
		var save struct {
			ArticleContent struct {
				ContentState string `json:"contentState"`
			} `json:"articleContent"`
		}
		_ = json.Unmarshal(body, &save)
		f.state = save.ArticleContent.ContentState
		f.version++
		if strings.HasSuffix(r.URL.Path, "publish") && f.captchaOnPublish > 0 {
			f.captchaOnPublish--
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"type":"captcha-required-error"}],"captchaLink":"https://id.vk.ru/x"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "publish") {
			f.status = "published"
		}
		fmt.Fprint(w, `{"result":"OK"}`)
	case "/editor-api/v2/delete-publication":
		f.status = "deleted"
		fmt.Fprintf(w, `{"objectId":%q}`, r.URL.Query().Get("id"))
	case "/editor-api/v2/publisher/" + publisher + "/publication/" + pubHex:
		if f.status == "deleted" && f.badReadAfterDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"type":"internal-error"}]}`)
			return
		}
		if f.status == "deleted" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"errors":[{"type":"not-found"}]}`)
			return
		}
		f.writePublication(w)
	case "/editor-api/v3/publications":
		size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		after := r.URL.Query().Get("publicationIdAfter")
		var items []string
		for i := 0; i < f.listed && len(items) < size; i++ {
			id := fmt.Sprintf("%024x", i+1)
			if after != "" && id <= after {
				continue
			}
			items = append(items, fmt.Sprintf(`{"id":%q,"publisherId":%q,"version":1,"addTime":1793000000000,"modTime":1793000000000,"content":{"type":"article","preview":{"title":"T"}}}`, id, publisher))
		}
		fmt.Fprintf(w, `{"drafts":[],"publications":[%s]}`, strings.Join(items, ","))
	case "/api/comments/v2/root-comments":
		if f.threaded {
			fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":50,"text":"корень"}}],"metaByCommentId":{"50":{"childrenCount":2}}}`)
			return
		}
		if f.commentPages {
			if r.URL.Query().Get("fetchAfterCommentId") == "" {
				fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":1,"text":"новый"}}],"moreItemsQueryString":"fetchAfterCommentId=1"}`)
				return
			}
			fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":77,"text":"старый"}}]}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":42,"authorSafeUid":"7","text":"Привет","createdTs":1}}],"usersById":{"7":{"displayName":"Анна"}},"metaByCommentId":{}}`)
	case "/api/comments/v2/child-comments":
		if r.URL.Query().Get("fetchAfterCommentId") == "" {
			fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":100,"text":"первый"}}],"moreItemsQueryString":"fetchAfterCommentId=100"}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":101,"text":"поздний"}}]}`)
	case "/api/comments/v2/create-comment":
		if f.status == "no-url" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"type":"internal-error"}]}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok","items":[{"entityData":{"id":99}}]}`)
	default:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(404)
	}
}

func (f *fakeDzen) writePublication(w http.ResponseWriter) {
	state, _ := json.Marshal(f.state)
	images, _ := json.Marshal(f.images)
	url := ""
	if f.status == "published" && !f.noCommonURL {
		url = "https://dzen.ru/a/slug"
	}
	publishTime := 0
	if f.status == "published" {
		publishTime = 1793000000000
	}
	fmt.Fprintf(w, `{"publications":[{"id":%q,"publisherId":%q,"version":%d,"publishTime":%d,"commonUrl":%q,"commentsFlagState":"on","visibleComments":"visible","delayedPublicationFlagState":"off","privateData":{"status":%q,"draftStatus":"no-changes","snippetFrozen":%v},"content":{"type":"article","modTime":%d,"preview":{"title":"Заголовок","snippet":""},"articleContent":{"contentState":%s}}}],"images":%s}`, pubHex, publisher, f.version, publishTime, url, f.status, f.frozen, f.version, state, images)
}

func (f *fakeDzen) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if strings.HasPrefix(r, "POST") || strings.HasPrefix(r, "PUT") {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeDzen) reset() {
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
}

type result struct {
	code           int
	stdout, stderr string
}

type harness struct {
	t    *testing.T
	fake *fakeDzen
	home string
	cwd  string
	tty  bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DZEN_HOME", home)
	t.Setenv("DZEN_CONFIG", "")
	t.Setenv("DZEN_PROFILE", "")
	h := &harness{t: t, fake: newFake(t), home: home, cwd: t.TempDir()}
	store := session.Store{Dir: filepath.Join(home, "sessions")}
	if err := store.Save(session.Session{Profile: "acc", Login: "author", PublisherID: publisher, Source: session.Source{Kind: "chrome"}, Cookies: []session.Cookie{{Name: "Session_id", Value: testCookie, Domain: ".dzen.ru"}}, UserAgent: "UA"}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	return h.runInput("", args...)
}

func (h *harness) runInput(input string, args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	app := App{Version: "test", Stdin: strings.NewReader(input), Stdout: &out, Stderr: &errOut, BaseURL: h.fake.URL,
		IsTTY: func() bool { return h.tty },
		Sleep: func(context.Context, time.Duration) error { return nil }, Now: time.Now,
		Importer: func(context.Context, session.ImportOptions, session.Source) ([]session.Candidate, error) {
			return []session.Candidate{{Cookies: []session.Cookie{{Name: "Session_id", Value: testCookie, Domain: ".dzen.ru"}}, UserAgent: "UA", Source: session.Source{Kind: "chrome"}}}, nil
		}}
	code := Main(context.Background(), app, args)
	if strings.Contains(out.String()+errOut.String(), testCookie) {
		h.t.Fatalf("cookie leaked in output of %v", args)
	}
	return result{code, out.String(), errOut.String()}
}

func (h *harness) file(name, body string) string {
	h.t.Helper()
	path := filepath.Join(h.cwd, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func envelope(t *testing.T, r result) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not one JSON document: %q (%v)", r.stdout, err)
	}
	return out
}

func TestPushCreatesDraftWithFlatContentAndRewritesFile(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст **статьи**.\n")
	r := h.run("push", file, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	data := envelope(t, r)["data"].(map[string]any)
	if data["publication_id"] != pubHex || data["status"] != "draft" {
		t.Fatalf("%v", data)
	}
	var save map[string]any
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	state := save["articleContent"].(map[string]any)["contentState"].(string)
	if strings.Contains(state, "draftJsState") || !strings.Contains(state, `"blocks"`) {
		t.Fatalf("not flat: %s", state)
	}
	if _, ok := save["fp"]; ok {
		t.Fatal("fp is not required and must not be sent")
	}
	if save["preview"].(map[string]any)["title"] != "Заголовок" {
		t.Fatalf("%v", save["preview"])
	}
	written, _ := os.ReadFile(file)
	if !strings.Contains(string(written), `publication_id: "`+pubHex+`"`) || !strings.Contains(string(written), "Текст **статьи**.") {
		t.Fatalf("%s", written)
	}
	if _, err := os.Stat(filepath.Join(h.cwd, ".dzen", ".gitignore")); err != nil {
		t.Fatal(err)
	}
}

func TestSecondPushWithoutEditsSendsNothing(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	if r := h.run("push", file); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	h.fake.reset()
	r := h.run("push", file)
	if r.code != 0 || !strings.Contains(r.stdout, "no changes") || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v %v", r, h.fake.mutations())
	}
}

func TestPushRefusesToOverwriteAConcurrentEdit(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	h.fake.mu.Lock()
	h.fake.version = 40
	h.fake.mu.Unlock()
	h.fake.reset()
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), "Текст.", "Правка.", 1)), 0o644)
	r := h.run("push", file, "--json")
	if r.code != 6 || envelope(t, r)["error"].(map[string]any)["code"] != "revision_conflict" || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v", r)
	}
	if forced := h.run("push", file, "--force"); forced.code != 0 || len(h.fake.mutations()) != 1 {
		t.Fatalf("%+v", forced)
	}
}

func TestDryRunNeedsNoSessionAndMakesNoRequests(t *testing.T) {
	h := newHarness(t)
	if err := os.RemoveAll(filepath.Join(h.home, "sessions")); err != nil {
		t.Fatal(err)
	}
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	r := h.run("push", file, "--dry-run", "--json")
	if r.code != 0 || envelope(t, r)["outcome"] != "dry_run_ok" {
		t.Fatalf("%+v", r)
	}
	if len(h.fake.requests) != 0 {
		t.Fatalf("network used: %v", h.fake.requests)
	}
	written, _ := os.ReadFile(file)
	if strings.Contains(string(written), "publication_id") {
		t.Fatal("dry run changed the file")
	}
}

func TestPublishRequiresConfirmationWithoutTerminal(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	h.fake.reset()
	r := h.run("publish", file, "--no-input")
	if r.code != 4 || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v", r)
	}
	r = h.run("publish", file, "--yes", "--json")
	if r.code != 0 || envelope(t, r)["outcome"] != "confirmed" {
		t.Fatalf("%+v", r)
	}
}

func TestTerminalPromptGuardsPublishing(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	h.fake.reset()
	if r := h.runInput("n\n", "publish", file); r.code != 4 || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.runInput("y\n", "publish", file); r.code != 0 || len(h.fake.mutations()) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestPushOfPublishedArticleNeedsConfirmation(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	h.run("publish", file, "--yes")
	h.fake.reset()
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), "Текст.", "Правка.", 1)), 0o644)
	if r := h.run("push", file, "--no-input"); r.code != 4 || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("push", file, "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestExpiredSessionIsReimportedOnceAndOperationRepeated(t *testing.T) {
	h := newHarness(t)
	h.fake.expired = 1
	r := h.run("draft", "list", "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestPrincipalMismatchOnImportIsRefused(t *testing.T) {
	h := newHarness(t)
	h.fake.login = "someone-else"
	if r := h.run("auth", "import", "--profile", "acc"); r.code != 3 || !strings.Contains(r.stderr, "principal_mismatch") {
		t.Fatalf("%+v", r)
	}
}

func TestDeleteProvesTheObjectIsGone(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	if r := h.run("draft", "delete", pubHex, "--no-input"); r.code != 4 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("draft", "delete", pubHex, "--yes", "--json"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("status", pubHex); r.code != 5 {
		t.Fatalf("%+v", r)
	}
}

func TestCommentListAndPost(t *testing.T) {
	h := newHarness(t)
	r := h.run("comment", "list", pubHex, "--json")
	items := envelope(t, r)["data"].(map[string]any)["items"].([]any)
	if r.code != 0 || len(items) != 1 || items[0].(map[string]any)["author"] != "Анна" {
		t.Fatalf("%+v", r)
	}
	r = h.runInput("Ответ\n", "comment", "post", pubHex, "--stdin", "--json")
	if r.code != 0 || envelope(t, r)["data"].(map[string]any)["comment_id"] != "99" {
		t.Fatalf("%+v", r)
	}
	var body map[string]any
	_ = json.Unmarshal(h.fake.bodies["/api/comments/v2/create-comment"], &body)
	if body["text"] != "Ответ" || body["documentId"] != "native:"+pubHex {
		t.Fatalf("%v", body)
	}
}

func TestCommentLengthIsCheckedLocally(t *testing.T) {
	h := newHarness(t)
	if r := h.run("comment", "post", pubHex, "--text", strings.Repeat("я", 2501)); r.code != 2 || len(h.fake.requests) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestErrorsUseStableCodesAndOneJSONDocument(t *testing.T) {
	h := newHarness(t)
	file := h.file("bad.md", "# Заголовок\n\n`код`\n")
	r := h.run("push", file, "--json")
	env := envelope(t, r)
	if r.code != 2 || env["ok"] != false || env["error"].(map[string]any)["code"] != "markdown_unsupported" {
		t.Fatalf("%+v", r)
	}
	if env["error"].(map[string]any)["diagnostics"] == nil || !strings.Contains(r.stderr, "line 3") {
		t.Fatalf("%+v", r)
	}
}

func TestFlagsWorkAfterPositionalsAndUnknownOnesFail(t *testing.T) {
	h := newHarness(t)
	if r := h.run("draft", "list", "--limit", "5", "--plain"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("draft", "list", "--nope"); r.code != 2 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("draft", "list", "--at", "x"); r.code != 2 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("nonsense"); r.code != 2 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("--version"); r.code != 0 || strings.TrimSpace(r.stdout) != "test" {
		t.Fatalf("%+v", r)
	}
	if r := h.run("publish", "--help"); r.code != 0 || !strings.Contains(r.stdout, "--at") {
		t.Fatalf("%+v", r)
	}
}

func TestLocalCommandsMakeNoNetworkCalls(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"crawl", "status"}, {"crawl", "search", "x"}, {"crawl", "sql", "select 1"}, {"crawl", "export-md"}, {"metadata"}} {
		if r := h.run(args...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	if len(h.fake.requests) != 0 {
		t.Fatalf("network used: %v", h.fake.requests)
	}
}

func TestMetadataIsAControlManifest(t *testing.T) {
	h := newHarness(t)
	var manifest map[string]any
	if err := json.Unmarshal([]byte(h.run("metadata", "--json").stdout), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["schema_version"] != "crawlkit.control.v1" || manifest["id"] != "dzen" {
		t.Fatalf("%v", manifest)
	}
}

func pngFile(t *testing.T, path string, width int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, 200))
	img.Set(0, 0, color.White)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeDzen) count(request string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == request {
			n++
		}
	}
	return n
}

func TestImageIsUploadedOnceAndBecomesItsDzenURL(t *testing.T) {
	h := newHarness(t)
	pngFile(t, filepath.Join(h.cwd, "pic.png"), 640)
	file := h.file("a.md", "# Заголовок\n\n![](pic.png \"Подпись\")\n")
	if r := h.run("push", file); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	written, _ := os.ReadFile(file)
	if !strings.Contains(string(written), "avatars.dzeninfra.ru/get-zen_doc/7/pub_a_img1/scale_1200") {
		t.Fatalf("%s", written)
	}
	var save map[string]any
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	state, _ := article.ParseContentState(save["articleContent"].(map[string]any)["contentState"].(string))
	if state.Blocks[0].Type != "atomic:image" || state.Blocks[0].Text != "Подпись" {
		t.Fatalf("%+v", state.Blocks[0])
	}
	h.fake.reset()
	if r := h.run("push", file); r.code != 0 || len(h.fake.mutations()) != 0 {
		t.Fatalf("%+v %v", r, h.fake.mutations())
	}
}

func TestUnsendableImagesFailBeforeAnyRequest(t *testing.T) {
	h := newHarness(t)
	pngFile(t, filepath.Join(h.cwd, "tiny.png"), 100)
	for name, src := range map[string]string{
		"too small":    "tiny.png",
		"missing":      "absent.png",
		"remote":       "https://example.com/a.png",
		"plain http":   "http://example.com/a.png",
		"foreign dzen": "https://avatars.dzeninfra.ru/get-zen_doc/9/other/scale_1200",
	} {
		src := src
		t.Run(name, func(t *testing.T) {
			h.fake.reset()
			file := h.file("x.md", "# Заголовок\n\n![]("+src+")\n")
			if r := h.run("push", file); r.code != 2 || len(h.fake.mutations()) != 0 {
				t.Fatalf("%+v %v", r, h.fake.mutations())
			}
		})
	}
}

func TestScheduleSendsItsTimeInUTC(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	at := time.Now().Add(48 * time.Hour).In(time.FixedZone("MSK", 3*3600)).Truncate(time.Second)
	if r := h.run("publish", file, "--at", at.Format(time.RFC3339), "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var body map[string]any
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content-and-publish"], &body)
	want := at.UTC().Format("2006-01-02T15:04:05.000Z")
	if body["delayedPublicationFlagState"] != "on" || body["requestedPublishTime"] != want {
		t.Fatalf("%v want %s", body, want)
	}
}

func TestDeleteUsesQueryParametersAndAnEmptyBody(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nТекст.\n"))
	if r := h.run("article", "delete", pubHex, "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if len(h.fake.bodies["/editor-api/v2/delete-publication"]) != 0 {
		t.Fatalf("%s", h.fake.bodies["/editor-api/v2/delete-publication"])
	}
}

func TestReplyAddressesTheThreadRoot(t *testing.T) {
	h := newHarness(t)
	if r := h.run("comment", "reply", "42", "--article", pubHex, "--text", "Ответ", "--json"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var body map[string]any
	_ = json.Unmarshal(h.fake.bodies["/api/comments/v2/create-comment"], &body)
	if body["rootCommentId"] != float64(42) || body["replyToCommentId"] != float64(42) {
		t.Fatalf("%v", body)
	}
}

func TestCommentRefusalExplainsAMissingPublicURL(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nТекст.\n"))
	h.fake.status = "no-url"
	r := h.run("comment", "post", pubHex, "--text", "x", "--json")
	if r.code != 5 || !strings.Contains(r.stderr, "public URL") {
		t.Fatalf("%+v", r)
	}
}

func TestPublishedArticleWithoutCommonURLGetsItsReaderAddress(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nТекст.\n"))
	h.fake.status, h.fake.noCommonURL = "published", true
	r := h.run("article", "show", pubHex, "--json")
	var out struct {
		Data struct {
			URL         string `json:"url"`
			PublishedAt string `json:"published_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || r.code != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if want := "https://dzen.ru/a/qqqqqqqqqqqqqqqq"; out.Data.URL != want {
		t.Fatalf("url %q, want %q", out.Data.URL, want)
	}
	if out.Data.PublishedAt != "2026-10-26T07:33:20Z" {
		t.Fatalf("published_at %q", out.Data.PublishedAt)
	}
	if got, _ := publicationID(out.Data.URL); got != pubHex {
		t.Fatalf("url does not round-trip: %q", got)
	}
	h.fake.status = "draft"
	r = h.run("article", "show", pubHex, "--json")
	if strings.Contains(r.stdout, "dzen.ru/a/") {
		t.Fatalf("draft got a reader address: %s", r.stdout)
	}
}

func TestArticleListSaysWhenTheLimitCutItShort(t *testing.T) {
	h := newHarness(t)
	h.fake.listed = 3
	var out struct {
		Data struct {
			Items []struct {
				CreatedAt   string `json:"created_at"`
				PublishedAt string `json:"published_at"`
			} `json:"items"`
			HasMore bool `json:"has_more"`
		} `json:"data"`
	}
	for limit, want := range map[string]struct {
		n    int
		more bool
	}{"2": {2, true}, "3": {3, false}, "5": {3, false}} {
		r := h.run("article", "list", "--limit", limit, "--json")
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || r.code != 0 {
			t.Fatalf("%+v %v", r, err)
		}
		if len(out.Data.Items) != want.n || out.Data.HasMore != want.more {
			t.Fatalf("limit %s: %d items, has_more %v", limit, len(out.Data.Items), out.Data.HasMore)
		}
		if out.Data.Items[0].CreatedAt != "2026-10-26T07:33:20Z" || out.Data.Items[0].PublishedAt != "" {
			t.Fatalf("dates %+v", out.Data.Items[0])
		}
	}
}

func TestReaderAddressMatchesAKnownPublishedArticle(t *testing.T) {
	p := dzen.Publication{ID: "6a4da7f623afe7791738102b", Status: "published"}
	if got, want := publicURL(p), "https://dzen.ru/a/ak2n9iOv53kXOBAr"; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

func TestPublicationIDFormats(t *testing.T) {
	for input, want := range map[string]string{
		"0123456789abcdef01234567": "0123456789abcdef01234567",
		"https://dzen.ru/profile/editor/id/" + publisher + "/0123456789abcdef01234567/edit": "0123456789abcdef01234567",
		"https://dzen.ru/a/ASNFZ4mrze8BI0Vn?x=1":                                            "0123456789abcdef01234567",
	} {
		got, err := publicationID(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
	if _, err := publicationID("nothing"); err == nil {
		t.Fatal("accepted garbage")
	}
}

func TestUsageErrorsKeepTheJSONEnvelope(t *testing.T) {
	h := newHarness(t)
	r := h.run("--json", "push", "--nope")
	if r.code != 2 || envelope(t, r)["error"].(map[string]any)["code"] != "usage_error" {
		t.Fatalf("%+v", r)
	}
}

func TestFailedUploadResumesTheSameDraft(t *testing.T) {
	h := newHarness(t)
	pngFile(t, filepath.Join(h.cwd, "pic.png"), 640)
	file := h.file("a.md", "# Заголовок\n\n![](pic.png)\n\nТекст.\n")
	h.fake.failUpload = 1
	if r := h.run("push", file); r.code == 0 {
		t.Fatalf("%+v", r)
	}
	if h.fake.count("POST /editor-api/v2/add-publication") != 1 {
		t.Fatal("no draft was created")
	}
	written, _ := os.ReadFile(file)
	if !strings.Contains(string(written), pubHex) || !strings.Contains(string(written), "Текст.") {
		t.Fatalf("identity or body lost: %s", written)
	}
	if r := h.run("push", file); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if h.fake.count("POST /editor-api/v2/add-publication") != 1 {
		t.Fatal("retrying created a second draft")
	}
}

func TestExpiredSessionDuringPushDoesNotCreateASecondDraft(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.fake.expireOn = "/editor-api/v2/update-publication-content"
	if r := h.run("push", file); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if h.fake.count("POST /editor-api/v2/add-publication") != 1 || h.fake.count("POST /editor-api/v2/update-publication-content") != 1 {
		t.Fatalf("%v", h.fake.requests)
	}
}

func TestDiffShowsSettingsChanges(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "---\ntitle: Заголовок\n---\n\nТекст.\n")
	h.run("push", file)
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), `comments: "on"`, `comments: "off"`, 1)), 0o644)
	r := h.run("diff", file)
	if r.code != 0 || !strings.Contains(r.stdout, "+comments: off") {
		t.Fatalf("%+v", r)
	}
}

func TestRemovingSnippetUnfreezesIt(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "---\ntitle: Заголовок\nsnippet: Своё\n---\n\nТекст.\n")
	h.run("push", file)
	var save map[string]any
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	if save["snippetFrozen"] != true {
		t.Fatalf("%v", save)
	}
	h.fake.mu.Lock()
	h.fake.frozen = true
	h.fake.mu.Unlock()
	if r := h.run("pull", pubHex, "--dir", h.cwd, "--force"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	pulled := filepath.Join(h.cwd, "заголовок.md")
	written, err := os.ReadFile(pulled)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(written), "\n") {
		if !strings.HasPrefix(line, "snippet:") {
			kept = append(kept, line)
		}
	}
	_ = os.WriteFile(pulled, []byte(strings.Join(kept, "\n")), 0o644)
	if r := h.run("push", pulled); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	if save["snippetFrozen"] != false {
		t.Fatalf("%v", save)
	}
}

func TestDeleteDoesNotTrustAGenericReadFailure(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nТекст.\n"))
	h.fake.badReadAfterDelete = true
	r := h.run("article", "delete", pubHex, "--yes", "--json")
	if r.code != 8 {
		t.Fatalf("%+v", r)
	}
}

func TestCommentShowFollowsPagination(t *testing.T) {
	h := newHarness(t)
	h.fake.commentPages = true
	r := h.run("comment", "show", "77", "--article", pubHex, "--json")
	if r.code != 0 || envelope(t, r)["data"].(map[string]any)["text"] != "старый" {
		t.Fatalf("%+v", r)
	}
}

func TestCoverMustBeAnImageOfTheBody(t *testing.T) {
	h := newHarness(t)
	pngFile(t, filepath.Join(h.cwd, "body.png"), 640)
	pngFile(t, filepath.Join(h.cwd, "other.png"), 640)
	outside := h.file("out.md", "---\ntitle: Заголовок\ncover: other.png\n---\n\n![](body.png)\n")
	for _, args := range [][]string{{"push", outside, "--dry-run"}, {"push", outside}} {
		h.fake.reset()
		if r := h.run(args...); r.code != 2 || !strings.Contains(r.stderr, "validation_cover_not_in_body") || len(h.fake.mutations()) != 0 {
			t.Fatalf("%v: %+v %v", args, r, h.fake.mutations())
		}
	}
	inside := h.file("in.md", "---\ntitle: Заголовок\ncover: ./body.png\n---\n\n![](body.png)\n")
	if r := h.run("push", inside); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	var save struct {
		Preview struct {
			Image struct {
				ID string `json:"id"`
			} `json:"image"`
		} `json:"preview"`
	}
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	if save.Preview.Image.ID != "img1" {
		t.Fatalf("%+v", save)
	}
}

func TestUploadCrashIsAKnownFailureThatSavesNothing(t *testing.T) {
	h := newHarness(t)
	pngFile(t, filepath.Join(h.cwd, "pic.png"), 640)
	file := h.file("a.md", "# Заголовок\n\n![](pic.png)\n")
	h.fake.crashUpload = 2
	r := h.run("push", file, "--json")
	failure, _ := envelope(t, r)["error"].(map[string]any)
	if r.code != 5 || failure["code"] != "remote_validation" {
		t.Fatalf("%+v", r)
	}
	if _, saved := h.fake.bodies["/editor-api/v2/update-publication-content"]; saved {
		t.Fatal("article was saved after the image failed")
	}
}

func TestCommentsOffIsSentWithInvisibleComments(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "---\ntitle: Заголовок\ncomments: \"off\"\n---\n\nТекст.\n")
	h.run("push", file)
	var save map[string]any
	_ = json.Unmarshal(h.fake.bodies["/editor-api/v2/update-publication-content"], &save)
	if save["commentsFlagState"] != "off" || save["visibleComments"] != "invisible" {
		t.Fatalf("%v", save)
	}
}

func TestDiscardedSettingIsReportedAsAWarning(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "---\ntitle: Заголовок\ncomments: \"off\"\n---\n\nТекст.\n")
	r := h.run("push", file, "--json")
	warnings, _ := envelope(t, r)["data"].(map[string]any)["warnings"].([]any)
	if r.code != 0 || len(warnings) == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCaptchaRefusalDoesNotCauseAFalseConflict(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), "Текст.", "Правка.", 1)), 0o644)
	h.fake.captchaOnPublish = 1
	if r := h.run("publish", file, "--yes", "--no-captcha"); r.code != 4 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("publish", file, "--yes"); r.code != 0 {
		t.Fatalf("retry after captcha: %+v", r)
	}
}

func TestUnknownCommandInJSONModeIsOneDocument(t *testing.T) {
	h := newHarness(t)
	r := h.run("nonsense", "--json")
	if r.code != 2 || envelope(t, r)["ok"] != false {
		t.Fatalf("%+v", r)
	}
}

func TestFrontMatterEditMakesTheCopyModified(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), "Заголовок", "Новый заголовок", 1)), 0o644)
	r := h.run("status", file, "--json")
	data := envelope(t, r)["data"].(map[string]any)
	if r.code != 0 || data["local"] != "modified" {
		t.Fatalf("%+v", r)
	}
}

func TestLargeDiffOfASmallEditStaysSmall(t *testing.T) {
	lines := strings.Repeat("строка\n", 50000)
	got := unifiedDiff("a", "b", lines+"старое\n"+lines, lines+"новое\n"+lines)
	if strings.Count(got, "\n") != 4 || !strings.Contains(got, "+новое") {
		t.Fatalf("%d lines", strings.Count(got, "\n"))
	}
}

func TestForcedPushWithoutChangesAcceptsTheRemoteRevision(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	h.fake.mu.Lock()
	h.fake.version = 40
	h.fake.mu.Unlock()
	if r := h.run("push", file, "--force"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("status", file, "--json"); r.code != 0 || envelope(t, r)["data"].(map[string]any)["local"] != "clean" {
		t.Fatalf("%+v", r)
	}
}

func TestFailedFirstSaveResumesTheSameDraft(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.fake.failSaves = 1
	if r := h.run("push", file); r.code == 0 {
		t.Fatalf("%+v", r)
	}
	if r := h.run("push", file); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if h.fake.count("POST /editor-api/v2/add-publication") != 1 {
		t.Fatal("retry created a second draft")
	}
}

func TestLateJSONFlagShapesAnEarlierUsageError(t *testing.T) {
	h := newHarness(t)
	r := h.run("push", "a.md", "--unknown", "--json")
	if r.code != 2 || envelope(t, r)["error"].(map[string]any)["code"] != "usage_error" {
		t.Fatalf("%+v", r)
	}
}

func TestSettingsOnlyPushIsReportedAsAChange(t *testing.T) {
	h := newHarness(t)
	file := h.file("a.md", "# Заголовок\n\nТекст.\n")
	h.run("push", file)
	written, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(written), "Заголовок", "Другой", 1)), 0o644)
	r := h.run("push", file, "--json")
	data := envelope(t, r)["data"].(map[string]any)
	if r.code != 0 || data["changed"] != true || len(data["changes"].([]any)) == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCommentRepliesFollowPagination(t *testing.T) {
	h := newHarness(t)
	h.fake.threaded = true
	r := h.run("comment", "show", "101", "--article", pubHex, "--json")
	if r.code != 0 || envelope(t, r)["data"].(map[string]any)["text"] != "поздний" {
		t.Fatalf("%+v", r)
	}
}

func TestInvalidArticleCapStartsNoSyncRun(t *testing.T) {
	h := newHarness(t)
	if r := h.run("crawl", "sync", "chan", "--max-articles", "x"); r.code != 2 {
		t.Fatalf("%+v", r)
	}
	r := h.run("crawl", "sql", "select count(*) from sync_runs")
	if r.code != 0 || !strings.HasSuffix(strings.TrimSpace(r.stdout), "\n0") {
		t.Fatalf("%+v", r)
	}
}

func TestShowReportsTheAdvertisingDisclosure(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nТекст.\n\nРеклама. ООО \"КУПИШУЗ\". ИНН 7705935687. erid: 2SDnjcUSQQQ\n"))
	r := h.run("article", "show", pubHex, "--json")
	var out struct {
		Data struct {
			AdMarked  bool     `json:"ad_marked"`
			AdMarking []string `json:"ad_marking"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || r.code != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if !out.Data.AdMarked || len(out.Data.AdMarking) != 1 || !strings.HasPrefix(out.Data.AdMarking[0], "Реклама. ООО") {
		t.Fatalf("%+v", out.Data)
	}
}

func TestShowReportsNoDisclosureForAPlainArticle(t *testing.T) {
	h := newHarness(t)
	h.run("push", h.file("a.md", "# Заголовок\n\nЭто не реклама, просто совет.\n"))
	r := h.run("draft", "show", pubHex, "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"ad_marked": false`) || !strings.Contains(r.stdout, `"ad_marking": []`) {
		t.Fatalf("%+v", r)
	}
}
