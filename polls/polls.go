// Package polls — опросы и предикты: зеркало нативных Twitch-голосований
// в панель + создание/завершение опросов из панели.
package polls

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type Choice struct {
	Title string `json:"title"`
	Votes int    `json:"votes"`
}

type Poll struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Choices   []Choice `json:"choices"`
	StartedAt string   `json:"startedAt"`
	EndsAt    string   `json:"endsAt"`
}

type Prediction struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Outcomes []Choice `json:"outcomes"`
	EndsAt   string   `json:"endsAt"`
}

// Poller — создание/завершение опросов через Helix (реализуется chatruntime).
type Poller interface {
	CreatePoll(ctx context.Context, title string, choices []string, durationSeconds int) (string, error)
	EndPoll(ctx context.Context, pollID, status string) error
}

type Service struct {
	mu         sync.Mutex
	current    *Poll
	history    []Poll
	prediction *Prediction

	poller Poller
}

func New(poller Poller) *Service {
	return &Service{
		poller:  poller,
		history: make([]Poll, 0, 50),
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "polls"
}

func (s *Service) Name() string {
	return "Опросы и предикты"
}

func (s *Service) Kind() string {
	return "polls"
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

	state := "нет активного опроса"

	if s.current != nil && s.current.Status == "ACTIVE" {
		state = "активен: " + s.current.Title
	}

	return state
}

func (s *Service) Current() *Poll {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.current
}

func (s *Service) CurrentPrediction() *Prediction {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.prediction
}

func (s *Service) History() []Poll {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Poll, 0, len(s.history))

	for i := len(s.history) - 1; i >= 0; i-- {
		result = append(result, s.history[i])
	}

	return result
}

/* ── Создание и завершение ────────────────────────────────────────── */

// Create запускает нативный опрос Twitch.
func (s *Service) Create(ctx context.Context, title string, choices []string, durationSeconds int) (string, error) {
	if s.poller == nil {
		return "", fmt.Errorf("опросы недоступны: нет подключения к Twitch")
	}

	if len(choices) < 2 {
		return "", fmt.Errorf("нужно минимум два варианта")
	}

	if durationSeconds < 15 {
		durationSeconds = 15
	}

	if durationSeconds > 1800 {
		durationSeconds = 1800
	}

	return s.poller.CreatePoll(ctx, title, choices, durationSeconds)
}

// End завершает активный опрос (TERMINATED — досрочно, ARCHIVED — после подсчёта).
func (s *Service) End(ctx context.Context, status string) error {
	current := s.Current()

	if current == nil {
		return fmt.Errorf("нет активного опроса")
	}

	if status != "TERMINATED" && status != "ARCHIVED" {
		status = "TERMINATED"
	}

	if s.poller == nil {
		return fmt.Errorf("опросы недоступны")
	}

	return s.poller.EndPoll(ctx, current.ID, status)
}

/* ── Обработка EventSub ───────────────────────────────────────────── */

type pollEvent struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`

	StartedAt string `json:"started_at"`
	EndsAt    string `json:"ends_at"`

	Choices []struct {
		Title string `json:"title"`
		Votes int    `json:"votes"`
	} `json:"choices"`
}

type predictionEvent struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	EndsAt string `json:"ends_at"`

	Outcomes []struct {
		Title         string `json:"title"`
		Users         int    `json:"users"`
		ChannelPoints int    `json:"channel_points"`
	} `json:"outcomes"`
}

func (s *Service) HandleNotification(subscriptionType string, raw json.RawMessage) {
	switch subscriptionType {
	case "channel.poll.begin", "channel.poll.progress", "channel.poll.end":
		s.handlePoll(subscriptionType, raw)
	case "channel.prediction.begin", "channel.prediction.progress", "channel.prediction.lock", "channel.prediction.end":
		s.handlePrediction(raw)
	}
}

func (s *Service) handlePoll(subscriptionType string, raw json.RawMessage) {
	var event pollEvent

	if err := json.Unmarshal(raw, &event); err != nil || event.ID == "" {
		return
	}

	poll := Poll{
		ID:        event.ID,
		Title:     event.Title,
		Status:    event.Status,
		StartedAt: event.StartedAt,
		EndsAt:    event.EndsAt,
		Choices:   make([]Choice, 0, len(event.Choices)),
	}

	for _, choice := range event.Choices {
		poll.Choices = append(poll.Choices, Choice{Title: choice.Title, Votes: choice.Votes})
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.current = &poll

	if subscriptionType == "channel.poll.end" {
		poll.Status = "TERMINATED"

		s.history = append(s.history, poll)

		if len(s.history) > 50 {
			s.history = s.history[len(s.history)-50:]
		}
	}
}

func (s *Service) handlePrediction(raw json.RawMessage) {
	var event predictionEvent

	if err := json.Unmarshal(raw, &event); err != nil || event.ID == "" {
		return
	}

	prediction := Prediction{
		ID:       event.ID,
		Title:    event.Title,
		Status:   orDefault(event.Status, "ACTIVE"),
		EndsAt:   event.EndsAt,
		Outcomes: make([]Choice, 0, len(event.Outcomes)),
	}

	for _, outcome := range event.Outcomes {
		prediction.Outcomes = append(prediction.Outcomes, Choice{Title: outcome.Title, Votes: outcome.Users})
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.prediction = &prediction
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/polls", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{
				"current":    service.Current(),
				"prediction": service.CurrentPrediction(),
				"history":    service.History(),
			})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Title           string   `json:"title"`
			Choices         []string `json:"choices"`
			DurationSeconds int      `json:"durationSeconds"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		id, err := service.Create(request.Context(), body.Title, body.Choices, body.DurationSeconds)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]string{"id": id})
	})

	srv.ServeEndpoint("/rgg-sync/polls/end", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Status string `json:"status"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		if err := service.End(request.Context(), body.Status); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]string{"status": "terminated"})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
