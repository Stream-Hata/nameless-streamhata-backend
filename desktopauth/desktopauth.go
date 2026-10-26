// Package desktopauth выдаёт одноразовый код для входа desktop-клиента.
package desktopauth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Service struct {
	db    *sql.DB
	mu    sync.Mutex
	codes map[string]code
}

type code struct {
	userID  int
	expires time.Time
}

func New(db *sql.DB) *Service {
	return &Service{db: db, codes: make(map[string]code)}
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/desktop/code", s.createCode)
	srv.ServeEndpoint("/rgg-sync/desktop/resume", s.resume)
	srv.ServeEndpoint("/rgg-sync/desktop/exchange", s.exchange)
	srv.ServeEndpoint("/rgg-sync/desktop/login", s.login)
}

func (s *Service) createCode(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	userID, ok := s.userID(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "code generation failed"})

		return
	}

	token := hex.EncodeToString(buffer)

	s.mu.Lock()
	s.codes[token] = code{userID: userID, expires: time.Now().Add(2 * time.Minute)}
	s.mu.Unlock()

	writeJSON(response, http.StatusOK, map[string]string{"code": token})
}

func (s *Service) resume(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	token := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)

		return
	}

	var userID int
	err := s.db.QueryRowContext(
		request.Context(),
		`SELECT u.id FROM tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.refresh_token = $1 AND u.banned = false LIMIT 1`,
		token,
	).Scan(&userID)
	if err != nil {
		http.Error(response, "unauthorized", http.StatusUnauthorized)

		return
	}

	buffer := make([]byte, 32)
	_, _ = rand.Read(buffer)
	bridgeCode := hex.EncodeToString(buffer)

	s.mu.Lock()
	s.codes[bridgeCode] = code{userID: userID, expires: time.Now().Add(2 * time.Minute)}
	s.mu.Unlock()

	writeJSON(response, http.StatusOK, map[string]string{"bridgeCode": bridgeCode})
}

func (s *Service) exchange(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096)).Decode(&body); err != nil {
		http.Error(response, "invalid json", http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	item, ok := s.codes[body.Code]
	delete(s.codes, body.Code)
	s.mu.Unlock()

	if !ok || time.Now().After(item.expires) {
		http.Error(response, "code is invalid or expired", http.StatusBadRequest)

		return
	}

	var refreshToken string
	err := s.db.QueryRowContext(
		request.Context(),
		`SELECT refresh_token FROM tokens WHERE user_id = $1 ORDER BY id DESC LIMIT 1`,
		item.userID,
	).Scan(&refreshToken)
	if err != nil || refreshToken == "" {
		http.Error(response, "session not found", http.StatusUnauthorized)

		return
	}

	buffer := make([]byte, 32)
	_, _ = rand.Read(buffer)
	bridgeCode := hex.EncodeToString(buffer)

	s.mu.Lock()
	s.codes[bridgeCode] = code{userID: item.userID, expires: time.Now().Add(2 * time.Minute)}
	s.mu.Unlock()

	writeJSON(response, http.StatusOK, map[string]string{
		"refreshToken": refreshToken,
		"bridgeCode":   bridgeCode,
	})
}

func (s *Service) login(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	token := strings.TrimSpace(request.URL.Query().Get("code"))
	if token == "" {
		http.Error(response, "code is required", http.StatusBadRequest)

		return
	}

	s.mu.Lock()
	item, ok := s.codes[token]
	delete(s.codes, token)
	s.mu.Unlock()

	if !ok || time.Now().After(item.expires) {
		http.Error(response, "code is invalid or expired", http.StatusBadRequest)

		return
	}

	var refreshToken string

	err := s.db.QueryRowContext(
		request.Context(),
		`SELECT refresh_token FROM tokens WHERE user_id = $1 ORDER BY id DESC LIMIT 1`,
		item.userID,
	).Scan(&refreshToken)
	if err != nil || refreshToken == "" {
		http.Error(response, "session not found", http.StatusUnauthorized)

		return
	}

	http.SetCookie(response, &http.Cookie{
		Name:     "e-hub-refresh-token",
		Value:    refreshToken,
		Path:     "/",
		Domain:   os.Getenv("COOKIE_DOMAIN"),
		HttpOnly: true,
		Secure:   os.Getenv("IS_PROD") == "1",
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(30 * 24 * time.Hour),
	})
	http.Redirect(response, request, "/app/dashboard", http.StatusSeeOther)
}

func (s *Service) userID(request *http.Request) (int, bool) {
	cookie, err := request.Cookie("e-hub-refresh-token")
	if err != nil || cookie.Value == "" {
		return 0, false
	}

	var userID int

	err = s.db.QueryRowContext(
		request.Context(),
		`SELECT u.id FROM tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.refresh_token = $1 AND u.banned = false LIMIT 1`,
		cookie.Value,
	).Scan(&userID)
	if err != nil {
		return 0, false
	}

	return userID, true
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
