package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type TelegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *TelegramMessage       `json:"message"`
	ChannelPost   *TelegramMessage       `json:"channel_post"`
	CallbackQuery *TelegramCallbackQuery `json:"callback_query"`
}

type TelegramChat struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      TelegramChat `json:"chat"`
	From      *struct {
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	} `json:"from"`
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
	token := strings.TrimSpace(settings.TelegramToken)
	if token == "" {
		return nil, offset, fmt.Errorf("telegram token не задан")
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=1&limit=20", token)
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
	s.deleteTelegramWebhook(ctx)
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, next, err := s.TelegramUpdates(ctx, offset)
		if err != nil {
			log.Printf("[Telegram] getUpdates failed: %v", err)
		}
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
		if update.CallbackQuery.Message != nil {
			s.recordTelegramChat(update.CallbackQuery.Message.Chat)
		}
		s.handleTelegramCallback(ctx, update.CallbackQuery, music)
		return
	}
	message := update.Message
	if message == nil {
		message = update.ChannelPost
	}
	if message == nil {
		return
	}
	s.recordTelegramChat(message.Chat)
	chatID := strconv.FormatInt(message.Chat.ID, 10)
	if !s.isTelegramChat(message.Chat) {
		return
	}

	text := strings.TrimSpace(message.Text)
	userName := "канал"
	if message.From != nil {
		if message.From.Username != "" {
			userName = "@" + message.From.Username
		} else if message.From.FirstName != "" {
			userName = message.From.FirstName
		}
	}
	if command, custom, ok := s.matchTelegramCommand(text, userName); ok {
		if err := s.SendTelegramCommandTo(ctx, chatID, custom, command.Buttons, command.ParseMode, command.DisablePreview); err != nil {
			log.Printf("[Telegram] custom command %q failed: %v", commandName(text), err)
		}

		return
	}
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
			requested := 0
			if fields := strings.Fields(text); len(fields) > 1 {
				requested, _ = strconv.Atoi(fields[1])
				requested--
			}
			page, pages := music(requested)
			if requested < 0 {
				requested = 0
			}
			if requested >= pages {
				requested = pages - 1
			}
			_ = s.sendTelegramPageTo(ctx, chatID, page, pages, requested)

			return
		}
	case "/ping":
		reply = "pong"
	default:
		return
	}
	if reply != "" {
		if err := s.SendTelegramTextTo(ctx, chatID, reply); err != nil {
			log.Printf("[Telegram] command %q failed: %v", command, err)
		}
	}
}

func (s *Service) handleTelegramCallback(ctx context.Context, query *TelegramCallbackQuery, music func(page int) (string, int)) {
	if query.Message == nil {
		return
	}

	chatID := strconv.FormatInt(query.Message.Chat.ID, 10)
	if !s.isTelegramChat(query.Message.Chat) {
		return
	}

	_ = s.answerTelegramCallback(ctx, query.ID)

	if !strings.HasPrefix(query.Data, "music:") || query.Data == "music:noop" || music == nil {
		return
	}

	page := 0
	_, _ = fmt.Sscanf(strings.TrimPrefix(query.Data, "music:"), "%d", &page)
	text, pages := music(page)
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	_ = s.editTelegramPageTo(ctx, chatID, query.Message.MessageID, text, pages, page)
}

func (s *Service) telegramKeyboard(page, pages int) map[string]any {
	row := make([]map[string]string, 0, 5)

	if page > 0 {
		row = append(row, map[string]string{"text": "« 1", "callback_data": "music:0"})
		row = append(row, map[string]string{"text": "‹", "callback_data": fmt.Sprintf("music:%d", page-1)})
	}

	row = append(row, map[string]string{"text": fmt.Sprintf("%d/%d", page+1, pages), "callback_data": "music:noop"})

	if page+1 < pages {
		row = append(row, map[string]string{"text": "›", "callback_data": fmt.Sprintf("music:%d", page+1)})
		row = append(row, map[string]string{"text": fmt.Sprintf("%d »", pages), "callback_data": fmt.Sprintf("music:%d", pages-1)})
	}

	return map[string]any{"inline_keyboard": [][]map[string]string{row}}
}

func (s *Service) SendTelegramPage(ctx context.Context, text string, page, pages int) error {
	settings := s.GetSettings()
	return s.sendTelegramPageTo(ctx, settings.TelegramChatID, text, pages, page)
}

func (s *Service) sendTelegramPageTo(ctx context.Context, chatID, text string, pages, page int) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || chatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
		"reply_markup":             s.telegramKeyboard(page, pages),
	}

	return s.callTelegram(ctx, settings.TelegramToken, "sendMessage", payload)
}

func (s *Service) editTelegramPageTo(ctx context.Context, chatID string, messageID int64, text string, pages, page int) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || chatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
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

func commandName(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}

	return fields[0]
}

func (s *Service) SendTelegramTextTo(ctx context.Context, chatID, text string) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || chatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	return s.sendTelegram(ctx, settings.TelegramToken, chatID, text)
}

func (s *Service) SendTelegramCommandTo(ctx context.Context, chatID, text string, buttons []TelegramButton, parseMode string, disablePreview bool) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || chatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": disablePreview,
	}
	if parseMode == "HTML" || parseMode == "MarkdownV2" {
		payload["parse_mode"] = parseMode
	}
	if len(buttons) > 0 {
		rows := make([][]map[string]string, 0, len(buttons))
		for _, button := range buttons {
			if strings.TrimSpace(button.Text) == "" || !strings.HasPrefix(button.URL, "http") {
				continue
			}
			rows = append(rows, []map[string]string{{"text": button.Text, "url": button.URL}})
		}
		payload["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}

	return s.callTelegram(ctx, settings.TelegramToken, "sendMessage", payload)
}

func (s *Service) SendTelegramButtons(ctx context.Context, text string, buttons []TelegramButton) error {
	settings := s.GetSettings()
	return s.SendTelegramButtonsTo(ctx, settings.TelegramChatID, text, buttons)
}

func (s *Service) SendTelegramButtonsTo(ctx context.Context, chatID, text string, buttons []TelegramButton) error {
	settings := s.GetSettings()
	if settings.TelegramToken == "" || chatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	rows := make([][]map[string]string, 0, len(buttons))
	for _, button := range buttons {
		if strings.TrimSpace(button.Text) == "" || !strings.HasPrefix(button.URL, "http") {
			continue
		}
		rows = append(rows, []map[string]string{{"text": button.Text, "url": button.URL}})
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": rows},
	}

	return s.callTelegram(ctx, settings.TelegramToken, "sendMessage", payload)
}

func (s *Service) deleteTelegramWebhook(ctx context.Context) {
	settings := s.GetSettings()
	token := strings.TrimSpace(settings.TelegramToken)
	if token == "" {
		return
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook", token)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return
	}
	response, err := s.http.Do(request)
	if err != nil {
		log.Printf("[Telegram] deleteWebhook failed: %v", err)

		return
	}
	_ = response.Body.Close()
}

func (s *Service) recordTelegramChat(chat TelegramChat) {
	if chat.ID == 0 {
		return
	}

	s.mu.Lock()
	s.telegramChats[chat.ID] = chat
	s.mu.Unlock()
}

func (s *Service) TelegramChatsSeen() []TelegramChat {
	s.mu.Lock()
	defer s.mu.Unlock()

	chats := make([]TelegramChat, 0, len(s.telegramChats))
	for _, chat := range s.telegramChats {
		chats = append(chats, chat)
	}

	return chats
}
