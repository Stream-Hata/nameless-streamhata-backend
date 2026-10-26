// Package access — роли канала: владелец, админы, модераторы, VIP.
package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
)

const storageKey = "access_roles"

type Roles struct {
	Owners     []string `json:"owners"`
	Admins     []string `json:"admins"`
	Moderators []string `json:"moderators"`
	Vips       []string `json:"vips"`
}

type Service struct {
	mu    sync.RWMutex
	roles Roles
	db    *sql.DB
}

func New(db *sql.DB) *Service {
	service := &Service{db: db, roles: defaultRoles()}
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
	case contains(s.roles.Owners, login):
		return "owner"
	case contains(s.roles.Admins, login):
		return "admin"
	case contains(s.roles.Moderators, login):
		return "moderator"
	case contains(s.roles.Vips, login):
		return "vip"
	default:
		return "viewer"
	}
}

func (s *Service) IsOwner(login string) bool {
	return s.Role(login) == "owner"
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
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
