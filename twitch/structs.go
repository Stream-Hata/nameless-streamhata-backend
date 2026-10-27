package twitch

type TwitchOauthREsponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Scope        []string `json:"scope"`
	TokenType    string   `json:"token_type"`
}

type TwitchUser struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	DisplayName     string `json:"display_name"`
	ProfileImageURL string `json:"profile_image_url"`
}

type User struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Username     string `json:"username"`
	TwitchUserID string `json:"twitchId"`
	Avatar       string `json:"avatar,omitempty"`
}

type TwitchUsersResponse struct {
	Data []TwitchUser `json:"data"`
}

type TwitchFollowersResponse struct {
	Total int `json:"total"`
}

type TwitchSubscribersResponse struct {
	Total  int `json:"total"`
	Points int `json:"points"`
}

type TwitchChattersResponse struct {
	Total int `json:"total"`
}

type TwitchStreamsResponse struct {
	Data []TwitchStream `json:"data"`
}

type TwitchStream struct {
	ID           string   `json:"id"`
	UserID       string   `json:"user_id"`
	UserLogin    string   `json:"user_login"`
	UserName     string   `json:"user_name"`
	GameID       string   `json:"game_id"`
	GameName     string   `json:"game_name"`
	Status       string   `json:"status"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	ViewerCount  int      `json:"viewer_count"`
	StartedAt    string   `json:"started_at"`
	Language     string   `json:"language"`
	ThumbnailURL string   `json:"thumbnail_url"`
	Tags         []string `json:"tags"`
}

type TwitchUserResponse struct {
	Data []TwitchUser `json:"data"`
}

type TwitchValidateResponse struct {
	Valid bool `json:"valid"`
	User  User `json:"user"`
}
type TwitchStreamInfo struct {
	Online       bool     `json:"online"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	GameID       string   `json:"gameId"`
	GameName     string   `json:"gameName"`
	ViewerCount  int      `json:"viewerCount"`
	StartedAt    string   `json:"startedAt"`
	Language     string   `json:"language"`
	ThumbnailURL string   `json:"thumbnailUrl"`
	Tags         []string `json:"tags"`
}

type ValidationResponse struct {
	Valid  bool             `json:"valid"`
	User   User             `json:"user"`
	Stats  TwitchUserStats  `json:"state"`
	Stream TwitchStreamInfo `json:"steam"`
}

type TwitchUserInfo struct {
	ID          int    `json:"id"`
	TwitchID    string `json:"twitchId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type TwitchUserStats struct {
	Followers   int `json:"followers"`
	Subscribers int `json:"subscribers"`
	Chatters    int `json:"chatters"`
}

type ChatUser struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

type ChatEmote struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Provider string `json:"provider"`
}

type ChatBadge struct {
	SetID   string `json:"set_id"`
	Version string `json:"version"`
	Info    string `json:"info,omitempty"`
}

type TwitchChatMessage struct {
	MessageID string `json:"message_id"`

	Broadcaster ChatUser `json:"broadcaster"`
	Author      ChatUser `json:"author"`

	Text   string      `json:"text"`
	SentAt string      `json:"sent_at"`
	Color  string      `json:"color,omitempty"`
	Emotes []ChatEmote `json:"emotes,omitempty"`
	Badges []ChatBadge `json:"badges,omitempty"`
}
