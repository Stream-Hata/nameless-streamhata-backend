package chat

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/p7x-dev/nameless-streamhata-backend/bots"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/chat/recent", func(response http.ResponseWriter, request *http.Request) {
		limit := 100

		if raw := request.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = min(parsed, 200)
			}
		}

		messages := service.Recent(limit)
		filtered := messages[:0]

		for _, message := range messages {
			if !bots.Is(message.Author.Login) {
				filtered = append(filtered, message)
			}
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"messages": filtered,
		})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}
