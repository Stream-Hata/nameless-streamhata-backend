package music

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type listResponse struct {
	Tracks  []Track `json:"tracks"`
	Stats   Stats   `json:"stats"`
	Enabled bool    `json:"enabled"`
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/music", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, listResponse{
				Tracks:  service.List(),
				Stats:   service.Stats(),
				Enabled: service.Enabled(),
			})
		case http.MethodPost:
			var body struct {
				URL     string `json:"url"`
				AddedBy string `json:"addedBy"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			videoID, ok := ExtractVideoID(body.URL)

			if !ok {
				videoID = strings.TrimSpace(body.URL)
			}

			track, err := service.Add(request.Context(), videoID, body.URL, body.AddedBy)
			if err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, track)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/music/", func(response http.ResponseWriter, request *http.Request) {
		videoID := strings.TrimPrefix(request.URL.Path, "/rgg-sync/music/")

		if request.Method != http.MethodDelete || videoID == "" {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(videoID) {
			http.Error(response, "track not found", http.StatusNotFound)

			return
		}

		response.WriteHeader(http.StatusNoContent)
	})

	srv.ServeEndpoint("/rgg-sync/music/telegram", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		chunks, err := service.ExportToTelegram(request.Context())
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"ok":     true,
			"chunks": chunks,
			"tracks": len(service.List()),
		})
	})

	srv.ServeEndpoint("/rgg-sync/music/export", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/csv; charset=utf-8")
		response.Header().Set("Content-Disposition", `attachment; filename="jurykit-tracks.csv"`)

		if err := service.WriteCSV(response); err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
		}
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
