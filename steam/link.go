package steam

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

const steamOpenID = "https://steamcommunity.com/openid/login"

type LinkService struct {
	db     *sql.DB
	steam  *Service
	public string

	libraryMu    sync.Mutex
	libraryCache map[string]cachedLibrary
}

type cachedLibrary struct {
	items   []LibraryGame
	expires time.Time
}

func NewLinkService(db *sql.DB, steam *Service, publicURL string) *LinkService {
	if strings.TrimSpace(publicURL) == "" {
		publicURL = "https://streamhata.rdpctd.dev"
	}

	return &LinkService{
		db:           db,
		steam:        steam,
		public:       strings.TrimRight(publicURL, "/"),
		libraryCache: make(map[string]cachedLibrary),
	}
}

func (s *LinkService) Register(srv Router) {
	srv.ServeEndpoint("/rgg-sync/steam/authorize", s.authorize)
	srv.ServeEndpoint("/rgg-sync/steam/callback", s.callback)
	srv.ServeEndpoint("/rgg-sync/steam/me", s.me)
	srv.ServeEndpoint("/rgg-sync/steam/library", s.library)
	srv.ServeEndpoint("/rgg-sync/steam/inventory", s.inventory)
	srv.ServeEndpoint("/rgg-sync/steam/achievements", s.achievements)
	srv.ServeEndpoint("/rgg-sync/steam/unlink", s.unlink)
}

func (s *LinkService) authorize(response http.ResponseWriter, request *http.Request) {
	login := s.login(request)
	if login == "" {
		http.Redirect(response, request, "/login?next=steam", http.StatusTemporaryRedirect)

		return
	}

	state := randomState()
	http.SetCookie(response, &http.Cookie{
		Name:     "e-hub-steam-state",
		Value:    state,
		Path:     "/",
		Domain:   os.Getenv("COOKIE_DOMAIN"),
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   os.Getenv("IS_PROD") == "1",
	})

	returnTo := s.public + "/rgg-sync/steam/callback?state=" + url.QueryEscape(state)
	params := url.Values{}
	params.Set("openid.ns", "http://specs.openid.net/auth/2.0")
	params.Set("openid.mode", "checkid_setup")
	params.Set("openid.return_to", returnTo)
	params.Set("openid.realm", s.public)
	params.Set("openid.identity", "http://specs.openid.net/auth/2.0/identifier_select")
	params.Set("openid.claimed_id", "http://specs.openid.net/auth/2.0/identifier_select")

	http.Redirect(response, request, steamOpenID+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (s *LinkService) callback(response http.ResponseWriter, request *http.Request) {
	login := s.login(request)
	if login == "" {
		http.Redirect(response, request, "/login?next=steam", http.StatusTemporaryRedirect)

		return
	}

	stateCookie, err := request.Cookie("e-hub-steam-state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != request.URL.Query().Get("state") {
		http.Error(response, "invalid steam state", http.StatusBadRequest)

		return
	}

	params := url.Values{}

	for key, values := range request.URL.Query() {
		if strings.HasPrefix(key, "openid.") && len(values) > 0 {
			params.Set(key, values[0])
		}
	}

	params.Set("openid.mode", "check_authentication")

	verifyRequest, err := http.NewRequestWithContext(
		request.Context(),
		http.MethodPost,
		steamOpenID,
		strings.NewReader(params.Encode()),
	)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadGateway)

		return
	}

	verifyRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	verifyResponse, err := s.steam.client.Do(verifyRequest)
	if err != nil {
		http.Error(response, "steam verification failed", http.StatusBadGateway)

		return
	}

	defer func() {
		_ = verifyResponse.Body.Close()
	}()

	body, _ := io.ReadAll(io.LimitReader(verifyResponse.Body, 8192))
	if !strings.Contains(string(body), "is_valid:true") {
		http.Error(response, "steam verification failed", http.StatusBadRequest)

		return
	}

	claimed := request.URL.Query().Get("openid.claimed_id")
	steamID := claimed[strings.LastIndex(claimed, "/")+1:]
	if steamID == "" {
		http.Error(response, "steam id not found", http.StatusBadRequest)

		return
	}

	_, err = s.db.ExecContext(
		context.Background(),
		`UPDATE users SET steam_id = $1, steam_linked_at = now() WHERE lower(username) = lower($2)`,
		steamID,
		login,
	)
	if err != nil {
		http.Error(response, "steam save failed", http.StatusInternalServerError)

		return
	}

	s.libraryMu.Lock()
	delete(s.libraryCache, steamID)
	s.libraryMu.Unlock()

	http.Redirect(response, request, "/app/profile?steam=linked", http.StatusTemporaryRedirect)
}

