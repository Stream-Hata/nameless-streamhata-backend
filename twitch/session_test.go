package twitch

import (
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
)

type lookupRepository[T comparable] struct {
	data_base.Database[T]
	find func(data_base.Where) (*T, error)
}

func (repo lookupRepository[T]) Find(where data_base.Where) (*T, error) {
	return repo.find(where)
}

func TestBrowserSessionSurvivesTwitchTokenRotation(t *testing.T) {
	dsn := os.Getenv("STREAMHATA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires a disposable PostgreSQL database with migrations applied")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	account := data_base.User{TwitchUserID: time.Now().UnixNano()}
	if err := db.QueryRow(`INSERT INTO users (twitch_user_id) VALUES ($1) RETURNING id`, account.TwitchUserID).
		Scan(&account.ID); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, account.ID)
	token := data_base.Token{UserID: account.ID, AccessToken: "original", RefreshToken: "original-refresh"}
	sessions := NewSessions(db, lookupRepository[data_base.User]{find: func(where data_base.Where) (*data_base.User, error) {
		if where.Column != "id" || where.Value != account.ID {
			return nil, sql.ErrNoRows
		}
		return &account, nil
	}}, lookupRepository[data_base.Token]{find: func(where data_base.Where) (*data_base.Token, error) {
		if where.Column != "user_id" || where.Value != account.ID {
			return nil, sql.ErrNoRows
		}
		return &token, nil
	}})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/twitch/callback", nil)
	if err := sessions.Issue(response, request, account.ID); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Result().Cookies() {
		request.AddCookie(cookie)
	}
	token.AccessToken = "rotated"
	token.RefreshToken = "rotated-refresh"
	user, credentials, err := sessions.Account(request)
	if err != nil || user.ID != account.ID || credentials.AccessToken != "rotated" {
		t.Fatalf("session did not resolve rotated Twitch credentials: %v", err)
	}
	cookie, err := request.Cookie(sessionCookie)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	if _, err := db.Exec(`UPDATE twitch_session SET expires_at = now() + interval '1 day' WHERE digest = $1`, digest[:]); err != nil {
		t.Fatal(err)
	}
	renewal := httptest.NewRecorder()
	if err := sessions.renewBrowser(renewal, request, cookie.Value); err != nil {
		t.Fatal(err)
	}
	refreshed := renewal.Result().Cookies()
	if len(refreshed) != 1 || refreshed[0].Value != cookie.Value || time.Until(refreshed[0].Expires) < 29*24*time.Hour {
		t.Fatal("active browser session was not renewed")
	}
	if err := sessions.revoke(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessions.Account(request); err == nil {
		t.Fatal("revoked session still resolves to an account")
	}
}
