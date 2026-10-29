package feedback

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
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
	From struct {
		Username string `json:"username"`
	} `json:"from"`
	Text string `json:"text"`
}

type botPendingEdit struct {
	area   string
	number int
	kind   string
}

var botPending sync.Map

type botCallback struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		Username string `json:"username"`
	} `json:"from"`
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
	token := strings.TrimSpace(s.config.TelegramToken)
	configured := strings.TrimSpace(s.config.TelegramChat)
	owner := strings.TrimSpace(s.config.TelegramOwner)
	s.mu.Unlock()
	if token == "" {
		return
	}
	if owner == "" {
		owner = "p7x_dev"
	}

	var username string
	if update.CallbackQuery != nil {
		username = update.CallbackQuery.From.Username
	} else if update.Message != nil {
		username = update.Message.From.Username
	}
	if !strings.EqualFold(username, owner) {
		return
	}

	var chatID string
	if update.CallbackQuery != nil && update.CallbackQuery.Message != nil {
		chatID = strconv.FormatInt(update.CallbackQuery.Message.Chat.ID, 10)
	} else if update.Message != nil {
		chatID = strconv.FormatInt(update.Message.Chat.ID, 10)
	}
	if chatID == "" {
		return
	}

	if configured == "" || strings.Contains(configured, "X") {
		s.mu.Lock()
		s.config.TelegramChat = chatID
		s.mu.Unlock()
		_ = s.save()
		configured = chatID
	}
	if chatID != configured {
		return
	}

	if update.CallbackQuery != nil {
		s.botCallback(ctx, update.CallbackQuery, token, configured)
		return
	}
	if update.Message == nil {
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return
	}
	if !strings.HasPrefix(text, "/") {
		if pending, ok := botPending.Load(configured); ok {
			edit := pending.(botPendingEdit)
			botPending.Delete(configured)
			s.botApplyEdit(ctx, token, configured, edit, text)
			return
		}
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "/issues":
		page := 0
		if len(fields) > 1 {
			page, _ = strconv.Atoi(fields[1])
			page--
		}
		s.botIssues(ctx, token, configured, page)
	case "/issue":
		if len(fields) > 1 {
			s.botIssue(ctx, token, configured, strings.ToUpper(fields[1]))
			return
		}
		s.botIssues(ctx, token, configured, 0)
	case "/feedbacks":
		s.botFeedbacks(ctx, token, configured)
	case "/close", "/open":
		if len(fields) > 1 {
			state := "closed"
			if strings.ToLower(fields[0]) == "/open" {
				state = "open"
			}
			s.botSetState(ctx, token, configured, strings.ToUpper(fields[1]), state)
		}
	case "/help", "/start":
		s.botSend(ctx, token, configured, "Feedback bot\n/issues — открытые issue\n/feedbacks — только фидбеки\n/issue BE-001 — детали\n/close BE-001, /open BE-001 — сменить статус")
	}
}

func (s *Service) botIssues(ctx context.Context, token, chat string, page int) {
	s.mu.Lock()
	repos := map[string]string{}
	for key, value := range s.config.Repos {
		repos[key] = value
	}
	s.mu.Unlock()

	type entry struct {
		prefix string
		issue  githubIssue
	}
	entries := make([]entry, 0)
	for area, repo := range repos {
		issues, err := s.githubList(ctx, repo)
		if err != nil {
			continue
		}
		prefix := map[string]string{"backend": "BE", "frontend": "FE", "desktop": "DT"}[area]
		for _, issue := range issues {
			entries = append(entries, entry{prefix: prefix, issue: issue})
		}
	}

	const perPage = 5
	pages := (len(entries) + perPage - 1) / perPage
	if pages == 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("Открытые issue · стр %d/%d\n", page+1, pages))
	rows := make([][]map[string]string, 0, perPage+1)
	start := page * perPage
	end := start + perPage
	if end > len(entries) {
		end = len(entries)
	}
	for _, item := range entries[start:end] {
		fmt.Fprintf(&text, "• %s #%d %s\n  %s\n", item.prefix, item.issue.Number, item.issue.Title, item.issue.HTMLURL)
		label := fmt.Sprintf("%s #%d %s", item.prefix, item.issue.Number, item.issue.Title)
		if len([]rune(label)) > 40 {
			label = string([]rune(label)[:39]) + "…"
		}
		rows = append(rows, []map[string]string{{
			"text": label, "callback_data": fmt.Sprintf("fb:view:%s:%d", item.prefix, item.issue.Number),
		}})
	}

	if pages > 1 {
		row := make([]map[string]string, 0, 3)
		if page > 0 {
			row = append(row, map[string]string{"text": "‹", "callback_data": fmt.Sprintf("fb:page:%d", page)})
		}
		row = append(row, map[string]string{"text": fmt.Sprintf("%d/%d", page+1, pages), "callback_data": "fb:noop:0"})
		if page+1 < pages {
			row = append(row, map[string]string{"text": "›", "callback_data": fmt.Sprintf("fb:page:%d", page+2)})
		}
		rows = append(rows, row)
	}
	s.botSendKeyboard(ctx, token, chat, text.String(), map[string]any{"inline_keyboard": rows})
}

