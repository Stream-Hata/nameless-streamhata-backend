// Package seventv — эмоты 7TV: глобальный набор + набор канала Twitch.
package seventv

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"net/http"
	"sync"
	"time"
)

const (
	globalSetURL = "https://7tv.io/v3/emote-sets/global"
	channelURL   = "https://7tv.io/v3/users/twitch/%s"
	cdnTemplate  = "https://cdn.7tv.app/emote/%s/2x.webp"
	refreshEvery = 15 * time.Minute
)

type emote struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type globalResponse struct {
	Emotes []emote `json:"emotes"`
}

type channelResponse struct {
	EmoteSet struct {
		Emotes []emote `json:"emotes"`
	} `json:"emote_set"`
}

// Snapshot — отдача панели: имя эмонта → URL картинки.
type Snapshot struct {
	Emotes    map[string]string `json:"emotes"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Error     string            `json:"error,omitempty"`
}

type Service struct {
	mu        sync.Mutex
	channelID string
	emotes    map[string]string
	updatedAt time.Time
	lastError string
	enabled   bool

	http *http.Client
}

func New() *Service {
	return &Service{
		emotes:  make(map[string]string),
		enabled: true,
		http:    netcfg.NewHTTPClient(15 * time.Second),
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "seventv"
}

func (s *Service) Name() string {
	return "7TV эмоты"
}

func (s *Service) Kind() string {
	return "emotes"
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	s.enabled = enabled
	s.mu.Unlock()

	if !enabled {
		s.mu.Lock()
		s.emotes = make(map[string]string)
		s.mu.Unlock()
	}

	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lastError != "" {
		return "ошибка: " + s.lastError
	}

	return fmt.Sprintf("эмотов: %d", len(s.emotes))
}

func (s *Service) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	emotes := make(map[string]string, len(s.emotes))

	for name, url := range s.emotes {
		emotes[name] = url
	}

	return Snapshot{Emotes: emotes, UpdatedAt: s.updatedAt, Error: s.lastError}
}

// SetChannel задаёт Twitch id канала и сразу обновляет набор.
func (s *Service) SetChannel(channelID string) {
	s.mu.Lock()
	s.channelID = channelID
	s.mu.Unlock()

	go s.Refresh(context.Background())
}

// Run обновляет наборы раз в 15 минут.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Refresh(ctx)
		}
	}
}

// Refresh забирает глобальный набор и набор канала (если известен).
func (s *Service) Refresh(ctx context.Context) {
	if !s.Enabled() {
		return
	}

	emotes := make(map[string]string)

	if err := s.loadGlobal(ctx, globalSetURL, emotes); err != nil {
		s.setError(err)

		return
	}

	s.mu.Lock()
	channelID := s.channelID
	s.mu.Unlock()

	if channelID != "" {
		_ = s.loadChannel(ctx, fmt.Sprintf(channelURL, channelID), emotes)
	}

	s.mu.Lock()
	s.emotes = emotes
	s.updatedAt = time.Now().UTC()
	s.lastError = ""
	s.mu.Unlock()
}

func (s *Service) loadGlobal(ctx context.Context, url string, target map[string]string) error {
	response, err := s.get(ctx, url)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	var payload globalResponse

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return err
	}

	mergeEmotes(payload.Emotes, target)

	return nil
}

func (s *Service) loadChannel(ctx context.Context, url string, target map[string]string) error {
	response, err := s.get(ctx, url)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	var payload channelResponse

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return err
	}

	mergeEmotes(payload.EmoteSet.Emotes, target)

	return nil
}

func (s *Service) get(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "JuryKit/1.0")

	response, err := s.http.Do(request)
	if err != nil {
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()

		return nil, fmt.Errorf("7tv returned %d", response.StatusCode)
	}

	return response, nil
}

func mergeEmotes(items []emote, target map[string]string) {
	for _, item := range items {
		if item.Name == "" || item.ID == "" {
			continue
		}

		target[item.Name] = fmt.Sprintf(cdnTemplate, item.ID)
	}
}

func (s *Service) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastError = err.Error()
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/emotes", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{
			"sevenTV":        service.Snapshot(),
			"twitchTemplate": "https://static-cdn.jtvnw.net/emoticons/v2/%s/default/dark/2.0",
		})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
