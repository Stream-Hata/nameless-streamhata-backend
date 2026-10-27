// Package commands — кастомные команды чата с переменными, кулдаунами,
// уровнями доступа и счётчиками.
package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Command — пользовательская команда чата.
type Command struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Aliases         []string  `json:"aliases"`
	Response        string    `json:"response"`
	CooldownSeconds int       `json:"cooldownSeconds"`
	UserLevel       string    `json:"userLevel"`
	Enabled         bool      `json:"enabled"`
	Counter         int       `json:"counter"`
	CreatedAt       time.Time `json:"createdAt"`
}

var levels = map[string]int{
	"everyone":    0,
	"subscriber":  1,
	"vip":         2,
	"moderator":   3,
	"broadcaster": 4,
}

// Service хранит команды, кулдауны и обрабатывает сообщения чата.
type Service struct {
	mu        sync.Mutex
	commands  map[string]*Command
	cooldowns map[string]time.Time

	writer chat.Writer
	db     *sql.DB
	now    func() time.Time
}

func New(writer chat.Writer, db *sql.DB) *Service {
	service := &Service{
		commands:  make(map[string]*Command),
		cooldowns: make(map[string]time.Time),
		writer:    writer,
		db:        db,
		now:       time.Now,
	}

	service.load()

	return service
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "commands"
}

func (s *Service) Name() string {
	return "Команды чата"
}

func (s *Service) Kind() string {
	return "commands"
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

	return fmt.Sprintf("команд: %d", len(s.commands))
}

/* ── CRUD ─────────────────────────────────────────────────────────── */

func (s *Service) List() []Command {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]Command, 0, len(s.commands))

	for _, command := range s.commands {
		list = append(list, *command)
	}

	return list
}

func (s *Service) Upsert(command Command) (Command, error) {
	command.Name = normalizeName(command.Name)

	if command.Name == "" {
		return Command{}, fmt.Errorf("команда должна иметь имя")
	}

	if command.Response == "" {
		return Command{}, fmt.Errorf("команда должна иметь ответ")
	}

	if _, ok := levels[command.UserLevel]; !ok {
		command.UserLevel = "everyone"
	}

	if command.ID == "" {
		command.ID = fmt.Sprintf("cmd-%d", time.Now().UnixNano())
		command.CreatedAt = s.now().UTC()
	}

	s.mu.Lock()
	s.commands[command.Name] = &command
	s.mu.Unlock()

	s.persist(command)

	return command, nil
}

func (s *Service) SetCommandEnabled(id string, enabled bool) (Command, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, command := range s.commands {
		if command.ID != id {
			continue
		}

		command.Enabled = enabled
		snapshot := *command
		s.persist(snapshot)

		return snapshot, true
	}

	return Command{}, false
}

func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, command := range s.commands {
		if command.ID == id {
			delete(s.commands, name)

			if s.db != nil {
				_, _ = s.db.ExecContext(context.Background(), `DELETE FROM command WHERE id = $1`, id)
			}

			return true
		}
	}

	return false
}

/* ── Обработка чата ───────────────────────────────────────────────── */

// HandleMessage вызывается из шины чата.
func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	text := strings.TrimSpace(message.Text)

	if !strings.HasPrefix(text, "!") {
		return
	}

	parts := strings.Fields(text)
	name := normalizeName(parts[0])

	s.mu.Lock()

	command, ok := s.commands[name]

	if !ok {
		command = s.findByAliasLocked(name)
	}

	if command == nil || !command.Enabled {
		s.mu.Unlock()

		return
	}

	userKey := command.Name + ":" + message.Author.ID
	now := s.now()

	if until, cooling := s.cooldowns[userKey]; cooling && now.Before(until) {
		s.mu.Unlock()

		return
	}

	command.Counter++
	response := render(command.Response, message, command.Counter)

	if command.CooldownSeconds > 0 {
		s.cooldowns[userKey] = now.Add(time.Duration(command.CooldownSeconds) * time.Second)
	}

	s.mu.Unlock()

	s.persist(*command)

	if s.writer != nil {
		if err := s.writer.Say(response); err != nil {
			log.Printf("[Commands] send failed: %v", err)
		}
	}
}

