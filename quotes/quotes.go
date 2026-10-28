// Package quotes — цитаты чата: !quote, !quote <n>, !quote add, !quote del.
package quotes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type Quote struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	Text      string    `json:"text"`
	Author    string    `json:"author"`
	AddedBy   string    `json:"addedBy"`
	Votes     int       `json:"votes"`
	CreatedAt time.Time `json:"createdAt"`
}

type Service struct {
	mu     sync.Mutex
	quotes []Quote
	next   int

	writer   chat.Writer
	db       *sql.DB
	voters   map[string]map[string]bool
	randN    func(int) int
	triggers *triggers.Service
}

func New(writer chat.Writer, db *sql.DB) *Service {
	service := &Service{
		writer:   writer,
		db:       db,
		voters:   make(map[string]map[string]bool),
		randN:    rand.IntN,
		triggers: triggers.New(nil),
	}

	service.load()
	service.loadVotes()

	return service
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "quotes"
}

func (s *Service) Name() string {
	return "Цитаты"
}

func (s *Service) Kind() string {
	return "quotes"
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

	return fmt.Sprintf("цитат: %d", len(s.quotes))
}

func (s *Service) List() []Quote {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Quote, 0, len(s.quotes))

	for i := len(s.quotes) - 1; i >= 0; i-- {
		result = append(result, s.quotes[i])
	}

	return result
}

func (s *Service) Add(text, author, addedBy string) (Quote, error) {
	text = strings.TrimSpace(text)

	if text == "" {
		return Quote{}, fmt.Errorf("цитата не может быть пустой")
	}

	s.mu.Lock()
	s.next++
	quote := Quote{
		ID:        fmt.Sprintf("quote-%d", time.Now().UnixNano()),
		Number:    s.next,
		Text:      text,
		Author:    author,
		AddedBy:   addedBy,
		CreatedAt: time.Now().UTC(),
	}
	s.quotes = append(s.quotes, quote)
	s.mu.Unlock()

	s.persist(quote)

	return quote, nil
}

func (s *Service) Vote(id, userID string) (Quote, bool) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "#")
	userID = strings.TrimSpace(userID)

	if userID == "" {
		return Quote{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for index := range s.quotes {
		quote := &s.quotes[index]
		if quote.ID != id && fmt.Sprintf("%d", quote.Number) != id {
			continue
		}

		if s.voters[quote.ID] == nil {
			s.voters[quote.ID] = make(map[string]bool)
		}
		if s.voters[quote.ID][userID] {
			return *quote, false
		}

		s.voters[quote.ID][userID] = true
		quote.Votes++
		snapshot := *quote
		s.persistVotes()

		return snapshot, true
	}

	return Quote{}, false
}

func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for index, quote := range s.quotes {
		if quote.ID == id || fmt.Sprintf("%d", quote.Number) == id {
			s.quotes = append(s.quotes[:index], s.quotes[index+1:]...)

			delete(s.voters, quote.ID)

			if s.db != nil {
				_, _ = s.db.ExecContext(context.Background(), `DELETE FROM quote WHERE id = $1`, quote.ID)
			}
			s.persistVotes()

			return true
		}
	}

	return false
}

// SetTriggers подключает настраиваемые стримером команды.
func (s *Service) SetTriggers(service *triggers.Service) {
	s.triggers = service
}

