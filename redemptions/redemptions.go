// Package redemptions — channel points: привязка наград к действиям.
package redemptions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/points"
)

// Mapping — привязка названия награды к действию.
type Mapping struct {
	ID          string `json:"id"`
	RewardTitle string `json:"rewardTitle"`
	Action      string `json:"action"`
	Payload     string `json:"payload"`
	Enabled     bool   `json:"enabled"`
}

// Record — запись о выкупе.
type Record struct {
	ID           string    `json:"id"`
	RedemptionID string    `json:"redemptionId"`
	RewardID     string    `json:"rewardId"`
	UserID       string    `json:"userId"`
	UserName     string    `json:"userName"`
	RewardTitle  string    `json:"rewardTitle"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Actioner — действия, которые может выполнить сервис.
type Actioner interface {
	Say(text string) error
	TimeoutUser(ctx context.Context, userID string, seconds int, reason string) error
	FulfillRedemption(ctx context.Context, rewardID, redemptionID string) error
}

type Service struct {
	mu       sync.Mutex
	mappings map[string]*Mapping
	records  []Record

	points  *points.Service
	actions Actioner
	db      *sql.DB
	now     func() time.Time

	wheelEntry func(userID, login, displayName, title string, luck bool, bet int) error
}

func New(pointsService *points.Service, actions Actioner, db *sql.DB) *Service {
	service := &Service{
		mappings: make(map[string]*Mapping),
		records:  make([]Record, 0, 100),
		points:   pointsService,
		actions:  actions,
		db:       db,
		now:      time.Now,
	}

	service.load()

	return service
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "redemptions"
}

func (s *Service) Name() string {
	return "Channel Points"
}

func (s *Service) Kind() string {
	return "redemptions"
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

	return fmt.Sprintf("привязок: %d, выкупов: %d", len(s.mappings), len(s.records))
}

/* ── CRUD ─────────────────────────────────────────────────────────── */

func (s *Service) Mappings() []Mapping {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]Mapping, 0, len(s.mappings))

	for _, mapping := range s.mappings {
		list = append(list, *mapping)
	}

	return list
}

func (s *Service) Upsert(mapping Mapping) (Mapping, error) {
	mapping.RewardTitle = strings.TrimSpace(mapping.RewardTitle)

	if mapping.RewardTitle == "" {
		return Mapping{}, fmt.Errorf("укажите название награды из Twitch")
	}

	switch mapping.Action {
	case "say", "points", "timeout", "wheel":
	default:
		return Mapping{}, fmt.Errorf("действие должно быть say, points, timeout или wheel")
	}

	if mapping.ID == "" {
		mapping.ID = fmt.Sprintf("map-%d", s.now().UnixNano())
		mapping.Enabled = true
	}

	s.mu.Lock()
	s.mappings[mapping.ID] = &mapping
	s.mu.Unlock()

	s.persistMapping(mapping)

	return mapping, nil
}

func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.mappings[id]; !ok {
		return false
	}

	delete(s.mappings, id)

	if s.db != nil {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM redemption_mapping WHERE id = $1`, id)
	}

	return true
}

func (s *Service) Recent(limit int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > len(s.records) {
		limit = len(s.records)
	}

	result := make([]Record, 0, limit)

	for i := len(s.records) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, s.records[i])
	}

	return result
}

/* ── Обработка событий EventSub ───────────────────────────────────── */

type redemptionEvent struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	UserID    string `json:"user_id"`
	UserLogin string `json:"user_login"`
	UserName  string `json:"user_name"`

	Reward struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Cost  int    `json:"cost"`
	} `json:"reward"`
	UserInput string `json:"user_input"`
}

