package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TelegramCommand — настраиваемая команда Telegram-бота.
type TelegramCommand struct {
	ID             string           `json:"id"`
	Name           string           `json:"name,omitempty"`
	Command        string           `json:"command"`
	Aliases        []string         `json:"aliases,omitempty"`
	Description    string           `json:"description,omitempty"`
	Action         string           `json:"action,omitempty"`
	DeleteCommand  bool             `json:"deleteCommand,omitempty"`
	Response       string           `json:"response"`
	ParseMode      string           `json:"parseMode,omitempty"`
	DisablePreview bool             `json:"disablePreview,omitempty"`
	Enabled        bool             `json:"enabled"`
	Buttons        []TelegramButton `json:"buttons,omitempty"`
}

type TelegramButton struct {
	Text             string `json:"text"`
	URL              string `json:"url,omitempty"`
	Callback         string `json:"callback,omitempty"`
	CallbackResponse string `json:"callbackResponse,omitempty"`
	Color            string `json:"color,omitempty"`
}

func (s *Service) loadTelegramCommands() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'telegram_commands'`).Scan(&raw); err != nil {
		s.telegramCommands = defaultTelegramCommands()
		s.persistTelegramCommands()

		return
	}

	var commands []TelegramCommand
	if json.Unmarshal(raw, &commands) == nil && len(commands) > 0 {
		s.telegramCommands = commands

		return
	}

	s.telegramCommands = defaultTelegramCommands()
	s.persistTelegramCommands()
}

func defaultTelegramCommands() []TelegramCommand {
	return []TelegramCommand{
		{ID: "tg-start", Name: "Старт", Command: "/start", Response: "Привет, {user}! Я бот канала.", Enabled: true},
		{ID: "tg-help", Name: "Помощь", Command: "/help", Response: "Команды: /status, /music, /ping", Enabled: true},
		{ID: "tg-status", Name: "Статус", Command: "/status", Action: "status", Enabled: true},
		{ID: "tg-music", Name: "Музыка", Command: "/music", Action: "music", Enabled: true},
		{ID: "tg-ping", Name: "Пинг", Command: "/ping", Action: "ping", Enabled: true},
	}
}

func (s *Service) persistTelegramCommands() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.telegramCommands)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('telegram_commands', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

func (s *Service) TelegramCommands() []TelegramCommand {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]TelegramCommand{}, s.telegramCommands...)
}

func (s *Service) SaveTelegramCommand(command TelegramCommand) (TelegramCommand, error) {
	command.Command = strings.TrimSpace(command.Command)
	if command.Command == "" {
		return command, errTelegramCommandName
	}
	if !strings.HasPrefix(command.Command, "/") {
		command.Command = "/" + command.Command
	}
	command.Command = strings.ToLower(command.Command)
	command.Response = strings.TrimSpace(command.Response)

	if command.ID == "" {
		buffer := make([]byte, 8)
		_, _ = rand.Read(buffer)
		command.ID = "tg-" + hex.EncodeToString(buffer)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	replaced := false
	for index := range s.telegramCommands {
		if s.telegramCommands[index].ID == command.ID {
			s.telegramCommands[index] = command
			replaced = true

			break
		}
	}
	if !replaced {
		s.telegramCommands = append(s.telegramCommands, command)
	}
	s.persistTelegramCommands()
	go func() { _ = s.SyncTelegramMenu(context.Background()) }()

	return command, nil
}

func (s *Service) DeleteTelegramCommand(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for index := range s.telegramCommands {
		if s.telegramCommands[index].ID == id {
			s.telegramCommands = append(s.telegramCommands[:index], s.telegramCommands[index+1:]...)
			s.persistTelegramCommands()
			go func() { _ = s.SyncTelegramMenu(context.Background()) }()

			return true
		}
	}

	return false
}

func (s *Service) matchTelegramCommand(text, userName string) (TelegramCommand, string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return TelegramCommand{}, "", false
	}

	name := strings.ToLower(fields[0])
	args := strings.Join(fields[1:], " ")

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, command := range s.telegramCommands {
		if !command.Enabled || !matchesTelegramCommand(command, name) {
			continue
		}

		response := strings.NewReplacer(
			"{user}", userName,
			"{name}", userName,
			"{args}", args,
			"{date}", time.Now().Format("02.01.2006"),
			"{time}", time.Now().Format("15:04"),
		).Replace(command.Response)

		return command, response, true
	}

	return TelegramCommand{}, "", false
}

func registerTelegramCommandRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/telegram/commands", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, map[string]any{"commands": service.TelegramCommands()})
		case http.MethodPost:
			var command TelegramCommand
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			saved, err := service.SaveTelegramCommand(command)
			if err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

				return
			}
			writeJSON(response, http.StatusOK, saved)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/telegram/commands/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}
		if !service.DeleteTelegramCommand(request.PathValue("id")) {
			http.Error(response, "not found", http.StatusNotFound)

			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
}

var errTelegramCommandName = &telegramCommandError{"command is required"}

type telegramCommandError struct{ text string }

func (e *telegramCommandError) Error() string { return e.text }

func matchesTelegramCommand(command TelegramCommand, name string) bool {
	if strings.ToLower(command.Command) == name {
		return true
	}

	for _, alias := range command.Aliases {
		alias = strings.TrimSpace(strings.ToLower(alias))
		if alias != "" && !strings.HasPrefix(alias, "/") {
			alias = "/" + alias
		}
		if alias == name {
			return true
		}
	}

	return false
}

// SyncTelegramMenu обновляет меню команд в клиенте Telegram.
func (s *Service) SyncTelegramMenu(ctx context.Context) error {
	settings := s.GetSettings()
	token := strings.TrimSpace(settings.TelegramToken)
	if token == "" {
		return fmt.Errorf("telegram token не задан")
	}

	s.mu.Lock()
	items := append([]TelegramCommand(nil), s.telegramCommands...)
	s.mu.Unlock()

	commands := make([]map[string]string, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item.Command)), "/")
		if name == "" {
			continue
		}
		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = strings.TrimSpace(item.Name)
		}
		if description == "" {
			description = name
		}
		commands = append(commands, map[string]string{
			"command":     name,
			"description": description,
		})
	}

	return s.callTelegram(ctx, token, "setMyCommands", map[string]any{"commands": commands})
}
