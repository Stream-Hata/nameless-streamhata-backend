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
	UpdateID      int64                  `json:"update_id"`
	Message       *TelegramMessage       `json:"message"`
	CallbackQuery *TelegramCallbackQuery `json:"callback_query"`
}

type TelegramMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type TelegramCallbackQuery struct {
	ID      string           `json:"id"`
	Data    string           `json:"data"`
	Message *TelegramMessage `json:"message"`
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

func (s *Service) TelegramBotLoop(ctx context.Context, status func() string, music func(page int) (string, int)) {
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

func (s *Service) handleTelegramUpdate(ctx context.Context, update TelegramUpdate, status func() string, music func(page int) (string, int)) {
	if update.CallbackQuery != nil {
		s.handleTelegramCallback(ctx, update.CallbackQuery, music)
		return
	}
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
			page, pages := music(0)
			_ = s.sendTelegramPage(ctx, page, pages, 0)

			return
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

func (s *Service) handleTelegramCallback(ctx context.Context, query *TelegramCallbackQuery, music func(page int) (string, int)) {
	if query.Message == nil {
		return
	}

	settings := s.GetSettings()
	chatID := strconv.FormatInt(query.Message.Chat.ID, 10)
	if settings.TelegramChatID == "" || chatID != settings.TelegramChatID {
		return
	}

	_ = s.answerTelegramCallback(ctx, query.ID)

	if !strings.HasPrefix(query.Data, "music:") || query.Data == "music:noop" || music == nil {
		return
	}

	page := 0
	_, _ = fmt.Sscanf(strings.TrimPrefix(query.Data, "music:"), "%d", &page)
	text, pages := music(page)
	_ = s.editTelegramPage(ctx, query.Message.MessageID, text, pages, page)
}

func (s *Service) telegramKeyboard(page, pages int) map[string]any {
	row := make([]map[string]string, 0, 3)

	if page > 0 {
		row = append(row, map[string]string{"text": "‹ Назад", "callback_data": fmt.Sprintf("music:%d", page-1)})
	}

	row = append(row, map[string]string{"text": fmt.Sprintf("%d/%d", page+1, pages), "callback_data": "music:noop"})

	if page+1 < pages {
		row = append(row, map[string]string{"text": "Вперёд ›", "callback_data": fmt.Sprintf("music:%d", page+1)})
	}

	return map[string]any{"inline_keyboard": [][]map[string]string{row}}
}

func (s *Service) sendTelegramPage(ctx context.Context, text string, pages, page int) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || settings.TelegramChatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	payload := map[string]any{
		"chat_id":                  settings.TelegramChatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             s.telegramKeyboard(page, pages),
	}

	return s.callTelegram(ctx, settings.TelegramToken, "sendMessage", payload)
}

func (s *Service) editTelegramPage(ctx context.Context, messageID int64, text string, pages, page int) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || settings.TelegramChatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	payload := map[string]any{
		"chat_id":                  settings.TelegramChatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             s.telegramKeyboard(page, pages),
	}

	return s.callTelegram(ctx, settings.TelegramToken, "editMessageText", payload)
}

func (s *Service) answerTelegramCallback(ctx context.Context, callbackID string) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" {
		return fmt.Errorf("telegram token не задан")
	}

	return s.callTelegram(ctx, settings.TelegramToken, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID,
	})
}

func (s *Service) callTelegram(ctx context.Context, token, method string, payload any) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s", token, method)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := s.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 300 {
		var errorPayload struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(response.Body).Decode(&errorPayload)
		if errorPayload.Description != "" {
			return fmt.Errorf("telegram: %s", errorPayload.Description)
		}

		return fmt.Errorf("telegram returned %d", response.StatusCode)
	}

	return nil
}
