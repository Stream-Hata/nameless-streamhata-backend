// Package access — роли канала: владелец, админы, модераторы, VIP.
package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

const storageKey = "access_roles"

type Roles struct {
	Owners     []string `json:"owners"`
	Admins     []string `json:"admins"`
	Moderators []string `json:"moderators"`
	Vips       []string `json:"vips"`
}

type Service struct {
	mu               sync.RWMutex
	roles            Roles
	db               *sql.DB
	sessions         *twitch.Sessions
	appOwnerLogin    string
	appOwnerTwitchID string
}

func New(db *sql.DB, sessions *twitch.Sessions) *Service {
	service := &Service{db: db, roles: defaultRoles(), sessions: sessions, appOwnerLogin: configuredApplicationOwner()}
	service.loadApplicationOwnerIdentity()
	service.load()

	return service
}

func defaultRoles() Roles {
	owners := make([]string, 0)

	for _, value := range strings.Split(os.Getenv("OWNER_LOGINS"), ",") {
		if login := normalize(value); login != "" {
			owners = append(owners, login)
		}
	}

	if login := normalize(os.Getenv("OWNER_LOGIN")); login != "" {
		owners = append(owners, login)
	}

	if len(owners) == 0 {
		owners = append(owners, "grep_ban")
	}

	return Roles{Owners: unique(owners)}
}

