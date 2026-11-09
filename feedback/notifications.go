package feedback

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (s *Service) telegramLinkHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	user, err := s.sessionUser(request)
	if err != nil {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "войдите через Twitch"})

		return
	}
	if s.db == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})

		return
	}
	userToken, userUsername := s.userTelegram()
	if userToken == "" || userUsername == "" {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "бот для уведомлений ещё не настроен"})

		return
	}
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "token generation failed"})

		return
	}
	token := hex.EncodeToString(buffer)
	_, err = s.db.ExecContext(request.Context(), `INSERT INTO feedback_notify_links
		(author, link_token, token_expires_at, updated_at)
		VALUES ($1, $2, now() + interval '15 minutes', now())
		ON CONFLICT (author) DO UPDATE SET
			link_token = EXCLUDED.link_token,
			token_expires_at = EXCLUDED.token_expires_at,
			updated_at = now()`, user.Username, token)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось создать ссылку"})

		return
	}
	writeJSON(response, http.StatusOK, map[string]string{
		"url": "https://t.me/" + userUsername + "?start=" + token,
	})
}

func (s *Service) botLinkTelegram(ctx context.Context, token, chatID, username, linkToken string) {
	if s.db == nil {
		return
	}
	var author string
	err := s.db.QueryRowContext(ctx, `UPDATE feedback_notify_links SET
		telegram_chat_id = $1, telegram_username = $2, link_token = '',
		token_expires_at = NULL, updated_at = now()
		WHERE link_token = $3 AND token_expires_at > now()
		RETURNING author`, chatID, username, linkToken).Scan(&author)
	if err != nil {
		s.botSend(ctx, token, chatID, "Ссылка устарела. Открой форму обратной связи и получи новую.")

		return
	}
	s.botSend(ctx, token, chatID, "Уведомления StreamHata подключены к аккаунту "+author+".")
}

func (s *Service) notifyAuthor(ctx context.Context, prefix string, number int, text string) {
	if s.db == nil {
		return
	}
	var author string
	if err := s.db.QueryRowContext(ctx, `SELECT author FROM feedback_reports
		WHERE prefix = $1 AND number = $2`, strings.ToUpper(prefix), number).Scan(&author); err != nil {
		return
	}
	var chatID sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT telegram_chat_id FROM feedback_notify_links
		WHERE author = $1 AND telegram_chat_id IS NOT NULL`, author).Scan(&chatID); err != nil || !chatID.Valid {
		return
	}
	_ = s.sendTelegramTo(ctx, fmt.Sprintf("%d", chatID.Int64), text)
}

func (s *Service) sendTelegramTo(ctx context.Context, chatID, text string) error {
	s.mu.Lock()
	token := strings.TrimSpace(s.config.TelegramToken)
	s.mu.Unlock()
	if token == "" || strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("Telegram feedback chat не настроен")
	}
	payload, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return fmt.Errorf("Telegram request failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("Telegram request failed: HTTP %d", response.StatusCode)
	}

	return nil
}
