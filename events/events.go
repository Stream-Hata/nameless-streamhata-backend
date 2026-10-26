// Package events — события канала: план, статус, история.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Event struct {
	ID        int        `json:"id"`
	Title     string     `json:"title"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	StartsAt  *time.Time `json:"startsAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type Service struct {
	mu     sync.Mutex
	items  []Event
	nextID int
	db     *sql.DB
}

func New(db *sql.DB) *Service {
	service := &Service{db: db, nextID: 1}
	service.load()

	return service
}

func (s *Service) List() []Event {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()

		return append([]Event(nil), s.items...)
	}

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, title, type, status, starts_at, created_at
		FROM stream_event
		ORDER BY starts_at NULLS LAST, created_at DESC
	`)
	if err != nil {
		return []Event{}
	}

	defer func() {
		_ = rows.Close()
	}()

	list := make([]Event, 0)

	for rows.Next() {
		var event Event

		if err := rows.Scan(
			&event.ID,
			&event.Title,
			&event.Type,
			&event.Status,
			&event.StartsAt,
			&event.CreatedAt,
		); err != nil {
			continue
		}

		list = append(list, event)
	}

	return list
}

func (s *Service) Create(title, eventType string, startsAt *time.Time) Event {
	title = strings.TrimSpace(title)
	eventType = strings.TrimSpace(eventType)

	if eventType == "" {
		eventType = "custom"
	}

	event := Event{
		Title:     title,
		Type:      eventType,
		Status:    "planned",
		StartsAt:  startsAt,
		CreatedAt: time.Now().UTC(),
	}

	if s.db == nil {
		s.mu.Lock()
		event.ID = s.nextID
		s.nextID++
		s.items = append(s.items, event)
		s.mu.Unlock()

		return event
	}

	err := s.db.QueryRowContext(
		context.Background(),
		`INSERT INTO stream_event (title, type, status, starts_at)
		 VALUES ($1, $2, 'planned', $3)
		 RETURNING id, created_at`,
		title,
		eventType,
		startsAt,
	).Scan(&event.ID, &event.CreatedAt)
	if err != nil {
		return event
	}

	return event
}

func (s *Service) UpdateStatus(id int, status string) (Event, bool) {
	status = strings.TrimSpace(status)

	if status == "" {
		status = "planned"
	}

	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()

		for index := range s.items {
			if s.items[index].ID == id {
				s.items[index].Status = status

				return s.items[index], true
			}
		}

		return Event{}, false
	}

	var event Event

	err := s.db.QueryRowContext(
		context.Background(),
		`UPDATE stream_event SET status = $1 WHERE id = $2
		 RETURNING id, title, type, status, starts_at, created_at`,
		status,
		id,
	).Scan(&event.ID, &event.Title, &event.Type, &event.Status, &event.StartsAt, &event.CreatedAt)
	if err != nil {
		return Event{}, false
	}

	return event, true
}

func (s *Service) Delete(id int) bool {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()

		for index, event := range s.items {
			if event.ID == id {
				s.items = append(s.items[:index], s.items[index+1:]...)

				return true
			}
		}

		return false
	}

	result, err := s.db.ExecContext(context.Background(), `DELETE FROM stream_event WHERE id = $1`, id)
	if err != nil {
		return false
	}

	affected, _ := result.RowsAffected()

	return affected > 0
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	s.items = s.List()
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/events", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, map[string]any{"events": service.List()})
		case http.MethodPost:
			var body struct {
				Title    string `json:"title"`
				Type     string `json:"type"`
				StartsAt string `json:"startsAt"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			if strings.TrimSpace(body.Title) == "" {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "title is required"})

				return
			}

			var startsAt *time.Time

			if body.StartsAt != "" {
				parsed, err := time.Parse(time.RFC3339, body.StartsAt)
				if err == nil {
					startsAt = &parsed
				}
			}

			writeJSON(response, http.StatusOK, service.Create(body.Title, body.Type, startsAt))
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/events/{id}", func(response http.ResponseWriter, request *http.Request) {
		id, err := strconv.Atoi(request.PathValue("id"))
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid id"})

			return
		}

		switch request.Method {
		case http.MethodPatch:
			var body struct {
				Status string `json:"status"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			event, ok := service.UpdateStatus(id, body.Status)
			if !ok {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "event not found"})

				return
			}

			writeJSON(response, http.StatusOK, event)
		case http.MethodDelete:
			if !service.Delete(id) {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "event not found"})

				return
			}

			writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