// HandleMessage обрабатывает команды цитат из чата.
func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	action, fields, ok := s.triggers.Match(message.Text)

	if !ok || action != "quotes.quote" {
		return
	}

	// !quote add <текст>
	if len(fields) >= 2 && strings.EqualFold(fields[0], "add") {
		text := strings.Join(fields[1:], " ")
		quote, err := s.Add(text, message.Author.DisplayName, message.Author.DisplayName)

		if err != nil {
			s.reply("Не удалось добавить цитату: " + err.Error())

			return
		}

		s.reply(fmt.Sprintf("Цитата #%d добавлена.", quote.Number))

		return
	}

	// !quote del <номер> — только стример.
	if len(fields) >= 2 && strings.EqualFold(fields[0], "del") {
		if message.Author.ID != message.Broadcaster.ID {
			return
		}

		if s.Delete(fields[1]) {
			s.reply("Цитата удалена.")
		}

		return
	}

	// !quote vote <номер>
	if len(fields) >= 2 && strings.EqualFold(fields[0], "vote") {
		quote, ok := s.Vote(fields[1], message.Author.ID)
		if !ok {
			s.reply("Голос не учтён: цитата не найдена или ты уже голосовал.")
		} else {
			s.reply(fmt.Sprintf("Голос за цитату #%d учтён (голосов: %d).", quote.Number, quote.Votes))
		}

		return
	}

	// !quote <номер>
	if len(fields) >= 1 {
		s.mu.Lock()

		for _, quote := range s.quotes {
			if fmt.Sprintf("%d", quote.Number) == fields[0] {
				s.mu.Unlock()
				s.reply(fmt.Sprintf("#%d: «%s» — %s", quote.Number, quote.Text, quote.Author))

				return
			}
		}

		s.mu.Unlock()
		s.reply("Цитата не найдена.")

		return
	}

	// !quote — случайная.
	s.mu.Lock()

	if len(s.quotes) == 0 {
		s.mu.Unlock()
		s.reply("Цитат пока нет. Добавить: !quote add <текст>")

		return
	}

	quote := s.quotes[s.randN(len(s.quotes))]
	s.mu.Unlock()

	s.reply(fmt.Sprintf("#%d: «%s» — %s", quote.Number, quote.Text, quote.Author))
}

func (s *Service) RandomText() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.quotes) == 0 {
		return "Цитат пока нет."
	}

	quote := s.quotes[s.randN(len(s.quotes))]

	return fmt.Sprintf("#%d: «%s» — %s", quote.Number, quote.Text, quote.Author)
}

func (s *Service) reply(text string) {
	if s.writer == nil || text == "" {
		return
	}

	_ = s.writer.Say(text)
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) persist(quote Quote) {
	if s.db == nil {
		return
	}

	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO quote (id, text, author, added_by, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`,
		quote.ID, quote.Text, quote.Author, quote.AddedBy, quote.CreatedAt)
	if err != nil {
		log.Printf("[Quotes] persist failed: %v", err)
	}
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, text, author, added_by, created_at FROM quote ORDER BY created_at ASC`)
	if err != nil {
		log.Printf("[Quotes] load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var quote Quote

		if err := rows.Scan(&quote.ID, &quote.Text, &quote.Author, &quote.AddedBy, &quote.CreatedAt); err != nil {
			continue
		}

		s.next++
		quote.Number = s.next
		s.quotes = append(s.quotes, quote)
	}
}

func (s *Service) loadVotes() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'quote_votes'`).Scan(&raw); err != nil {
		return
	}

	_ = json.Unmarshal(raw, &s.voters)

	s.mu.Lock()
	for index := range s.quotes {
		if voters := s.voters[s.quotes[index].ID]; voters != nil {
			s.quotes[index].Votes = len(voters)
		}
	}
	s.mu.Unlock()
}

func (s *Service) persistVotes() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.voters)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('quote_votes', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/quotes", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"quotes": service.List()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Text   string `json:"text"`
			Author string `json:"author"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		quote, err := service.Add(body.Text, body.Author, "panel")
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, quote)
	})

	srv.ServeEndpoint("/rgg-sync/quotes/{id}/vote", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			UserID string `json:"userId"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)

		quote, ok := service.Vote(request.PathValue("id"), body.UserID)
		if !ok {
			writeJSON(response, http.StatusConflict, map[string]string{"error": "already voted or quote not found"})

			return
		}

		writeJSON(response, http.StatusOK, quote)
	})

	srv.ServeEndpoint("/rgg-sync/quotes/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "quote not found"})

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
