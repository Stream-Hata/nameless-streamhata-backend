// Package analytics — сессии эфира: пики/средние зрители, сообщения, экспорт.
package analytics

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Sample — точка замера внутри сессии.
type Sample struct {
	At       time.Time `json:"at"`
	Viewers  int       `json:"viewers"`
	Chatters int       `json:"chatters"`
	Messages int       `json:"messages"`
}

// Session — сессия эфира.
type Session struct {
	ID           string     `json:"id"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt"`
	PeakViewers  int        `json:"peakViewers"`
	AvgViewers   int        `json:"avgViewers"`
	Chatters     int        `json:"chatters"`
	Messages     int        `json:"messages"`
	SamplesCount int        `json:"samplesCount"`
}

// ViewerSource — источник числа зрителей (реализуется chatruntime).
type ViewerSource interface {
	GetStreamViewers(ctx context.Context) (int, error)
}

type Service struct {
	mu      sync.Mutex
	current *Session
	history []Session
	samples []Sample

	source ViewerSource
	now    func() time.Time
}

func New(source ViewerSource) *Service {
	return &Service{
		source:  source,
		history: make([]Session, 0, 50),
		now:     time.Now,
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "analytics"
}

func (s *Service) Name() string {
	return "Аналитика эфира"
}

func (s *Service) Kind() string {
	return "analytics"
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

	if s.current == nil {
		return fmt.Sprintf("сессий в истории: %d", len(s.history))
	}

	return fmt.Sprintf("в эфире, пик %d зрителей", s.current.PeakViewers)
}

func (s *Service) Current() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.current
}

func (s *Service) History() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Session, 0, len(s.history))

	for i := len(s.history) - 1; i >= 0; i-- {
		result = append(result, s.history[i])
	}

	return result
}

/* ── События ──────────────────────────────────────────────────────── */

// StreamOnline начинает сессию.
func (s *Service) StreamOnline() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current != nil {
		return
	}

	s.current = &Session{
		ID:        fmt.Sprintf("session-%d", s.now().UnixNano()),
		StartedAt: s.now().UTC(),
	}
	s.samples = s.samples[:0]
}

// StreamOffline закрывает сессию и переносит её в историю.
func (s *Service) StreamOffline() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		return
	}

	endedAt := s.now().UTC()
	s.current.EndedAt = &endedAt

	s.history = append(s.history, *s.current)

	if len(s.history) > 50 {
		s.history = s.history[len(s.history)-50:]
	}

	s.current = nil
}

// HandleMessage считает сообщения чата в активной сессии.
func (s *Service) HandleMessage() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		s.current = &Session{
			ID:        fmt.Sprintf("session-%d", s.now().UnixNano()),
			StartedAt: s.now().UTC(),
		}
	}

	s.current.Messages++
}

// HandleChatters фиксирует число чатерсов из тика viewer-рантайма.
func (s *Service) HandleChatters(chatters int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		return
	}

	s.current.Chatters = chatters
}

// Tick опрашивает число зрителей и пишет точку замера.
func (s *Service) Tick(ctx context.Context) {
	s.mu.Lock()
	active := s.current != nil
	s.mu.Unlock()

	if !active || s.source == nil {
		return
	}

	viewers, err := s.source.GetStreamViewers(ctx)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.current == nil {
		return
	}

	sample := Sample{
		At:       s.now().UTC(),
		Viewers:  viewers,
		Chatters: s.current.Chatters,
		Messages: s.current.Messages,
	}

	s.samples = append(s.samples, sample)

	if len(s.samples) > 2000 {
		s.samples = s.samples[len(s.samples)-2000:]
	}

	s.current.SamplesCount = len(s.samples)

	if viewers > s.current.PeakViewers {
		s.current.PeakViewers = viewers
	}

	total := 0

	for _, item := range s.samples {
		total += item.Viewers
	}

	s.current.AvgViewers = total / len(s.samples)
}

// Run тикает раз в минуту до отмены контекста.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

/* ── Экспорт ──────────────────────────────────────────────────────── */

func (s *Service) WriteCSV(writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	csvWriter.Comma = ';'

	if err := csvWriter.Write([]string{
		"session_id", "started_at", "ended_at", "peak_viewers", "avg_viewers", "messages", "samples",
	}); err != nil {
		return err
	}

	for _, session := range s.History() {
		endedAt := ""

		if session.EndedAt != nil {
			endedAt = session.EndedAt.Format(time.RFC3339)
		}

		row := []string{
			session.ID,
			session.StartedAt.Format(time.RFC3339),
			endedAt,
			strconv.Itoa(session.PeakViewers),
			strconv.Itoa(session.AvgViewers),
			strconv.Itoa(session.Messages),
			strconv.Itoa(session.SamplesCount),
		}

		if err := csvWriter.Write(row); err != nil {
			return err
		}
	}

	csvWriter.Flush()

	return csvWriter.Error()
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/analytics", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{
			"current": service.Current(),
			"history": service.History(),
		})
	})

	srv.ServeEndpoint("/rgg-sync/analytics/export", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/csv; charset=utf-8")
		response.Header().Set("Content-Disposition", `attachment; filename="jurykit-analytics.csv"`)

		if err := service.WriteCSV(response); err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
		}
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
