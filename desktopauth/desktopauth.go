// Package desktopauth выдаёт одноразовый код для входа desktop-клиента.
package desktopauth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Service struct {
	db       *sql.DB
	mu       sync.Mutex
	codes    map[string]code
	sessions *twitch.Sessions
}

type code struct {
	userID  int
	expires time.Time
}

func New(db *sql.DB, sessions *twitch.Sessions) *Service {
	return &Service{db: db, sessions: sessions, codes: make(map[string]code)}
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

	userID, err := s.sessions.Resume(request.Context(), token)
	var upgradedToken string
	if err == sql.ErrNoRows {
		digest, digestErr := data_base.RefreshTokenDigest(token)
		if digestErr != nil {
			http.Error(response, "session temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		err = s.db.QueryRowContext(
			request.Context(),
			`SELECT u.id FROM tokens t JOIN users u ON u.id = t.user_id
			  WHERE t.refresh_token_hash = $1 AND u.banned = false LIMIT 1`,
			digest,
		).Scan(&userID)
		if err == nil {
			upgradedToken, _, err = s.sessions.Mint(request.Context(), userID)
		}
	}
	if err != nil && err != sql.ErrNoRows {
		http.Error(response, "session temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
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

	result := map[string]string{"bridgeCode": bridgeCode}
	if upgradedToken != "" {
		result["sessionToken"] = upgradedToken
	}
	writeJSON(response, http.StatusOK, result)
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

	secret, _, err := s.sessions.Mint(request.Context(), item.userID)
	if err != nil {
		http.Error(response, "session creation failed", http.StatusInternalServerError)
		return
	}

	buffer := make([]byte, 32)
	_, _ = rand.Read(buffer)
	bridgeCode := hex.EncodeToString(buffer)

	s.mu.Lock()
	s.codes[bridgeCode] = code{userID: item.userID, expires: time.Now().Add(2 * time.Minute)}
	s.mu.Unlock()

	writeJSON(response, http.StatusOK, map[string]string{
		"refreshToken": secret,
		"sessionToken": secret,
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

	if err := s.sessions.Issue(response, request, item.userID); err != nil {
		http.Error(response, "session creation failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(response, request, "/app/dashboard", http.StatusSeeOther)
}

func (s *Service) userID(request *http.Request) (int, bool) {
	user, _, err := s.sessions.Account(request)
	if err != nil {
		return 0, false
	}
	return user.ID, true
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
