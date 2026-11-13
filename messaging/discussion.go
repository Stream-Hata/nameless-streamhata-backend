package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type GameDiscussionMessage struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"userId"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	Message     string    `json:"message"`
	ParentID    *int64    `json:"parentId,omitempty"`
	Likes       int       `json:"likes"`
	Liked       bool      `json:"liked"`
	CreatedAt   time.Time `json:"createdAt"`
}

type GameDiscussionNotification struct {
	ID        int64     `json:"id"`
	MessageID *int64    `json:"messageId,omitempty"`
	GameSlug  string    `json:"gameSlug"`
	Kind      string    `json:"kind"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) gameDiscussion(response http.ResponseWriter, request *http.Request) {
	slug := strings.TrimSpace(request.PathValue("slug"))
	if slug == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "game slug is required"})

		return
	}

	switch request.Method {
	case http.MethodGet:
		s.listGameDiscussion(response, request, slug)
	case http.MethodPost:
		s.createGameDiscussion(response, request, slug)
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) listGameDiscussion(response http.ResponseWriter, request *http.Request, slug string) {
	userID, _, _, _ := s.user(request)
	rows, err := s.db.QueryContext(request.Context(), `
		SELECT d.id, d.user_id, u.username, COALESCE(NULLIF(u.display_name, ''), u.username), d.message, d.parent_id, d.created_at,
			(SELECT count(*) FROM game_discussion_likes l WHERE l.message_id = d.id),
			EXISTS(SELECT 1 FROM game_discussion_likes l WHERE l.message_id = d.id AND l.user_id = $2)
		FROM game_discussions d
		JOIN users u ON u.id = d.user_id
		WHERE d.game_slug = $1
		ORDER BY d.created_at ASC
		LIMIT 500`, slug, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load discussion"})

		return
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]GameDiscussionMessage, 0)

	for rows.Next() {
		var item GameDiscussionMessage
		if rows.Scan(&item.ID, &item.UserID, &item.Username, &item.DisplayName, &item.Message, &item.ParentID, &item.CreatedAt, &item.Likes, &item.Liked) == nil {
			items = append(items, item)
		}
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) createGameDiscussion(response http.ResponseWriter, request *http.Request, slug string) {
	userID, username, displayName, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	var body struct {
		Message  string `json:"message"`
		ParentID *int64 `json:"parentId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 32<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}

	message := strings.TrimSpace(body.Message)
	if message == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "message is required"})

		return
	}
	if len([]rune(message)) > 4000 {
		message = string([]rune(message)[:4000])
	}
	if decision := s.spam.CheckDiscussion(userID, message); !decision.Allowed {
		writeSpam(response, decision)

		return
	}

	if body.ParentID != nil {
		var parentAuthor int64
		if err := s.db.QueryRowContext(request.Context(), `
			SELECT user_id FROM game_discussions WHERE id = $1 AND game_slug = $2`, *body.ParentID, slug).Scan(&parentAuthor); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "parent message not found"})

			return
		}
		if parentAuthor != userID {
			s.notifyDiscussion(request.Context(), parentAuthor, *body.ParentID, slug, "reply")
		}
	}

	var item GameDiscussionMessage
	err := s.db.QueryRowContext(request.Context(), `
		INSERT INTO game_discussions (game_slug, user_id, message, parent_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, message, parent_id, created_at`, slug, userID, message, body.ParentID).
		Scan(&item.ID, &item.UserID, &item.Message, &item.ParentID, &item.CreatedAt)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to save message"})

		return
	}

	item.Username = username
	item.DisplayName = displayName
	writeJSON(response, http.StatusCreated, item)
}

func (s *Service) gameDiscussionMessage(response http.ResponseWriter, request *http.Request) {
	slug := strings.TrimSpace(request.PathValue("slug"))
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid id"})

		return
	}

	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	var authorID int64
	if err := s.db.QueryRowContext(request.Context(), `
		SELECT user_id FROM game_discussions WHERE id = $1 AND game_slug = $2`, id, slug).Scan(&authorID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "message not found"})

		return
	}

	if authorID != userID {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "not your message"})

		return
	}

	switch request.Method {
	case http.MethodDelete:
		_, err := s.db.ExecContext(request.Context(), `DELETE FROM game_discussions WHERE id = $1`, id)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to delete message"})

			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 32<<10)).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}
		message := strings.TrimSpace(body.Message)
		if message == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "message is required"})

			return
		}
		if len([]rune(message)) > 4000 {
			message = string([]rune(message)[:4000])
		}
		_, err := s.db.ExecContext(request.Context(), `UPDATE game_discussions SET message = $1 WHERE id = $2`, message, id)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update message"})

			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "message": message})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) gameDiscussionLike(response http.ResponseWriter, request *http.Request) {
	slug := strings.TrimSpace(request.PathValue("slug"))
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || request.Method != http.MethodPost {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid request"})

		return
	}

	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	var authorID int64
	if err := s.db.QueryRowContext(request.Context(), `
		SELECT user_id FROM game_discussions WHERE id = $1 AND game_slug = $2`, id, slug).Scan(&authorID); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "message not found"})

		return
	}

	result, err := s.db.ExecContext(request.Context(), `DELETE FROM game_discussion_likes WHERE message_id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update like"})

		return
	}
	removed, _ := result.RowsAffected()
	if removed == 0 {
		if _, err := s.db.ExecContext(request.Context(), `INSERT INTO game_discussion_likes (message_id, user_id) VALUES ($1, $2)`, id, userID); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update like"})

			return
		}
		if authorID != userID {
			s.notifyDiscussion(request.Context(), authorID, id, slug, "like")
		}
	}

	var likes int
	_ = s.db.QueryRowContext(request.Context(), `SELECT count(*) FROM game_discussion_likes WHERE message_id = $1`, id).Scan(&likes)
	writeJSON(response, http.StatusOK, map[string]any{"liked": removed == 0, "likes": likes})
}

func (s *Service) gameDiscussionNotifications(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	if request.Method == http.MethodPost {
		_, _ = s.db.ExecContext(request.Context(), `UPDATE game_discussion_notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})

		return
	}
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	rows, err := s.db.QueryContext(request.Context(), `
		SELECT id, message_id, game_slug, kind, read_at IS NOT NULL, created_at
		FROM game_discussion_notifications
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 100`, userID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load notifications"})

		return
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]GameDiscussionNotification, 0)
	unread := 0
	for rows.Next() {
		var item GameDiscussionNotification
		if rows.Scan(&item.ID, &item.MessageID, &item.GameSlug, &item.Kind, &item.Read, &item.CreatedAt) == nil {
			if !item.Read {
				unread++
			}
			items = append(items, item)
		}
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "unread": unread})
}

func (s *Service) notifyDiscussion(ctx context.Context, userID, messageID int64, slug, kind string) {
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO game_discussion_notifications (user_id, message_id, game_slug, kind)
		VALUES ($1, $2, $3, $4)`, userID, messageID, slug, kind)
}
