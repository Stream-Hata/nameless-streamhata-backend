package twitch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"net/http"
	"os"
	"time"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
)

const sessionCookie = "streamhata-twitch-session"
const sessionLifetime = 30 * 24 * time.Hour
const sessionPrefix = "shs_"

type Sessions struct {
	db     *sql.DB
	users  data_base.Database[data_base.User]
	tokens data_base.Database[data_base.Token]
}

func NewSessions(db *sql.DB, users data_base.Database[data_base.User], tokens data_base.Database[data_base.Token]) *Sessions {
	return &Sessions{db: db, users: users, tokens: tokens}
}

func (s *Sessions) Account(request *http.Request) (*data_base.User, *data_base.Token, error) {
	var token *data_base.Token
	var err error
	if userID, present, sessionErr := BrowserSessionUserID(s.db, request); present {
		err = sessionErr
		if err == nil {
			token, err = s.tokens.Find(data_base.Where{Column: "user_id", Value: userID})
		}
	} else {
		cookie, cookieErr := request.Cookie("e-hub-refresh-token")
		if cookieErr != nil || cookie.Value == "" {
			return nil, nil, http.ErrNoCookie
		}
		digest, digestErr := data_base.RefreshTokenDigest(cookie.Value)
		if digestErr != nil {
			return nil, nil, digestErr
		}
		token, err = s.tokens.Find(data_base.Where{Column: "refresh_token_hash", Value: digest})
	}
	if err != nil {
		return nil, nil, err
	}
	user, err := s.users.Find(data_base.Where{Column: "id", Value: token.UserID})
	if err != nil {
		return nil, nil, err
	}
	if user.Banned || user.TwitchUserID <= 0 {
		return nil, nil, sql.ErrNoRows
	}
	return user, token, nil
}

func (s *Sessions) Resume(ctx context.Context, secret string) (int, error) {
	id, err := lookupSessionUserID(ctx, s.db, secret)
	if err != nil {
		return 0, err
	}
	_, err = s.renew(ctx, secret)
	return id, err
}

func (s *Sessions) renewBrowser(w http.ResponseWriter, r *http.Request, secret string) error {
	expires, err := s.renew(r.Context(), secret)
	if err == nil && !expires.IsZero() {
		http.SetCookie(w, newSessionCookie(secret, expires, int(sessionLifetime/time.Second)))
	}
	return err
}

func (s *Sessions) renew(ctx context.Context, secret string) (time.Time, error) {
	digest := sha256.Sum256([]byte(secret))
	now := time.Now()
	var expires time.Time
	err := s.db.QueryRowContext(ctx, `
		UPDATE twitch_session SET expires_at = $3
		WHERE digest = $1 AND expires_at > now() AND expires_at <= $2
		RETURNING expires_at`, digest[:], now.Add(7*24*time.Hour), now.Add(sessionLifetime)).Scan(&expires)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	return expires, err
}

// BrowserSessionUserID distinguishes an absent cookie from an invalid session
// so callers can support the legacy cookie without accepting a revoked session.
func BrowserSessionUserID(db *sql.DB, request *http.Request) (int, bool, error) {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil {
		return 0, false, nil
	}
	if db == nil {
		return 0, true, sql.ErrNoRows
	}
	id, err := lookupSessionUserID(request.Context(), db, cookie.Value)
	return id, true, err
}

func lookupSessionUserID(ctx context.Context, db *sql.DB, secret string) (int, error) {
	digest := sha256.Sum256([]byte(secret))
	var userID int
	err := db.QueryRowContext(ctx, `
		SELECT u.id FROM twitch_session s JOIN users u ON u.id = s.user_id
		WHERE s.digest = $1 AND s.expires_at > now() AND u.banned = false`, digest[:]).Scan(&userID)
	return userID, err
}

func (s *Sessions) Mint(ctx context.Context, userID int) (string, time.Time, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", time.Time{}, err
	}
	value := sessionPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(value))
	expires := time.Now().Add(sessionLifetime)
	if _, err := s.db.ExecContext(ctx,
		`WITH cleanup AS (DELETE FROM twitch_session WHERE expires_at <= now())
		INSERT INTO twitch_session (digest, user_id, expires_at) VALUES ($1, $2, $3)`, digest[:], userID, expires); err != nil {
		return "", time.Time{}, err
	}
	return value, expires, nil
}

func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request, userID int) error {
	value, expires, err := s.Mint(r.Context(), userID)
	if err != nil {
		return err
	}
	if previous, err := r.Cookie(sessionCookie); err == nil {
		previousDigest := sha256.Sum256([]byte(previous.Value))
		_, _ = s.db.ExecContext(r.Context(), `DELETE FROM twitch_session WHERE digest = $1`, previousDigest[:])
	}
	http.SetCookie(w, newSessionCookie(value, expires, int(sessionLifetime/time.Second)))
	return nil
}

func (s *Sessions) revoke(w http.ResponseWriter, r *http.Request) error {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		digest := sha256.Sum256([]byte(cookie.Value))
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM twitch_session WHERE digest = $1`, digest[:]); err != nil {
			return err
		}
	}
	http.SetCookie(w, newSessionCookie("", time.Unix(1, 0), -1))
	return nil
}

func newSessionCookie(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", Domain: cookieDomain(),
		HttpOnly: true, Secure: os.Getenv("IS_PROD") == "1", SameSite: http.SameSiteLaxMode,
		Expires: expires, MaxAge: maxAge,
	}
}