// Test рендерит ответ команды без отправки (кнопка «Тест» в панели).
func (s *Service) Test(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, command := range s.commands {
		if command.ID == id {
			sample := twitch.TwitchChatMessage{
				Author: twitch.ChatUser{ID: "test", Login: "tester", DisplayName: "Тестер"},
			}

			return render(command.Response, sample, command.Counter+1), true
		}
	}

	return "", false
}

func (s *Service) findByAliasLocked(alias string) *Command {
	for _, command := range s.commands {
		for _, item := range command.Aliases {
			if normalizeName(item) == alias {
				return command
			}
		}
	}

	return nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "!"))
}

// render подставляет переменные: $(user), $(login), $(channel), $(count), $(date), $(time).
func render(template string, message twitch.TwitchChatMessage, counter int) string {
	now := time.Now()

	channel := message.Broadcaster.DisplayName

	if channel == "" {
		channel = message.Broadcaster.Login
	}

	display := message.Author.DisplayName

	if display == "" {
		display = message.Author.Login
	}

	replacements := map[string]string{
		"$(user)":    display,
		"$(login)":   message.Author.Login,
		"$(channel)": channel,
		"$(count)":   fmt.Sprintf("%d", counter),
		"$(date)":    now.Format("02.01.2006"),
		"$(time)":    now.Format("15:04"),
	}

	result := template

	for key, value := range replacements {
		result = strings.ReplaceAll(result, key, value)
	}

	return result
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) persist(command Command) {
	if s.db == nil {
		return
	}

	aliases, err := json.Marshal(command.Aliases)
	if err != nil {
		return
	}

	query := `
		INSERT INTO command (id, name, aliases, response, cooldown_seconds, user_level, enabled, counter, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			aliases = EXCLUDED.aliases,
			response = EXCLUDED.response,
			cooldown_seconds = EXCLUDED.cooldown_seconds,
			user_level = EXCLUDED.user_level,
			enabled = EXCLUDED.enabled,
			counter = EXCLUDED.counter
	`

	_, err = s.db.ExecContext(context.Background(), query,
		command.ID,
		command.Name,
		string(aliases),
		command.Response,
		command.CooldownSeconds,
		command.UserLevel,
		command.Enabled,
		command.Counter,
		command.CreatedAt,
	)
	if err != nil {
		log.Printf("[Commands] persist failed: %v", err)
	}
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, name, aliases, response, cooldown_seconds, user_level, enabled, counter, created_at FROM command`)
	if err != nil {
		log.Printf("[Commands] load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			command Command
			aliases string
		)

		if err := rows.Scan(
			&command.ID,
			&command.Name,
			&aliases,
			&command.Response,
			&command.CooldownSeconds,
			&command.UserLevel,
			&command.Enabled,
			&command.Counter,
			&command.CreatedAt,
		); err != nil {
			continue
		}

		_ = json.Unmarshal([]byte(aliases), &command.Aliases)
		s.commands[command.Name] = &command
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/commands", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"commands": service.List()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var command Command

		if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		saved, err := service.Upsert(command)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, saved)
	})

	srv.ServeEndpoint("/rgg-sync/commands/{id}/test", func(response http.ResponseWriter, request *http.Request) {
		text, ok := service.Test(request.PathValue("id"))

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]string{"response": text})
	})

	srv.ServeEndpoint("/rgg-sync/commands/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch {
			var body struct {
				Enabled bool `json:"enabled"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			command, ok := service.SetCommandEnabled(request.PathValue("id"), body.Enabled)
			if !ok {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

				return
			}

			writeJSON(response, http.StatusOK, command)

			return
		}

		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

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
