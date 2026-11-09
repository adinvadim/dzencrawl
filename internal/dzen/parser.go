package dzen

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/adinvadim/dzencrawl/internal/article"
)

// ParseArticle extracts an article from a public `?output=1` page.
func ParseArticle(rawHTML, fallbackURL, channel string) (Article, []Comment, CommentContext, error) {
	root, err := parseSSR(rawHTML)
	if err != nil {
		return Article{}, nil, CommentContext{}, err
	}
	ssr := asMap(root["ssrData"])
	response := asMap(asMap(asMap(ssr["publishersResponse"])["data"])["data"])
	publication := asMap(response["publication"])
	publisher := asMap(response["publisher"])
	if len(publication) == 0 {
		return Article{}, nil, CommentContext{}, errors.New("Dzen publication missing from SSR data")
	}
	content := asMap(publication["content"])
	preview := asMap(content["preview"])
	stats := asMap(publication["publicationStatistics"])
	title := str(preview["title"])
	if title == "" {
		title = str(response["title"])
	}
	likes, socialComments, socialKnown := socialMetrics(ssr)
	robotMeta := asMap(asMap(ssr["commentsForRobot"])["meta"])
	_, robotKnown := robotMeta["totalCommentsCount"]
	contentState := str(asMap(content["articleContent"])["contentState"])
	article := Article{
		ID: str(publication["id"]), PublisherID: str(publication["publisherId"]), Channel: channel,
		Title: title, URL: fallbackURL, Markdown: ContentMarkdown(contentState, response["images"]),
		ContentState: contentState, ProviderVersion: integer(publication["version"]), State: "published", CommentsCountKnown: robotKnown || socialKnown,
		PublishedAt: integer(publication["publishTime"]), ModifiedAt: integer(publication["modificationTime"]),
		Views: integer(stats["views"]), ViewsTillEnd: integer(stats["viewsTillEnd"]),
		TimeToReadSeconds: firstInt(content["timeToReadSeconds"], preview["timeToReadSeconds"]),
		Likes:             likes,
		CommentsCount:     firstInt(robotMeta["totalCommentsCount"], socialComments),
	}
	if article.ID == "" {
		return Article{}, nil, CommentContext{}, errors.New("Dzen publication id is empty")
	}
	raw, _ := json.Marshal(map[string]any{"publisher": publisher, "publication": publication})
	article.RawJSON = string(raw)
	commentsData := asMap(ssr["commentsForRobot"])
	comments, users, meta := parseCommentResponse(commentsData, article.ID, "")
	for i := range comments {
		if comments[i].AuthorName == "" {
			comments[i].AuthorName = users[comments[i].AuthorID]
		}
	}
	ctx := CommentContext{
		PublisherID: article.PublisherID, DocumentID: str(publication["commentsDocumentId"]),
		RID: str(asMap(ssr["zenParams"])["zenReqid"]), ServerTS: integer(commentsData["serverTs"]),
		MetaByID: meta, Users: users,
	}
	return article, comments, ctx, nil
}

func parseSSR(html string) (map[string]any, error) {
	const marker = "var _params=("
	searchFrom := 0
	found := false
	for {
		relative := strings.Index(html[searchFrom:], marker)
		if relative < 0 {
			break
		}
		found = true
		start := searchFrom + relative + len(marker)
		dec := json.NewDecoder(strings.NewReader(html[start:]))
		dec.UseNumber()
		var root map[string]any
		if err := dec.Decode(&root); err == nil {
			ssr := asMap(root["ssrData"])
			if len(asMap(ssr["publishersResponse"])) > 0 {
				return root, nil
			}
		}
		searchFrom = start
	}
	if !found {
		return nil, errors.New("Dzen SSR marker not found")
	}
	return nil, errors.New("Dzen publication SSR data not found")
}

