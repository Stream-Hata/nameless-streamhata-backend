// Package welcome — приветственные сообщения новым чатерам.
package welcome

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"

	"github.com/p7x-dev/nameless-streamhata-backend/bots"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

const storageKey = "welcome_config"

type Config struct {
	Enabled  bool     `json:"enabled"`
	Messages []string `json:"messages"`
}

type Service struct {
	mu     sync.Mutex
	config Config
	seen   map[string]bool
	db     *sql.DB
	writer chat.Writer
}

func New(db *sql.DB, writer chat.Writer) *Service {
	service := &Service{
		config: Config{Enabled: true, Messages: []string{"@{user}, добро пожаловать на стрим!"}},
		seen:   make(map[string]bool),
		db:     db,
		writer: writer,
	}
	service.load()

	return service
}

func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	return Config{Enabled: s.config.Enabled, Messages: append([]string(nil), s.config.Messages...)}
}

func (s *Service) Save(config Config) Config {
	messages := make([]string, 0, len(config.Messages))
	for _, message := range config.Messages {
		if message = strings.TrimSpace(message); message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		messages = []string{"@{user}, добро пожаловать на стрим!"}
	}

	s.mu.Lock()
	s.config = Config{Enabled: config.Enabled, Messages: messages}
	s.mu.Unlock()
	s.persist()

	return s.Config()
}

func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	if message.Author.ID == "" || message.Author.ID == message.Broadcaster.ID || bots.Is(message.Author.Login) {
		return
	}

	s.mu.Lock()
	if !s.config.Enabled || s.seen[message.Author.ID] {
		s.mu.Unlock()

		return
	}
	s.seen[message.Author.ID] = true
	messages := append([]string(nil), s.config.Messages...)
	s.mu.Unlock()

	if s.writer == nil || len(messages) == 0 {
		return
	}

	text := messages[rand.IntN(len(messages))]
	text = strings.ReplaceAll(text, "{user}", message.Author.DisplayName)
	text = strings.ReplaceAll(text, "{login}", message.Author.Login)
	text = strings.ReplaceAll(text, "{channel}", message.Broadcaster.DisplayName)
	_ = s.writer.Say(text)
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = $1`, storageKey).Scan(&raw); err != nil {
		return
	}

	var config Config
	if json.Unmarshal(raw, &config) != nil {
		return
	}
	if len(config.Messages) > 0 {
		s.config = config
	}
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.Config())
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, storageKey, string(raw))
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/welcome", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, service.Config())

			return
		}
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var config Config
		if err := json.NewDecoder(request.Body).Decode(&config); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}
		writeJSON(response, http.StatusOK, service.Save(config))
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
