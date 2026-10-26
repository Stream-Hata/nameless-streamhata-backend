// Package alerts — события канала (follow/sub/bits/raid/hype train),
// настройки типов и лента последних алертов для виджета.
package alerts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event — нормализованный алерт.
type Event struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Username  string          `json:"username"`
	Message   string          `json:"message"`
	Amount    int             `json:"amount"`
	CreatedAt time.Time       `json:"createdAt"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Settings — какие типы алертов включены и длительность показа.
type Settings struct {
	Types           map[string]bool `json:"types"`
	DurationSeconds int             `json:"durationSeconds"`
	SoundEnabled    bool            `json:"soundEnabled"`
	SoundURL        string          `json:"soundUrl"`
	SoundVolume     int             `json:"soundVolume"`
}

var defaultTypes = []string{
	"follow", "subscribe", "gift", "resub", "cheer", "raid", "hype_train", "goal",
	"stream_online", "stream_offline", "login", "streak", "system",
}

func defaultSettings() Settings {
	types := make(map[string]bool, len(defaultTypes))

	for _, alertType := range defaultTypes {
		types[alertType] = true
	}

	return Settings{Types: types, DurationSeconds: 8, SoundEnabled: true, SoundVolume: 70}
}

// Service — лента алертов и настройки.
type Service struct {
	mu       sync.Mutex
	events   []Event
	settings Settings
	enabled  bool
	counter  int

	listeners []func(Event)
}

func New() *Service {
	return &Service{
		events:   make([]Event, 0, 100),
		settings: defaultSettings(),
		enabled:  true,
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "alerts"
}

func (s *Service) Name() string {
	return "Алерты событий"
}

func (s *Service) Kind() string {
	return "alerts"
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.enabled = enabled

	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return fmt.Sprintf("событий в ленте: %d", len(s.events))
}

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings
}

func (s *Service) SetSettings(settings Settings) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	if settings.Types == nil {
		settings.Types = s.settings.Types
	}

	if settings.DurationSeconds <= 0 {
		settings.DurationSeconds = 8
	}

	if settings.SoundVolume < 0 || settings.SoundVolume > 100 {
		settings.SoundVolume = 70
	}

	s.settings = settings

	return s.settings
}

func (s *Service) Recent(limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}

	result := make([]Event, 0, limit)

	for i := len(s.events) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, s.events[i])
	}

	return result
}

// AddListener добавляет колбэк на опубликованные алерты (баллы, уведомления,
// аналитика). Можно навесить несколько подписчиков.
func (s *Service) AddListener(hook func(Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listeners = append(s.listeners, hook)
}

// Publish добавляет событие в ленту (с учётом включённых типов).
func (s *Service) Publish(event Event) bool {
	s.mu.Lock()

	if !s.enabled || !s.settings.Types[event.Type] {
		s.mu.Unlock()

		return false
	}

	s.counter++
	event.ID = fmt.Sprintf("alert-%d-%d", time.Now().UnixNano(), s.counter)

	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	s.events = append(s.events, event)

	if len(s.events) > 100 {
		s.events = s.events[len(s.events)-100:]
	}

	listeners := append([]func(Event){}, s.listeners...)
	s.mu.Unlock()

	for _, hook := range listeners {
		hook(event)
	}

	return true
}

// Notify добавляет системное уведомление в ленту без фильтрации по типам.
func (s *Service) Notify(eventType string, username string, message string) Event {
	event := Event{Type: eventType, Username: username, Message: message}

	s.mu.Lock()
	s.counter++
	event.ID = fmt.Sprintf("alert-%d-%d", time.Now().UnixNano(), s.counter)
	event.CreatedAt = time.Now().UTC()
	s.events = append(s.events, event)

	if len(s.events) > 100 {
		s.events = s.events[len(s.events)-100:]
	}

	s.mu.Unlock()

	return event
}

// Test публикует демонстрационный алерт выбранного типа (в обход фильтров).
func (s *Service) Test(alertType string) Event {
	event := demoEvent(alertType)

	s.mu.Lock()
	s.counter++
	event.ID = fmt.Sprintf("alert-%d-%d", time.Now().UnixNano(), s.counter)
	event.CreatedAt = time.Now().UTC()
	s.events = append(s.events, event)

	if len(s.events) > 100 {
		s.events = s.events[len(s.events)-100:]
	}

	s.mu.Unlock()

	return event
}

/* ── Разбор EventSub-событий ──────────────────────────────────────── */

// HandleNotification превращает событие EventSub в алерт.
func (s *Service) HandleNotification(subscriptionType string, raw json.RawMessage) {
	var (
		event Event
		ok    bool
	)

	switch subscriptionType {
	case "channel.follow":
		event, ok = fromFollow(raw)
	case "channel.subscribe":
		event, ok = fromSubscribe(raw)
	case "channel.subscription.gift":
		event, ok = fromGift(raw)
	case "channel.subscription.message":
		event, ok = fromResub(raw)
	case "channel.cheer":
		event, ok = fromCheer(raw)
	case "channel.raid":
		event, ok = fromRaid(raw)
	case "channel.hype_train.begin":
		event, ok = fromHypeTrain(raw)
	case "channel.goal.begin", "channel.goal.progress", "channel.goal.end":
		event, ok = fromGoal(subscriptionType, raw)
	case "stream.online":
		event, ok = fromStreamOnline(raw)
	case "stream.offline":
		event, ok = fromStreamOffline(raw)
	default:
		return
	}

	if !ok {
		return
	}

	event.Raw = raw
	s.Publish(event)
}

type followEvent struct {
	UserName string `json:"user_name"`
}

type subscribeEvent struct {
	UserName string `json:"user_name"`
	Tier     string `json:"tier"`
}

type giftEvent struct {
	UserName string `json:"user_name"`
	Total    int    `json:"total"`
	Tier     string `json:"tier"`
}

type resubEvent struct {
	UserName string `json:"user_name"`
	Message  struct {
		Text string `json:"text"`
	} `json:"message"`
}

type cheerEvent struct {
	UserName string `json:"user_name"`
	Bits     int    `json:"bits"`
	Message  string `json:"message"`
}

type raidEvent struct {
	FromBroadcasterUserName string `json:"from_broadcaster_user_name"`
	Viewers                 int    `json:"viewers"`
}

type hypeTrainEvent struct {
	Level int `json:"level"`
}

type goalEvent struct {
	Description   string  `json:"description"`
	Type          string  `json:"type"`
	CurrentAmount float64 `json:"current_amount"`
	TargetAmount  float64 `json:"target_amount"`
}

func fromFollow(raw json.RawMessage) (Event, bool) {
	var payload followEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.UserName == "" {
		return Event{}, false
	}

	return Event{Type: "follow", Username: payload.UserName, Message: "подписался на канал"}, true
}

func fromSubscribe(raw json.RawMessage) (Event, bool) {
	var payload subscribeEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.UserName == "" {
		return Event{}, false
	}

	return Event{
		Type:     "subscribe",
		Username: payload.UserName,
		Message:  fmt.Sprintf("оформил подписку (tier %s)", payload.Tier),
	}, true
}

func fromGift(raw json.RawMessage) (Event, bool) {
	var payload giftEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.UserName == "" {
		return Event{}, false
	}

	return Event{
		Type:     "gift",
		Username: payload.UserName,
		Message:  fmt.Sprintf("подарил подписок: %d", payload.Total),
		Amount:   payload.Total,
	}, true
}

func fromResub(raw json.RawMessage) (Event, bool) {
	var payload resubEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.UserName == "" {
		return Event{}, false
	}

	return Event{
		Type:     "resub",
		Username: payload.UserName,
		Message:  payload.Message.Text,
	}, true
}

func fromCheer(raw json.RawMessage) (Event, bool) {
	var payload cheerEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.UserName == "" {
		return Event{}, false
	}

	return Event{
		Type:     "cheer",
		Username: payload.UserName,
		Message:  fmt.Sprintf("накинул %d битсов", payload.Bits),
		Amount:   payload.Bits,
	}, true
}

func fromRaid(raw json.RawMessage) (Event, bool) {
	var payload raidEvent

	if err := json.Unmarshal(raw, &payload); err != nil || payload.FromBroadcasterUserName == "" {
		return Event{}, false
	}

	return Event{
		Type:     "raid",
		Username: payload.FromBroadcasterUserName,
		Message:  fmt.Sprintf("рейд с %d зрителями", payload.Viewers),
		Amount:   payload.Viewers,
	}, true
}

func fromHypeTrain(raw json.RawMessage) (Event, bool) {
	var payload hypeTrainEvent

	if err := json.Unmarshal(raw, &payload); err != nil {
		return Event{}, false
	}

	return Event{
		Type:     "hype_train",
		Username: "Хайп-трейн",
		Message:  fmt.Sprintf("уровень %d", payload.Level),
		Amount:   payload.Level,
	}, true
}

type streamEvent struct {
	Type string `json:"type"`
}

func fromStreamOnline(raw json.RawMessage) (Event, bool) {
	var payload streamEvent

	if err := json.Unmarshal(raw, &payload); err != nil {
		return Event{}, false
	}

	return Event{Type: "stream_online", Username: "Эфир", Message: "стрим начался"}, true
}

func fromStreamOffline(json.RawMessage) (Event, bool) {
	return Event{Type: "stream_offline", Username: "Эфир", Message: "стрим завершён"}, true
}

func fromGoal(subscriptionType string, raw json.RawMessage) (Event, bool) {
	var payload goalEvent

	if err := json.Unmarshal(raw, &payload); err != nil {
		return Event{}, false
	}

	stage := "в процессе"

	switch subscriptionType {
	case "channel.goal.begin":
		stage = "старт"
	case "channel.goal.end":
		stage = "завершена"
	}

	return Event{
		Type:     "goal",
		Username: "Цель",
		Message: fmt.Sprintf(
			"%s · %s: %.0f/%.0f",
			stage,
			payload.Description,
			payload.CurrentAmount,
			payload.TargetAmount,
		),
		Amount: int(payload.CurrentAmount),
	}, true
}

func demoEvent(alertType string) Event {
	switch alertType {
	case "subscribe":
		return Event{Type: "subscribe", Username: "kira_plays", Message: "оформил подписку (tier 1)"}
	case "gift":
		return Event{Type: "gift", Username: "donat1", Message: "подарил подписок: 5", Amount: 5}
	case "resub":
		return Event{Type: "resub", Username: "mary", Message: "продлевает подписку 7 месяцев"}
	case "cheer":
		return Event{Type: "cheer", Username: "bob", Message: "накинул 500 битсов", Amount: 500}
	case "raid":
		return Event{Type: "raid", Username: "streamer_friend", Message: "рейд с 132 зрителями", Amount: 132}
	case "hype_train":
		return Event{Type: "hype_train", Username: "Хайп-трейн", Message: "уровень 3", Amount: 3}
	case "goal":
		return Event{Type: "goal", Username: "Цель", Message: "в процессе · Подписки: 45/100", Amount: 45}
	default:
		return Event{Type: "follow", Username: "новый_зритель", Message: "подписался на канал"}
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/alerts", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{
			"events":   service.Recent(30),
			"settings": service.GetSettings(),
		})
	})

	srv.ServeEndpoint("/rgg-sync/alerts/settings", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch && request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var settings Settings

		if err := json.NewDecoder(request.Body).Decode(&settings); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		writeJSON(response, http.StatusOK, service.SetSettings(settings))
	})

	srv.ServeEndpoint("/rgg-sync/alerts/notify", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Type     string `json:"type"`
			Username string `json:"username"`
			Message  string `json:"message"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			response.WriteHeader(http.StatusBadRequest)

			return
		}

		event := service.Notify(body.Type, body.Username, body.Message)
		_ = json.NewEncoder(response).Encode(event)
	})

	srv.ServeEndpoint("/rgg-sync/alerts/test", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Type string `json:"type"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		writeJSON(response, http.StatusOK, service.Test(body.Type))
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