func (s *LinkService) me(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	login := s.login(request)
	if login == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}

	var steamID string

	_ = s.db.QueryRowContext(
		context.Background(),
		`SELECT steam_id FROM users WHERE lower(username) = lower($1) LIMIT 1`,
		login,
	).Scan(&steamID)

	if steamID == "" {
		writeJSON(response, http.StatusOK, map[string]any{"linked": false})

		return
	}

	player, playerErr := s.steam.Player(request.Context(), steamID)
	if playerErr != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": playerErr.Error()})

		return
	}

	games, _ := s.steam.OwnedGames(request.Context(), steamID)
	items := make([]LibraryGame, len(games))

	for index, game := range games {
		items[index] = LibraryGame{OwnedGame: game}
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"linked":    true,
		"player":    player,
		"gameCount": len(games),
		"items":     items,
	})
}

func (s *LinkService) library(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	steamID := s.steamID(request)
	if steamID == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "steam is not linked"})

		return
	}

	s.libraryMu.Lock()
	cached, cachedOK := s.libraryCache[steamID]
	s.libraryMu.Unlock()

	if cachedOK && time.Now().Before(cached.expires) {
		writeJSON(response, http.StatusOK, map[string]any{"items": cached.items, "count": len(cached.items)})

		return
	}

	games, err := s.steam.Library(request.Context(), steamID)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	names := make([]string, len(games))
	for index := range games {
		names[index] = strings.ToLower(games[index].Name)
	}

	slugs := make(map[string]string, len(games))

	rows, err := s.db.QueryContext(
		request.Context(),
		`SELECT lower(title), slug FROM catalog_game WHERE lower(title) = ANY($1)`,
		pq.Array(names),
	)
	if err == nil {
		for rows.Next() {
			var title, slug string
			if scanErr := rows.Scan(&title, &slug); scanErr == nil {
				slugs[title] = slug
			}
		}
		_ = rows.Close()
	}

	for index := range games {
		games[index].StreamhataSlug = slugs[strings.ToLower(games[index].Name)]
	}

	s.libraryMu.Lock()
	s.libraryCache[steamID] = cachedLibrary{items: games, expires: time.Now().Add(5 * time.Minute)}
	s.libraryMu.Unlock()

	writeJSON(response, http.StatusOK, map[string]any{"items": games, "count": len(games)})
}

func (s *LinkService) achievements(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	steamID := s.steamID(request)
	if steamID == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "steam is not linked"})

		return
	}

	items, err := s.steam.Achievements(request.Context(), steamID)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *LinkService) inventory(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	steamID := s.steamID(request)
	if steamID == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "steam is not linked"})

		return
	}

	items, err := s.steam.Inventory(request.Context(), steamID)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *LinkService) steamID(request *http.Request) string {
	login := s.login(request)
	if login == "" || s.db == nil {
		return ""
	}

	var steamID string

	_ = s.db.QueryRowContext(
		context.Background(),
		`SELECT steam_id FROM users WHERE lower(username) = lower($1) LIMIT 1`,
		login,
	).Scan(&steamID)

	return steamID
}

func (s *LinkService) unlink(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	login := s.login(request)
	if login == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`UPDATE users SET steam_id = '', steam_linked_at = NULL WHERE lower(username) = lower($1)`,
		login,
	)

	s.libraryMu.Lock()
	for key := range s.libraryCache {
		delete(s.libraryCache, key)
	}
	s.libraryMu.Unlock()

	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *LinkService) login(request *http.Request) string {
	if s.db == nil {
		return ""
	}

	cookie, err := request.Cookie("e-hub-refresh-token")
	if err != nil || cookie.Value == "" {
		return ""
	}

	var login string

	err = s.db.QueryRowContext(
		context.Background(),
		`SELECT u.username FROM tokens t JOIN users u ON u.id = t.user_id WHERE t.refresh_token = $1 LIMIT 1`,
		cookie.Value,
	).Scan(&login)
	if err != nil {
		return ""
	}

	return login
}

func randomState() string {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}

	return hex.EncodeToString(buffer)
}
