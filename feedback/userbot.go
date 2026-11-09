package feedback

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) UserBotLoop(ctx context.Context) {
	token, _ := s.userTelegram()
	if token == "" {
		return
	}
	s.setUserBotCommands(ctx, token)

	var offset int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, next := s.botUpdatesFor(ctx, token, offset)
		offset = next
		for _, update := range updates {
			s.userBotHandle(ctx, token, update)
		}
		time.Sleep(2 * time.Second)
	}
}

func (s *Service) userBotHandle(ctx context.Context, token string, update botUpdate) {
	if update.Message == nil {
		return
	}
	chatID := strconv.FormatInt(update.Message.Chat.ID, 10)
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "/start":
		if len(fields) > 1 {
			s.botLinkTelegram(ctx, token, chatID, update.Message.From.Username, fields[1])

			return
		}
		s.botSend(ctx, token, chatID, "StreamHata Feedback\n/my — мои обращения\n/status FE-001 — статус обращения\n/stop — отключить уведомления")
	case "/my":
		s.userBotList(ctx, token, chatID)
	case "/status":
		if len(fields) > 1 {
			s.userBotStatus(ctx, token, chatID, fields[1])
		} else {
			s.botSend(ctx, token, chatID, "Укажи номер: /status FE-001")
		}
	case "/stop":
		s.userBotUnlink(ctx, token, chatID)
	case "/help":
		s.botSend(ctx, token, chatID, "Команды: /my, /status FE-001, /stop")
	}
}

func (s *Service) userBotList(ctx context.Context, token, chatID string) {
	author := s.authorByChat(ctx, chatID)
	if author == "" {
		s.botSend(ctx, token, chatID, "Сначала привяжи Telegram через кнопку «Уведомления в Telegram» на странице обратной связи.")
		return
	}
	if s.db == nil {
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT prefix, number, title, status, issue_url, created_at
		FROM feedback_reports WHERE author = $1 ORDER BY created_at DESC LIMIT 15`, author)
	if err != nil {
		s.botSend(ctx, token, chatID, "Не удалось загрузить обращения.")
		return
	}
	defer rows.Close()
	var text strings.Builder
	fmt.Fprintf(&text, "Обращения %s:\n", author)
	count := 0
	for rows.Next() {
		var prefix, title, status, url string
		var number int
		var created time.Time
		if rows.Scan(&prefix, &number, &title, &status, &url, &created) != nil {
			continue
		}
		count++
		fmt.Fprintf(&text, "\n%s-%03d · %s\n%s · %s", prefix, number, title, status, created.Format("02.01 15:04"))
		if url != "" {
			text.WriteString("\n" + url)
		}
		text.WriteString("\n")
	}
	if count == 0 {
		text.WriteString("\nПока нет обращений.")
	}
	s.botSend(ctx, token, chatID, text.String())
}

func (s *Service) userBotStatus(ctx context.Context, token, chatID, key string) {
	author := s.authorByChat(ctx, chatID)
	if author == "" {
		s.botSend(ctx, token, chatID, "Сначала привяжи Telegram через форму обратной связи.")
		return
	}
	parts := strings.Split(strings.ToUpper(key), "-")
	if len(parts) != 2 {
		s.botSend(ctx, token, chatID, "Формат: /status FE-001")
		return
	}
	number, _ := strconv.Atoi(parts[1])
	var title, status, url string
	var created time.Time
	err := s.db.QueryRowContext(ctx, `SELECT title, status, issue_url, created_at FROM feedback_reports
		WHERE prefix=$1 AND number=$2 AND author=$3`, parts[0], number, author).Scan(&title, &status, &url, &created)
	if err != nil {
		s.botSend(ctx, token, chatID, "Обращение не найдено.")
		return
	}
	text := fmt.Sprintf("%s-%03d\n%s\nСтатус: %s\nСоздано: %s", parts[0], number, title, status, created.Format("02.01.2006 15:04"))
	if url != "" {
		text += "\n" + url
	}
	s.botSend(ctx, token, chatID, text)
}

func (s *Service) userBotUnlink(ctx context.Context, token, chatID string) {
	if s.db != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE feedback_notify_links SET telegram_chat_id=NULL,
			updated_at=now() WHERE telegram_chat_id=$1`, chatID)
	}
	s.botSend(ctx, token, chatID, "Уведомления отключены.")
}

func (s *Service) authorByChat(ctx context.Context, chatID string) string {
	if s.db == nil {
		return ""
	}
	var author string
	if err := s.db.QueryRowContext(ctx, `SELECT author FROM feedback_notify_links
		WHERE telegram_chat_id=$1`, chatID).Scan(&author); err != nil {
		return ""
	}
	return author
}