func parseCommentResponse(data map[string]any, articleID, parentID string) ([]Comment, map[string]string, map[string]commentMeta) {
	users := map[string]string{}
	for id, raw := range asMap(data["usersById"]) {
		u := asMap(raw)
		name := str(u["displayName"])
		if name == "" {
			name = str(asMap(u["publisher"])["name"])
		}
		users[id] = name
	}
	meta := map[string]commentMeta{}
	for id, raw := range asMap(data["metaByCommentId"]) {
		m := asMap(raw)
		meta[id] = commentMeta{Children: int(integer(m["childrenCount"])), Likes: integer(m["likes"]), Dislikes: integer(m["dislikes"])}
	}
	var out []Comment
	appendItems := func(items []any, parent string) {
		for _, raw := range items {
			wrapper := asMap(raw)
			entity := asMap(wrapper["entityData"])
			id := strconv.FormatInt(integer(entity["id"]), 10)
			if id == "0" {
				id = str(entity["id"])
			}
			m := meta[id]
			b, _ := json.Marshal(wrapper)
			out = append(out, Comment{ID: id, ArticleID: articleID, ParentID: first(str(entity["rootCommentId"]), parent), AuthorID: first(str(entity["authorSafeUid"]), str(entity["authorUid"])), Text: str(entity["text"]), CreatedAt: integer(entity["createdTs"]), Likes: m.Likes, Dislikes: m.Dislikes, RawJSON: string(b)})
		}
	}
	appendItems(asSlice(data["items"]), parentID)
	for root, raw := range asMap(data["subthreadByCommentId"]) {
		appendItems(asSlice(raw), root)
	}
	for i := range out {
		out[i].AuthorName = users[out[i].AuthorID]
	}
	return out, users, meta
}

// ContentMarkdown renders a contentState to article Markdown. Blocks without a
// Markdown form appear as dzen:raw anchors.
func ContentMarkdown(contentState string, images any) string {
	state, err := article.ParseContentState(contentState)
	if err != nil {
		return ""
	}
	directory := article.Images{}
	if raw, err := json.Marshal(images); err == nil {
		_ = json.Unmarshal(raw, &directory)
	}
	var nodes []article.Node
	for _, b := range state.Blocks {
		if b.Type != "marker" {
			nodes = append(nodes, article.NodeFromBlock(b, state.EntityMap, directory))
		}
	}
	return strings.TrimSpace(article.RenderBody(nodes))
}

func socialMetrics(ssr map[string]any) (likes, comments int64, known bool) {
	items := asSlice(asMap(ssr["socialMetaResponse"])["items"])
	if len(items) == 0 {
		return 0, 0, false
	}
	meta := asMap(asMap(items[0])["metaInfo"])
	_, known = meta["commentsCount"]
	return integer(meta["likeCount"]), integer(meta["commentsCount"]), known
}
func asMap(v any) map[string]any {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return map[string]any{}
}
func asSlice(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return nil
}
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
func integer(v any) int64 {
	s := str(v)
	if x, err := strconv.ParseInt(s, 10, 64); err == nil {
		return x
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}
func firstInt(values ...any) int64 {
	for _, value := range values {
		if n := integer(value); n != 0 {
			return n
		}
	}
	return 0
}
func first(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

type commentMeta struct {
	Children        int
	Likes, Dislikes int64
}
type CommentContext struct {
	PublisherID, DocumentID, RID string
	ServerTS                     int64
	MetaByID                     map[string]commentMeta
	Users                        map[string]string
}

// ParseComments reads one comments API page. It returns the comments and the
// ids of roots that have replies.
func ParseComments(body []byte, publicationID, parentID string) ([]Comment, []string, error) {
	var response commentResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, nil, err
	}
	comments, _, meta := parseCommentResponse(response.mapValue(), publicationID, parentID)
	var withReplies []string
	for id, m := range meta {
		if m.Children > 0 {
			withReplies = append(withReplies, id)
		}
	}
	return dedupeComments(comments), withReplies, nil
}