func normalize(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func configuredApplicationOwner() string {
	if login := normalize(os.Getenv("OWNER_LOGIN")); login != "" {
		return login
	}
	return "grep_ban"
}

func (s *Service) Get() Roles {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return clone(s.roles)
}

func (s *Service) Save(roles Roles) Roles {
	s.mu.Lock()
	s.roles = clean(roles)
	s.mu.Unlock()

	s.persist()

	return s.Get()
}

func (s *Service) SetRole(login string, role string) Roles {
	login = normalize(login)

	if login == "" {
		return s.Get()
	}

	s.mu.Lock()

	s.roles.Owners = remove(s.roles.Owners, login)
	s.roles.Admins = remove(s.roles.Admins, login)
	s.roles.Moderators = remove(s.roles.Moderators, login)
	s.roles.Vips = remove(s.roles.Vips, login)

	switch role {
	case "owner":
		s.roles.Owners = append(s.roles.Owners, login)
	case "admin":
		s.roles.Admins = append(s.roles.Admins, login)
	case "moderator":
		s.roles.Moderators = append(s.roles.Moderators, login)
	case "vip":
		s.roles.Vips = append(s.roles.Vips, login)
	}

	s.mu.Unlock()

	s.persist()

	return s.Get()
}

func (s *Service) Role(login string) string {
	login = normalize(login)

	s.mu.RLock()
	defer s.mu.RUnlock()

	switch {
	case login != "" && login == s.appOwnerLogin:
		return "owner"
	case contains(s.roles.Admins, login):
		return "admin"
	case contains(s.roles.Moderators, login):
		return "moderator"
	case contains(s.roles.Vips, login):
		return "vip"
	default:
		if login != "" {
			return "streamer"
		}
		return "viewer"
	}
}

func (s *Service) IsOwner(login string) bool {
	login = normalize(login)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return login != "" && login == s.appOwnerLogin
}

// IsApplicationOwner is reserved for app-wide configuration such as shared
// provider credentials. Channel-level role assignments must not grant it.
func (s *Service) IsApplicationOwner(login, twitchUserID string) bool {
	login = normalize(login)
	twitchUserID = strings.TrimSpace(twitchUserID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if login == "" || twitchUserID == "" {
		return false
	}
	if s.appOwnerTwitchID == "" {
		if login != s.appOwnerLogin {
			return false
		}
		s.appOwnerTwitchID = twitchUserID
	} else if s.appOwnerTwitchID != twitchUserID {
		return false
	}
	s.appOwnerLogin = login
	s.persistApplicationOwnerIdentityLocked()
	return true
}

func (s *Service) IsAdmin(login string) bool {
	return s.Role(login) == "owner" || s.Role(login) == "admin"
}

func (s *Service) IsGroupAdmin(login string) bool {
	login = normalize(login)
	if login == "" || s.db == nil {
		return false
	}

	var allowed bool
	err := s.db.QueryRowContext(context.Background(), `
		SELECT EXISTS(
			SELECT 1 FROM streamer_group_members m
			JOIN users u ON u.id = m.user_id
			WHERE lower(u.username) = $1 AND m.role IN ('owner', 'admin')
		)`, login).Scan(&allowed)

	return err == nil && allowed
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		storageKey,
	).Scan(&raw); err != nil {
		return
	}

	var roles Roles
	if json.Unmarshal(raw, &roles) == nil {
		roles = clean(roles)
		// Never allow the dashboard to become ownerless after a bad role edit.
		for _, owner := range defaultRoles().Owners {
			if !contains(roles.Owners, owner) {
				roles.Owners = append(roles.Owners, owner)
			}
		}
		s.roles = clean(roles)
	}
}

type applicationOwnerIdentity struct {
	Login        string `json:"login"`
	TwitchUserID string `json:"twitch_user_id"`
}

func (s *Service) loadApplicationOwnerIdentity() {
	if s.db == nil {
		return
	}
	var raw []byte
	err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'application_owner_identity'`).Scan(&raw)
	if err == nil {
		var identity applicationOwnerIdentity
		if json.Unmarshal(raw, &identity) == nil && strings.TrimSpace(identity.TwitchUserID) != "" {
			s.appOwnerTwitchID = strings.TrimSpace(identity.TwitchUserID)
			if login := normalize(identity.Login); login != "" {
				s.appOwnerLogin = login
			}
			return
		}
	}

	// Bootstrap the stable Twitch ID from the current configured login. Once
	// stored, all authorization uses the immutable Twitch ID across renames.
	var twitchUserID int64
	if err := s.db.QueryRowContext(context.Background(), `
		SELECT twitch_user_id FROM users WHERE lower(username) = $1 AND twitch_user_id > 0 ORDER BY id LIMIT 1
	`, s.appOwnerLogin).Scan(&twitchUserID); err == nil {
		s.appOwnerTwitchID = strconv.FormatInt(twitchUserID, 10)
		s.persistApplicationOwnerIdentityLocked()
	}
}

func (s *Service) persistApplicationOwnerIdentityLocked() {
	if s.db == nil || s.appOwnerTwitchID == "" || s.appOwnerLogin == "" {
		return
	}
	raw, err := json.Marshal(applicationOwnerIdentity{Login: s.appOwnerLogin, TwitchUserID: s.appOwnerTwitchID})
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(context.Background(), `
		INSERT INTO app_setting (key, value) VALUES ('application_owner_identity', $1::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`, string(raw))
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.roles)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		storageKey,
		string(raw),
	)
}

func clean(roles Roles) Roles {
	return Roles{
		Owners:     unique(roles.Owners),
		Admins:     unique(roles.Admins),
		Moderators: unique(roles.Moderators),
		Vips:       unique(roles.Vips),
	}
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)

	for _, value := range values {
		login := normalize(value)

		if login == "" || seen[login] {
			continue
		}

		seen[login] = true
		result = append(result, login)
	}

	return result
}

func clone(roles Roles) Roles {
	return Roles{
		Owners:     append(make([]string, 0), roles.Owners...),
		Admins:     append(make([]string, 0), roles.Admins...),
		Moderators: append(make([]string, 0), roles.Moderators...),
		Vips:       append(make([]string, 0), roles.Vips...),
	}
}

func contains(values []string, login string) bool {
	for _, value := range values {
		if value == login {
			return true
		}
	}

	return false
}

func remove(values []string, login string) []string {
	result := make([]string, 0, len(values))

	for _, value := range values {
		if value != login {
			result = append(result, value)
		}
	}

	return result
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func (s *Service) LoginFromRequest(request *http.Request) string {
	if s.sessions != nil {
		user, _, err := s.sessions.Account(request)
		if err != nil {
			return ""
		}
		return normalize(user.Username)
	}
	if s.db == nil {
		return ""
	}

	cookie, err := request.Cookie("e-hub-refresh-token")
	if err != nil || cookie.Value == "" {
		return ""
	}
	digest, err := data_base.RefreshTokenDigest(cookie.Value)
	if err != nil {
		return ""
	}

	var login string

	err = s.db.QueryRowContext(
		context.Background(),
		`SELECT u.username FROM tokens t JOIN users u ON u.id = t.user_id WHERE t.refresh_token_hash = $1 LIMIT 1`,
		digest,
	).Scan(&login)
	if err != nil {
		return ""
	}

	return normalize(login)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/access", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, service.Get())
		case http.MethodPut, http.MethodPost:
			if login := service.LoginFromRequest(request); login == "" || !service.IsOwner(login) {
				writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

				return
			}

			var roles Roles

			if err := json.NewDecoder(request.Body).Decode(&roles); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			writeJSON(response, http.StatusOK, service.Save(roles))
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/access/role", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]string{
				"role": service.Role(request.URL.Query().Get("login")),
			})

			return
		}

		if request.Method != http.MethodPost && request.Method != http.MethodPut {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if login := service.LoginFromRequest(request); login == "" || !service.IsOwner(login) {
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

			return
		}

		var body struct {
			Login string `json:"login"`
			Role  string `json:"role"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		writeJSON(response, http.StatusOK, service.SetRole(body.Login, body.Role))
	})

	srv.ServeEndpoint("/rgg-sync/access/membership", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}
		login := service.LoginFromRequest(request)
		if login == "" {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "authorization required"})

			return
		}
		writeJSON(response, http.StatusOK, map[string]any{
			"login":      login,
			"role":       service.Role(login),
			"groupAdmin": service.IsGroupAdmin(login),
		})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