func (s *Service) botFeedbacks(ctx context.Context, token, chat string) {
	if s.db == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT prefix, number, title, issue_url, created_at
		FROM feedback_reports WHERE kind = 'feedback' ORDER BY created_at DESC LIMIT 15`)
	if err != nil {
		s.botSend(ctx, token, chat, "Не удалось прочитать фидбеки")
		return
	}
	defer rows.Close()
	var text strings.Builder
	text.WriteString("Фидбеки:\n")
	for rows.Next() {
		var prefix, title, url string
		var number int
		var created time.Time
		if rows.Scan(&prefix, &number, &title, &url, &created) != nil {
			continue
		}
		fmt.Fprintf(&text, "• %s-%03d %s\n  %s\n", prefix, number, title, created.Format("02.01 15:04"))
		if url != "" {
			text.WriteString("  " + url + "\n")
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
	if len(parts) < 3 || parts[0] != "fb" {
		return
	}
	switch parts[1] {
	case "page":
		page, _ := strconv.Atoi(parts[2])
		s.botIssues(ctx, token, chat, page-1)
	case "view":
		if len(parts) == 4 {
			number, _ := strconv.Atoi(parts[3])
			s.botIssueDetail(ctx, token, chat, parts[2], number)
		}
	case "comments":
		if len(parts) == 4 {
			number, _ := strconv.Atoi(parts[3])
			s.botComments(ctx, token, chat, parts[2], number)
		}
	case "edit":
		if len(parts) == 5 {
			number, _ := strconv.Atoi(parts[4])
			s.botBeginEdit(ctx, token, chat, parts[2], number, parts[3])
		}
	case "comment":
		if len(parts) == 4 {
			number, _ := strconv.Atoi(parts[3])
			s.botBeginComment(ctx, token, chat, parts[2], number)
		}
	case "close", "open":
		state := "closed"
		if parts[1] == "open" {
			state = "open"
		}
		s.botSetState(ctx, token, chat, parts[2], state)
	}
	s.botAnswer(ctx, token, query.ID, "Готово")
}

func (s *Service) botIssueDetail(ctx context.Context, token, chat, prefix string, number int) {
	area := map[string]string{"BE": "backend", "FE": "frontend", "DT": "desktop"}[strings.ToUpper(prefix)]
	s.mu.Lock()
	repo := s.config.Repos[area]
	s.mu.Unlock()
	if repo == "" {
		s.botSend(ctx, token, chat, "Репозиторий не найден")
		return
	}
	issue, err := s.githubIssue(ctx, repo, number)
	if err != nil {
		s.botSend(ctx, token, chat, "Не удалось загрузить issue: "+err.Error())
		return
	}
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.Name)
	}
	assignees := make([]string, 0, len(issue.Assignees))
	for _, assignee := range issue.Assignees {
		assignees = append(assignees, assignee.Login)
	}
	body := issue.Body
	if len([]rune(body)) > 1800 {
		body = string([]rune(body)[:1800]) + "…"
	}
	text := fmt.Sprintf("%s #%d\n%s\n\nСостояние: %s\nМетки: %s\nИсполнители: %s\n\n%s\n\n%s",
		prefix, issue.Number, issue.Title, issue.State, strings.Join(labels, ", "), strings.Join(assignees, ", "), body, issue.HTMLURL)
	key := fmt.Sprintf("%s-%d", strings.ToUpper(prefix), number)
	keyboard := map[string]any{"inline_keyboard": [][]map[string]string{
		{
			{"text": "Закрыть", "callback_data": "fb:close:" + key},
			{"text": "Открыть", "callback_data": "fb:open:" + key},
		},
		{
			{"text": "Комментарии", "callback_data": "fb:comments:" + key},
			{"text": "Заголовок", "callback_data": "fb:edit:title:" + key},
			{"text": "Описание", "callback_data": "fb:edit:body:" + key},
		},
		{
			{"text": "Добавить комментарий", "callback_data": "fb:comment:" + key},
		},
	}}
	s.botSendKeyboard(ctx, token, chat, text, keyboard)
}

func (s *Service) botSetState(ctx context.Context, token, chat, key, state string) {
	fields := strings.Split(key, "-")
	if len(fields) != 2 {
		return
	}
	number, _ := strconv.Atoi(fields[1])
	area := map[string]string{"BE": "backend", "FE": "frontend", "DT": "desktop"}[strings.ToUpper(fields[0])]
	s.mu.Lock()
	repo := s.config.Repos[area]
	s.mu.Unlock()
	if repo == "" {
		s.botSend(ctx, token, chat, "Репозиторий для "+key+" не найден")
		return
	}
	if err := s.githubState(ctx, repo, number, state); err != nil {
		s.botSend(ctx, token, chat, "Не удалось изменить issue: "+err.Error())
		return
	}
	s.botSend(ctx, token, chat, fmt.Sprintf("%s: %s", key, state))
}

func (s *Service) botBeginEdit(ctx context.Context, token, chat, prefix string, number int, kind string) {
	botPending.Store(chat, botPendingEdit{area: strings.ToUpper(prefix), number: number, kind: kind})
	label := "заголовок"
	if kind == "body" {
		label = "описание"
	}
	s.botSend(ctx, token, chat, "Отправь новый "+label+" для "+strings.ToUpper(prefix)+"-"+strconv.Itoa(number))
}

func (s *Service) botBeginComment(ctx context.Context, token, chat, prefix string, number int) {
	botPending.Store(chat, botPendingEdit{area: strings.ToUpper(prefix), number: number, kind: "comment"})
	s.botSend(ctx, token, chat, "Отправь текст комментария для "+strings.ToUpper(prefix)+"-"+strconv.Itoa(number))
}

func (s *Service) botApplyEdit(ctx context.Context, token, chat string, edit botPendingEdit, text string) {
	area := map[string]string{"BE": "backend", "FE": "frontend", "DT": "desktop"}[edit.area]
	s.mu.Lock()
	repo := s.config.Repos[area]
	s.mu.Unlock()
	if repo == "" {
		s.botSend(ctx, token, chat, "Репозиторий не найден")
		return
	}
	if edit.kind == "comment" {
		if err := s.githubAddComment(ctx, repo, edit.number, text); err != nil {
			s.botSend(ctx, token, chat, "Не удалось добавить комментарий: "+err.Error())
			return
		}
		s.botSend(ctx, token, chat, "Комментарий добавлен")
		return
	}
	if err := s.githubUpdateIssue(ctx, repo, edit.number, edit.kind, text); err != nil {
		s.botSend(ctx, token, chat, "Не удалось обновить issue: "+err.Error())
		return
	}
	s.botSend(ctx, token, chat, "Issue обновлён")
}

func (s *Service) botComments(ctx context.Context, token, chat, prefix string, number int) {
	area := map[string]string{"BE": "backend", "FE": "frontend", "DT": "desktop"}[strings.ToUpper(prefix)]
	s.mu.Lock()
	repo := s.config.Repos[area]
	s.mu.Unlock()
	comments, err := s.githubComments(ctx, repo, number)
	if err != nil {
		s.botSend(ctx, token, chat, "Не удалось загрузить комментарии: "+err.Error())
		return
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Комментарии %s-%d:\n", strings.ToUpper(prefix), number)
	for _, comment := range comments {
		body := comment.Body
		if len([]rune(body)) > 500 {
			body = string([]rune(body)[:500]) + "…"
		}
		fmt.Fprintf(&text, "\n@%s:\n%s\n", comment.User.Login, body)
	}
	s.botSend(ctx, token, chat, text.String())
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
