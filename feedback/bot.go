package feedback

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type botUpdate struct {
	UpdateID      int64        `json:"update_id"`
	Message       *botMessage  `json:"message"`
	CallbackQuery *botCallback `json:"callback_query"`
}

type botMessage struct {
	MessageID int64 `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text string `json:"text"`
}

type botCallback struct {
	ID      string      `json:"id"`
	Data    string      `json:"data"`
	Message *botMessage `json:"message"`
}

func (s *Service) BotLoop(ctx context.Context) {
	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, next := s.botUpdates(ctx, offset)
		offset = next
		for _, update := range updates {
			s.botHandle(ctx, update)
		}
		time.Sleep(2 * time.Second)
	}
}

func (s *Service) botUpdates(ctx context.Context, offset int64) ([]botUpdate, int64) {
	s.mu.Lock()
	token := strings.TrimSpace(s.config.TelegramToken)
	chat := strings.TrimSpace(s.config.TelegramChat)
	s.mu.Unlock()
	if token == "" {
		return nil, offset
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=1&limit=20", token)
	if offset > 0 {
		endpoint += "&offset=" + strconv.FormatInt(offset, 10)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, offset
	}
	response, err := s.http.Do(request)
	if err != nil {
		return nil, offset
	}
	defer response.Body.Close()
	var payload struct {
		Result []botUpdate `json:"result"`
	}
	if json.NewDecoder(response.Body).Decode(&payload) != nil {
		return nil, offset
	}
	for _, update := range payload.Result {
		if update.UpdateID >= offset {
			offset = update.UpdateID + 1
		}
	}
	_ = chat
	return payload.Result, offset
}

func (s *Service) botHandle(ctx context.Context, update botUpdate) {
	s.mu.Lock()
	token := s.config.TelegramToken
	chat := s.config.TelegramChat
	s.mu.Unlock()
	if token == "" || chat == "" {
		return
	}
	if update.CallbackQuery != nil {
		s.botCallback(ctx, update.CallbackQuery, token, chat)
		return
	}
	if update.Message == nil || strconv.FormatInt(update.Message.Chat.ID, 10) != chat {
		return
	}
	fields := strings.Fields(strings.TrimSpace(update.Message.Text))
	if len(fields) == 0 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "/issues", "/issue":
		if len(fields) > 1 {
			s.botIssue(ctx, token, chat, strings.ToUpper(fields[1]))
			return
		}
		s.botIssues(ctx, token, chat)
	case "/help", "/start":
		s.botSend(ctx, token, chat, "Feedback bot\n/issues — открытые issue\n/issue BE-001 — детали")
	}
}

func (s *Service) botIssues(ctx context.Context, token, chat string) {
	s.mu.Lock()
	repos := map[string]string{}
	for key, value := range s.config.Repos {
		repos[key] = value
	}
	s.mu.Unlock()
	var text strings.Builder
	text.WriteString("Открытые issue:\n")
	for area, repo := range repos {
		issues, err := s.githubList(ctx, repo)
		if err != nil {
			continue
		}
		for _, issue := range issues {
			fmt.Fprintf(&text, "• %s #%d %s\n", strings.ToUpper(area[:2]), issue.Number, issue.Title)
		}
	}
	s.botSend(ctx, token, chat, text.String())
}

func (s *Service) botIssue(ctx context.Context, token, chat, key string) {
	if s.db == nil {
		return
	}
	var repo, title, url, status string
	var number int
	if err := s.db.QueryRowContext(ctx, `SELECT repo, issue_number, title, issue_url, status FROM feedback_reports
		WHERE prefix = $1 AND number = $2`, strings.Split(key, "-")[0], strings.TrimPrefix(strings.Split(key, "-")[1], "0")).Scan(&repo, &number, &title, &url, &status); err != nil {
		s.botSend(ctx, token, chat, "Issue не найден")
		return
	}
	text := fmt.Sprintf("%s\n%s\nСтатус: %s", title, url, status)
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{{
		{"text": "Закрыть", "callback_data": "fb:close:" + key},
		{"text": "Открыть", "callback_data": "fb:open:" + key},
	}}}
	s.botSendKeyboard(ctx, token, chat, text, keyboard)
}

func (s *Service) botCallback(ctx context.Context, query *botCallback, token, chat string) {
	if query.Message == nil || strconv.FormatInt(query.Message.Chat.ID, 10) != chat {
		return
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 || parts[0] != "fb" {
		return
	}
	key := parts[2]
	fields := strings.Split(key, "-")
	if len(fields) != 2 {
		return
	}
	number, _ := strconv.Atoi(fields[1])
	area := map[string]string{"BE": "backend", "FE": "frontend", "DE": "desktop"}[strings.ToUpper(fields[0])]
	state := "closed"
	if parts[1] == "open" {
		state = "open"
	}
	s.mu.Lock()
	repo := s.config.Repos[area]
	s.mu.Unlock()
	if repo != "" {
		_ = s.githubState(ctx, repo, number, state)
	}
	s.botAnswer(ctx, token, query.ID, "Готово")
	s.botSend(ctx, token, chat, fmt.Sprintf("%s: %s", key, state))
}

func (s *Service) botSend(ctx context.Context, token, chat, text string) {
	s.botSendKeyboard(ctx, token, chat, text, nil)
}

func (s *Service) botSendKeyboard(ctx context.Context, token, chat, text string, keyboard any) {
	payload := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
	body, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", strings.NewReader(string(body)))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err == nil {
		response.Body.Close()
	}
}

func (s *Service) botAnswer(ctx context.Context, token, id, text string) {
	body, _ := json.Marshal(map[string]string{"callback_query_id": id, "text": text})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/answerCallbackQuery", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err == nil {
		response.Body.Close()
	}
}
