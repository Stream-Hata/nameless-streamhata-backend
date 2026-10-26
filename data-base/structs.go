package data_base

import (
	"database/sql"
	"time"
)

type AppMode struct {
	ID   int    `db:"id"`
	Name string `db:"name"`
}

type User struct {
	ID           int           `db:"id"`
	CreatedAt    time.Time     `db:"created_at"`
	UpdatedAt    time.Time     `db:"updated_at"`
	Username     string        `db:"username"`
	DisplayName  string        `db:"display_name"`
	TwitchToken  sql.NullInt64 `db:"twitch_token"`
	TwitchUserID int64         `db:"twitch_user_id"`
	Avatar       string        `db:"avatar"`
	Banned       bool          `db:"banned"`
	BanReason    string        `db:"ban_reason"`
	BannedAt     sql.NullTime  `db:"banned_at"`
	SteamID      string        `db:"steam_id"`
	SteamLinked  sql.NullTime  `db:"steam_linked_at"`
}

func (User) TableName() string {
	return "users"
}

type AppState struct {
	ID   int    `db:"id"`
	Mode string `db:"mode"`
}

type Timer struct {
	ID      int `db:"id"`
	Minutes int `db:"minutes"`
	Seconds int `db:"seconds"`
	Hours   int `db:"hours"`
}

type UserPlatform struct {
	ID                  int    `db:"id"`
	Code                string `db:"code"`
	Name                string `db:"name"`
	Icon                string `db:"icon"`
	CompletedGamesCount int    `db:"completed_games_count"`
}

type UserState struct {
	StateID int `db:"state_id"`
	UserID  int `db:"user_id"`
}

type StatePlatform struct {
	StateID    int `db:"state_id"`
	PlatformID int `db:"platform_id"`
}

type TimerState struct {
	StateID int `db:"state_id"`
	TimerID int `db:"timer_id"`
}

type Token struct {
	ID           int    `db:"id"`
	AccessToken  string `db:"access_token"`
	RefreshToken string `db:"refresh_token"`
	UserID       int    `db:"user_id"`
}

func (Token) TableName() string {
	return "tokens"
}
