// Package timers — периодические сообщения в чат.
package timers

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
)

const (
	tickInterval   = 30 * time.Second
	activeWindow   = 5 * time.Minute
	minIntervalSec = 30
)

// Timer — таймер с набором сообщений.
type Timer struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Messages        []string  `json:"messages"`
	IntervalSeconds int       `json:"intervalSeconds"`
	Enabled         bool      `json:"enabled"`
	ActiveOnly      bool      `json:"activeOnly"`
	Random          bool      `json:"random"`
	NextMessage     int       `json:"-"`
	LastFiredAt     time.Time `json:"lastFiredAt"`
}

// Service хранит таймеры и отправляет сообщения по расписанию.
type Service struct {
	mu       sync.Mutex
	timers   map[string]*Timer
	writer   chat.Writer
	channel  string
	lastChat time.Time
	now      func() time.Time
}

func New(writer chat.Writer) *Service {
	return &Service{
		timers: make(map[string]*Timer),
		writer: writer,
		now:    time.Now,
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "timers"
}

func (s *Service) Name() string {
	return "Таймеры сообщений"
}

func (s *Service) Kind() string {
	return "timers"
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, timer := range s.timers {
		if timer.Enabled {
			return true
		}
	}

	return false
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, timer := range s.timers {
		timer.Enabled = enabled
	}

	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	active := 0

	for _, timer := range s.timers {
		if timer.Enabled {
			active++
		}
	}

	return fmt.Sprintf("таймеров: %d (активных: %d)", len(s.timers), active)
}

/* ── CRUD ─────────────────────────────────────────────────────────── */

func (s *Service) List() []Timer {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]Timer, 0, len(s.timers))

	for _, timer := range s.timers {
		list = append(list, *timer)
	}

	return list
}

func (s *Service) Upsert(timer Timer) (Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(timer.Messages) == 0 {
		return Timer{}, fmt.Errorf("нужно хотя бы одно сообщение")
	}

	if timer.IntervalSeconds < minIntervalSec {
		timer.IntervalSeconds = minIntervalSec
	}

	if timer.ID == "" {
		timer.ID = fmt.Sprintf("timer-%d", s.now().UnixNano())
	}

	if timer.Name == "" {
		timer.Name = "Таймер"
	}

	timer.LastFiredAt = s.now()

	s.timers[timer.ID] = &timer

	return timer, nil
}

func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.timers[id]; !ok {
		return false
	}

	delete(s.timers, id)

	return true
}

// SetChannel запоминает канал для $(channel)-переменных.
func (s *Service) SetChannel(login string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.channel = login
}

// NoteChatActivity отмечает активность чата (для ActiveOnly).
func (s *Service) NoteChatActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastChat = s.now()
}

// Run тикает до отмены контекста.
func (s *Service) Run(done <-chan struct{}) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.Tick(s.now())
		}
	}
}

// Tick проверяет таймеры и отправляет сообщения (вынесено для тестов).
func (s *Service) Tick(now time.Time) {
	s.mu.Lock()
	channel := s.channel
	lastChat := s.lastChat

	type firing struct {
		text string
	}

	var fires []firing

	for _, timer := range s.timers {
		if !timer.Enabled || len(timer.Messages) == 0 {
			continue
		}

		if now.Sub(timer.LastFiredAt) < time.Duration(timer.IntervalSeconds)*time.Second {
			continue
		}

		if timer.ActiveOnly && now.Sub(lastChat) > activeWindow {
			continue
		}

		text := ""

		if timer.Random {
			text = timer.Messages[rand.IntN(len(timer.Messages))]
		} else {
			text = timer.Messages[timer.NextMessage%len(timer.Messages)]
			timer.NextMessage++
		}

		timer.LastFiredAt = now
		fires = append(fires, firing{text: text})
	}

	s.mu.Unlock()

	for _, item := range fires {
		text := strings.ReplaceAll(item.text, "$(channel)", channel)

		if s.writer == nil {
			continue
		}

		if err := s.writer.Say(text); err != nil {
			log.Printf("[Timers] send failed: %v", err)
		}
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/timers", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"timers": service.List()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var timer Timer

		if err := json.NewDecoder(request.Body).Decode(&timer); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		saved, err := service.Upsert(timer)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, saved)
	})

	srv.ServeEndpoint("/rgg-sync/timers/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "timer not found"})

			return
		}

		response.WriteHeader(http.StatusNoContent)
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
