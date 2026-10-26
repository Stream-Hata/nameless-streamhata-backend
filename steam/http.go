package steam

import (
	"encoding/json"
	"net/http"
)

type Router interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Router, service *Service) {
	srv.ServeEndpoint("/rgg-sync/steam/player", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		steamID := request.URL.Query().Get("steamId")

		player, err := service.Player(request.Context(), steamID)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, player)
	})

	srv.ServeEndpoint("/rgg-sync/steam/games", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		steamID := request.URL.Query().Get("steamId")

		games, err := service.OwnedGames(request.Context(), steamID)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"items": games, "count": len(games)})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
