package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// TelegramCommand — настраиваемая команда Telegram-бота.
type TelegramCommand struct {
	ID             string           `json:"id"`
	Command        string           `json:"command"`
	Aliases        []string         `json:"aliases,omitempty"`
	Description    string           `json:"description,omitempty"`
	Response       string           `json:"response"`
	ParseMode      string           `json:"parseMode,omitempty"`
	DisablePreview bool             `json:"disablePreview,omitempty"`
	Enabled        bool             `json:"enabled"`
	Buttons        []TelegramButton `json:"buttons,omitempty"`
}

type TelegramButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

func (s *Service) loadTelegramCommands() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'telegram_commands'`).Scan(&raw); err != nil {
		return
	}

	var commands []TelegramCommand
	if json.Unmarshal(raw, &commands) == nil {
		s.telegramCommands = commands
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

	return command, nil
}

func (s *Service) DeleteTelegramCommand(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for index := range s.telegramCommands {
		if s.telegramCommands[index].ID == id {
			s.telegramCommands = append(s.telegramCommands[:index], s.telegramCommands[index+1:]...)
			s.persistTelegramCommands()

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