func (s *Service) SetWheelEntry(entry func(userID, login, displayName, title string, luck bool, bet int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.wheelEntry = entry
}

func (s *Service) HandleNotification(subscriptionType string, raw json.RawMessage) {
	var event redemptionEvent

	if err := json.Unmarshal(raw, &event); err != nil || event.ID == "" {
		return
	}

	if subscriptionType == "channel.channel_points_custom_reward_redemption.update" {
		s.updateStatus(event)

		return
	}

	record := Record{
		ID:           fmt.Sprintf("rec-%d", s.now().UnixNano()),
		RedemptionID: event.ID,
		RewardID:     event.Reward.ID,
		UserID:       event.UserID,
		UserName:     displayName(event),
		RewardTitle:  event.Reward.Title,
		Status:       orDefault(event.Status, "unfulfilled"),
		CreatedAt:    s.now().UTC(),
	}

	s.mu.Lock()
	s.records = append(s.records, record)

	if len(s.records) > 100 {
		s.records = s.records[len(s.records)-100:]
	}

	mapping := s.matchLocked(event.Reward.Title)
	s.mu.Unlock()

	if mapping == nil {
		return
	}

	s.execute(context.Background(), *mapping, event)
}

func (s *Service) updateStatus(event redemptionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for index := range s.records {
		if s.records[index].RedemptionID == event.ID {
			s.records[index].Status = orDefault(event.Status, s.records[index].Status)
		}
	}
}

func (s *Service) matchLocked(title string) *Mapping {
	for _, mapping := range s.mappings {
		if mapping.Enabled && strings.EqualFold(mapping.RewardTitle, title) {
			return mapping
		}
	}

	return nil
}

func (s *Service) execute(ctx context.Context, mapping Mapping, event redemptionEvent) {
	userID := event.UserID
	login := event.UserLogin
	name := displayName(event)

	fulfilled := true

	switch mapping.Action {
	case "say":
		text := strings.ReplaceAll(mapping.Payload, "$(user)", name)

		if s.actions != nil {
			if err := s.actions.Say(text); err != nil {
				log.Printf("[Redemptions] say failed: %v", err)
			}
		}
	case "points":
		delta, err := strconv.Atoi(strings.TrimSpace(mapping.Payload))

		if err == nil && s.points != nil {
			s.points.Add(userID, login, name, delta, "channel points: "+event.Reward.Title)
		}
	case "timeout":
		seconds, err := strconv.Atoi(strings.TrimSpace(mapping.Payload))

		if err == nil && s.actions != nil {
			if err := s.actions.TimeoutUser(ctx, userID, seconds, "channel points: "+event.Reward.Title); err != nil {
				log.Printf("[Redemptions] timeout failed: %v", err)
				fulfilled = false
			}
		}
	case "wheel":
		message := strings.TrimSpace(event.UserInput)
		title := message

		if title == "" {
			title = strings.TrimSpace(mapping.Payload)
		}

		if title == "" {
			title = event.Reward.Title
		}

		luckText := strings.ToLower(message + " " + mapping.Payload + " " + event.Reward.Title)
		luck := strings.Contains(luckText, "удач") || strings.Contains(luckText, "luck")
		if s.wheelEntry != nil {
			if err := s.wheelEntry(userID, login, name, title, luck, event.Reward.Cost); err != nil {
				log.Printf("[Redemptions] wheel entry failed: %v", err)
				fulfilled = false
			}
		}
	}

	if fulfilled && s.actions != nil && event.Reward.ID != "" {
		if err := s.actions.FulfillRedemption(ctx, event.Reward.ID, event.ID); err != nil {
			log.Printf("[Redemptions] fulfill failed: %v", err)
		}
	}
}

func displayName(event redemptionEvent) string {
	if event.UserName != "" {
		return event.UserName
	}

	return event.UserLogin
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) persistMapping(mapping Mapping) {
	if s.db == nil {
		return
	}

	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO redemption_mapping (id, reward_title, action, payload, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			reward_title = EXCLUDED.reward_title,
			action = EXCLUDED.action,
			payload = EXCLUDED.payload,
			enabled = EXCLUDED.enabled`,
		mapping.ID, mapping.RewardTitle, mapping.Action, mapping.Payload, mapping.Enabled)
	if err != nil {
		log.Printf("[Redemptions] persist failed: %v", err)
	}
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, reward_title, action, payload, enabled FROM redemption_mapping`)
	if err != nil {
		log.Printf("[Redemptions] load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var mapping Mapping

		if err := rows.Scan(&mapping.ID, &mapping.RewardTitle, &mapping.Action, &mapping.Payload, &mapping.Enabled); err != nil {
			continue
		}

		s.mappings[mapping.ID] = &mapping
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/redemptions", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{
				"mappings": service.Mappings(),
				"recent":   service.Recent(30),
			})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var mapping Mapping

		if err := json.NewDecoder(request.Body).Decode(&mapping); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		saved, err := service.Upsert(mapping)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, saved)
	})

	srv.ServeEndpoint("/rgg-sync/redemptions/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "mapping not found"})

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
