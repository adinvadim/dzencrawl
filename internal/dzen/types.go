package dzen

type Article struct {
	ID                string
	PublisherID       string
	Channel           string
	Title             string
	URL               string
	Markdown          string
	PublishedAt       int64
	ModifiedAt        int64
	Views             int64
	ViewsTillEnd      int64
	TimeToReadSeconds int64
	Likes             int64
	CommentsCount     int64
	CommentsComplete  bool
	// CommentsCountKnown is false when Dzen did not report a count at all.
	CommentsCountKnown bool
	RawJSON            string
	// KeepPublicMetrics marks a source that has no views, likes or comment
	// counts, so archived values must survive this update.
	KeepPublicMetrics bool
	State             string
	ProviderVersion   int64
	ContentState      string
}

type Comment struct {
	ID         string
	ArticleID  string
	ParentID   string
	AuthorID   string
	AuthorName string
	Text       string
	CreatedAt  int64
	Likes      int64
	Dislikes   int64
	RawJSON    string
}

type ChannelPage struct {
	PageCount int `json:"pageCount"`
	Channel   struct {
		Source struct {
			Title    string `json:"title"`
			FeedLink string `json:"feed_link"`
		} `json:"source"`
	} `json:"channel"`
	Items []struct {
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
	} `json:"items"`
}
