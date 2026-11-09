package dzen

import (
	"encoding/json"

	"github.com/adinvadim/dzencrawl/internal/article"
)

// Publication is the studio's read model of an article or draft.
type Publication struct {
	ID                   string
	PublisherID          string
	Version              int64
	Status               string
	DraftStatus          string
	HasChanges           bool
	CommonURL            string
	AddTime              int64
	ModTime              int64
	PublishTime          int64
	Type                 string
	Title                string
	Snippet              string
	CoverID              string
	ContentState         string
	SnippetFrozen        bool
	HasNativeAds         bool
	CommentsFlagState    string
	VisibleComments      string
	VisibilityType       string
	CustomCommentsTitle  string
	DelayedFlag          string
	RequestedPublishTime string
	PremiumTariffs       json.RawMessage
	Images               article.Images
	Raw                  json.RawMessage
}

type wirePublication struct {
	ID                  string          `json:"id"`
	PublisherID         string          `json:"publisherId"`
	Version             int64           `json:"version"`
	CommonURL           string          `json:"commonUrl"`
	AddTime             int64           `json:"addTime"`
	ModTime             int64           `json:"modTime"`
	PublishTime         int64           `json:"publishTime"`
	HasNativeAds        bool            `json:"hasNativeAds"`
	CommentsFlagState   string          `json:"commentsFlagState"`
	VisibleComments     string          `json:"visibleComments"`
	VisibilityType      string          `json:"visibilityType"`
	CustomCommentsTitle string          `json:"customCommentsTitle"`
	DelayedFlag         string          `json:"delayedPublicationFlagState"`
	RequestedPublishIso string          `json:"requestedPublishTimeIso"`
	PremiumTariffs      json.RawMessage `json:"premiumTariffs"`
	PrivateData         struct {
		HasChanges    bool   `json:"hasChanges"`
		SnippetFrozen bool   `json:"snippetFrozen"`
		DraftStatus   string `json:"draftStatus"`
		Status        string `json:"status"`
	} `json:"privateData"`
	Content struct {
		Type    string `json:"type"`
		ModTime int64  `json:"modTime"`
		Preview struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Image   *struct {
				ID string `json:"id"`
			} `json:"image"`
			ImageID string `json:"imageId"`
		} `json:"preview"`
		ArticleContent struct {
			ContentState json.RawMessage `json:"contentState"`
		} `json:"articleContent"`
	} `json:"content"`
}

// DecodePublication reads one studio publication object.
func DecodePublication(raw json.RawMessage, images article.Images) (Publication, error) {
	var w wirePublication
	if err := json.Unmarshal(raw, &w); err != nil {
		return Publication{}, err
	}
	p := Publication{
		ID: w.ID, PublisherID: w.PublisherID, Version: w.Version, Status: w.PrivateData.Status, DraftStatus: w.PrivateData.DraftStatus,
		HasChanges: w.PrivateData.HasChanges, CommonURL: w.CommonURL, AddTime: w.AddTime, ModTime: w.Content.ModTime, PublishTime: w.PublishTime,
		Type: w.Content.Type, Title: w.Content.Preview.Title, Snippet: w.Content.Preview.Snippet, CoverID: w.Content.Preview.ImageID,
		ContentState: contentStateString(w.Content.ArticleContent.ContentState), SnippetFrozen: w.PrivateData.SnippetFrozen, HasNativeAds: w.HasNativeAds,
		CommentsFlagState: w.CommentsFlagState, VisibleComments: w.VisibleComments, VisibilityType: w.VisibilityType, CustomCommentsTitle: w.CustomCommentsTitle,
		DelayedFlag: w.DelayedFlag, RequestedPublishTime: w.RequestedPublishIso, PremiumTariffs: w.PremiumTariffs, Images: images, Raw: raw,
	}
	if w.Content.Preview.Image != nil && w.Content.Preview.Image.ID != "" {
		p.CoverID = w.Content.Preview.Image.ID
	}
	return p, nil
}

// contentStateString accepts the string form and a nested JSON object.
func contentStateString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}
