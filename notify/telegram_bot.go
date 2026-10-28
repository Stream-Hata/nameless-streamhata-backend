package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type TelegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// TelegramUpdates long-polls getUpdates for the configured bot.
func (s *Service) TelegramUpdates(ctx context.Context, offset int64) ([]TelegramUpdate, int64, error) {
	settings := s.GetSettings()
	if settings.TelegramToken == "" {
		return nil, offset, fmt.Errorf("telegram token не задан")
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=1&limit=20", settings.TelegramToken)
	if offset > 0 {
		endpoint += "&offset=" + strconv.FormatInt(offset, 10)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, offset, err
	}

	response, err := s.http.Do(request)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 300 {
		return nil, offset, fmt.Errorf("telegram getUpdates returned %d", response.StatusCode)
	}

	var payload struct {
		Result []TelegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, offset, err
	}

	for _, update := range payload.Result {
		if update.UpdateID >= offset {
			offset = update.UpdateID + 1
		}
	}

	return payload.Result, offset, nil
}

func (s *Service) TelegramBotLoop(ctx context.Context, status func() string, music func() string) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, next, err := s.TelegramUpdates(ctx, offset)
		if err == nil {
			offset = next
			for _, update := range updates {
				s.handleTelegramUpdate(ctx, update, status, music)
			}
		}
		time.Sleep(2 * time.Second)
	}
}

func (s *Service) handleTelegramUpdate(ctx context.Context, update TelegramUpdate, status func() string, music func() string) {
	if update.Message == nil {
		return
	}
	settings := s.GetSettings()
	if settings.TelegramChatID == "" || strconv.FormatInt(update.Message.Chat.ID, 10) != settings.TelegramChatID {
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	command := strings.ToLower(strings.Split(text, " ")[0])
	var reply string
	switch command {
	case "/start", "/help":
		reply = "JuryKit bot\n/status — статус стрима\n/music — текущий трек\n/ping — проверка"
	case "/status":
		if status != nil {
			reply = status()
		}
	case "/music":
		if music != nil {
			reply = music()
		}
	case "/ping":
		reply = "pong"
	default:
		return
	}
	if reply != "" {
		_ = s.SendTelegramText(ctx, reply)
	}
}
