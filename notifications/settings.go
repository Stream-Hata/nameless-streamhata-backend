package notifications

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
)

type Deps interface {
	ServeEndpoint(string, http.HandlerFunc)
}

type Settings struct {
	Enabled          bool `json:"enabled"`
	Version          bool `json:"version"`
	Follow           bool `json:"follow"`
	Subscribe        bool `json:"subscribe"`
	Gift             bool `json:"gift"`
	Resub            bool `json:"resub"`
	Raid             bool `json:"raid"`
	Chatter          bool `json:"chatter"`
	Viewer           bool `json:"viewer"`
	QuietWhenFocused bool `json:"quietWhenFocused"`
}

func Defaults() Settings {
	return Settings{Enabled: true, Version: true, Follow: true, Subscribe: true, Gift: true, Resub: true, Raid: true, Chatter: true, Viewer: true}
}

type Service struct {
	mu       sync.RWMutex
	db       *sql.DB
	settings Settings
}

func New(db *sql.DB) *Service {
	service := &Service{db: db, settings: Defaults()}
	if db != nil {
		var raw []byte
		if db.QueryRow(`SELECT value FROM app_setting WHERE key = 'notification_settings'`).Scan(&raw) == nil {
			_ = json.Unmarshal(raw, &service.settings)
		}
	}

	return service
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/app-settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			var raw []byte
			if s.db != nil {
				_ = s.db.QueryRow(`SELECT value FROM app_setting WHERE key = 'app_settings'`).Scan(&raw)
			}
			if len(raw) == 0 {
				raw = []byte(`{}`)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write(raw)
		case http.MethodPut:
			var value map[string]any
			if json.NewDecoder(r.Body).Decode(&value) != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(value)
			if s.db != nil {
				_, _ = s.db.Exec(`INSERT INTO app_setting (key, value) VALUES ('app_settings', $1)
					ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
			}
			writeJSON(w, value)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/notifications/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.mu.RLock()
			value := s.settings
			s.mu.RUnlock()
			writeJSON(w, value)
		case http.MethodPut:
			var value Settings
			if json.NewDecoder(r.Body).Decode(&value) != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.settings = value
			raw, _ := json.Marshal(value)
			if s.db != nil {
				_, _ = s.db.Exec(`INSERT INTO app_setting (key, value) VALUES ('notification_settings', $1)
					ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
			}
			s.mu.Unlock()
			writeJSON(w, value)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
