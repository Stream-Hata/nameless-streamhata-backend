// Package stream — управление эфиром: инфо канала, категория, шилд-мод, реклама.
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// Manager — операции Twitch (реализуется chatruntime).
type Manager interface {
	GetChannelInfo(ctx context.Context) (title, gameName, gameID string, err error)
	UpdateChannel(ctx context.Context, title, gameID string) error
	SearchGame(ctx context.Context, query string) (id, name string, err error)
	StartCommercial(ctx context.Context, lengthSeconds int) error
	GetShieldMode(ctx context.Context) (bool, error)
	UpdateShieldMode(ctx context.Context, active bool) error
	GetStreamViewers(ctx context.Context) (int, error)
}

type Info struct {
	Title    string `json:"title"`
	GameName string `json:"gameName"`
	GameID   string `json:"gameId"`
	Shield   bool   `json:"shieldMode"`
	Viewers  int    `json:"viewers"`
}

type Service struct {
	mu      sync.Mutex
	manager Manager
	info    Info
}

func New(manager Manager) *Service {
	return &Service{manager: manager}
}

// Refresh подтягивает инфо канала из Twitch.
func (s *Service) Refresh(ctx context.Context) Info {
	if s.manager == nil {
		return Info{}
	}

	info := Info{}

	if title, gameName, gameID, err := s.manager.GetChannelInfo(ctx); err == nil {
		info.Title = title
		info.GameName = gameName
		info.GameID = gameID
	}

	if shield, err := s.manager.GetShieldMode(ctx); err == nil {
		info.Shield = shield
	}

	if viewers, err := s.manager.GetStreamViewers(ctx); err == nil {
		info.Viewers = viewers
	}

	s.mu.Lock()
	s.info = info
	s.mu.Unlock()

	return info
}

func (s *Service) Current() Info {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.info
}

// Update меняет title и (опционально) категорию по поисковому запросу.
func (s *Service) Update(ctx context.Context, title, gameQuery string) (Info, error) {
	if s.manager == nil {
		return Info{}, fmt.Errorf("Twitch не подключён")
	}

	gameID := ""

	if gameQuery != "" {
		id, _, err := s.manager.SearchGame(ctx, gameQuery)
		if err != nil {
			return Info{}, fmt.Errorf("категория не найдена: %s", gameQuery)
		}

		gameID = id
	}

	if err := s.manager.UpdateChannel(ctx, title, gameID); err != nil {
		return Info{}, err
	}

	return s.Refresh(ctx), nil
}

func (s *Service) AdBreak(ctx context.Context, lengthSeconds int) error {
	if s.manager == nil {
		return fmt.Errorf("Twitch не подключён")
	}

	if lengthSeconds != 30 && lengthSeconds != 60 && lengthSeconds != 90 && lengthSeconds != 120 && lengthSeconds != 150 && lengthSeconds != 180 {
		lengthSeconds = 60
	}

	return s.manager.StartCommercial(ctx, lengthSeconds)
}

func (s *Service) SetShield(ctx context.Context, active bool) error {
	if s.manager == nil {
		return fmt.Errorf("Twitch не подключён")
	}

	if err := s.manager.UpdateShieldMode(ctx, active); err != nil {
		return err
	}

	s.mu.Lock()
	s.info.Shield = active
	s.mu.Unlock()

	return nil
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "stream"
}

func (s *Service) Name() string {
	return "Управление эфиром"
}

func (s *Service) Kind() string {
	return "stream"
}

func (s *Service) Enabled() bool {
	return true
}

func (s *Service) SetEnabled(bool) error {
	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.info.Title == "" {
		return "нет данных"
	}

	return fmt.Sprintf("%s · %s", s.info.GameName, s.info.Title)
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/stream", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, service.Refresh(request.Context()))

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Title     string `json:"title"`
			GameQuery string `json:"gameQuery"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		info, err := service.Update(request.Context(), body.Title, body.GameQuery)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, info)
	})

	srv.ServeEndpoint("/rgg-sync/stream/commercial", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Length int `json:"length"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		if err := service.AdBreak(request.Context(), body.Length); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "length": body.Length})
	})

	srv.ServeEndpoint("/rgg-sync/stream/shield", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Active bool `json:"active"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		if err := service.SetShield(request.Context(), body.Active); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "active": body.Active})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
