package messaging

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Service) groups(response http.ResponseWriter, request *http.Request) {
	userID, _, _, ok := s.user(request)
	if !ok {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		rows, err := s.db.QueryContext(request.Context(), `SELECT g.id,g.title,g.owner_id,g.created_at,
			(SELECT count(*) FROM streamer_group_members m WHERE m.group_id=g.id)
			FROM streamer_groups g JOIN streamer_group_members m ON m.group_id=g.id
			WHERE m.user_id=$1 ORDER BY g.created_at DESC`, userID)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load groups"})
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, ownerID, members int64
			var title string
			var created any
			if rows.Scan(&id, &title, &ownerID, &created, &members) == nil {
				items = append(items, map[string]any{"id": id, "title": title, "ownerId": ownerID, "createdAt": created, "members": members})
			}
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid title"})
			return
		}
		tx, err := s.db.BeginTx(request.Context(), nil)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create group"})
			return
		}
		defer tx.Rollback()
		var id int64
		if err := tx.QueryRowContext(request.Context(), `INSERT INTO streamer_groups (owner_id,title) VALUES ($1,$2) RETURNING id`, userID, strings.TrimSpace(body.Title)).Scan(&id); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create group"})
			return
		}
		if _, err := tx.ExecContext(request.Context(), `INSERT INTO streamer_group_members (group_id,user_id,role) VALUES ($1,$2,'owner')`, id, userID); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create group"})
			return
		}
		if err := tx.Commit(); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create group"})
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"id": id})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}
