// Package widgets — включение/выключение OBS-оверлеев.
package widgets

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
)

const storageKey = "widgets"
const configsKey = "widget_configs"

var defaultWidgets = map[string]bool{
	"jury":      true,
	"timer":     true,
	"hotbar":    true,
	"roulette":  true,
	"alerts":    false,
	"events":    false,
	"donations": true,
	"audio":     true,
	"wheel":     false,
	"chat":      true,
	"points":    false,
	"music":     false,
	"nowplaying": false,
	"streaks":   true,
}

type Service struct {
	mu       sync.RWMutex
	settings map[string]bool
	configs  map[string]map[string]any
	db       *sql.DB
}

func New(db *sql.DB) *Service {
	service := &Service{
		settings: clone(defaultWidgets),
		configs:  make(map[string]map[string]any),
		db:       db,
	}
	service.load()

	return service
}

func (s *Service) Get() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return clone(s.settings)
}

func (s *Service) Set(kind string, enabled bool) map[string]bool {
	s.mu.Lock()
	s.settings[kind] = enabled
	s.mu.Unlock()

	s.persist()

	return s.Get()
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

	var stored map[string]bool

	if err := json.Unmarshal(raw, &stored); err != nil {
		return
	}

	s.mu.Lock()
	for kind, enabled := range stored {
		s.settings[kind] = enabled
	}
	s.mu.Unlock()

	var configsRaw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		configsKey,
	).Scan(&configsRaw); err != nil {
		return
	}

	var configs map[string]map[string]any

	if err := json.Unmarshal(configsRaw, &configs); err != nil {
		return
	}

	s.mu.Lock()
	for kind, config := range configs {
		s.configs[kind] = config
	}
	s.mu.Unlock()
}

func (s *Service) persistConfigs() {
	if s.db == nil {
		return
	}

	s.mu.RLock()
	raw, err := json.Marshal(s.configs)
	s.mu.RUnlock()

	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		configsKey,
		string(raw),
	)
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.Get())
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

func clone(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))

	for key, value := range source {
		result[key] = value
	}

	return result
}

func (s *Service) GetConfig(kind string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config := s.configs[kind]

	if config == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(config))

	for key, value := range config {
		result[key] = value
	}

	return result
}

func (s *Service) SetConfig(kind string, config map[string]any) map[string]any {
	s.mu.Lock()
	s.configs[kind] = config
	s.mu.Unlock()

	s.persistConfigs()

	return s.GetConfig(kind)
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/widgets", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"widgets": service.Get()})
	})

	srv.ServeEndpoint("/rgg-sync/widgets/{kind}/config", func(response http.ResponseWriter, request *http.Request) {
		kind := request.PathValue("kind")

		if kind == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "kind is required"})

			return
		}

		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"config": service.GetConfig(kind)})

			return
		}

		if request.Method != http.MethodPost && request.Method != http.MethodPut {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var config map[string]any

		if err := json.NewDecoder(request.Body).Decode(&config); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"config": service.SetConfig(kind, config)})
	})

	srv.ServeEndpoint("/rgg-sync/widgets/{kind}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost && request.Method != http.MethodPatch {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Enabled bool `json:"enabled"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		kind := request.PathValue("kind")
		if kind == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "kind is required"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"widgets": service.Set(kind, body.Enabled)})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
