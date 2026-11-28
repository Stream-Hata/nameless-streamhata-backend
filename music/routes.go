package music

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/media"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type listResponse struct {
	Tracks           []Track          `json:"tracks"`
	Stats            Stats            `json:"stats"`
	Enabled          bool             `json:"enabled"`
	StreamerPlaylist StreamerPlaylist `json:"streamerPlaylist"`
	Favorites        []Track          `json:"favorites"`
}

func RegisterRoutes(srv Deps, resolve func(http.ResponseWriter, *http.Request) *Service) {
	serve := func(path string, handler func(*Service, http.ResponseWriter, *http.Request)) {
		srv.ServeEndpoint(path, func(response http.ResponseWriter, request *http.Request) {
			if service := resolve(response, request); service != nil {
				handler(service, response, request)
			}
		})
	}
	serve("/rgg-sync/music/search-audio", func(_ *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Cache-Control", "no-store")
		sources, err := media.SearchAudio(request.Context(), request.URL.Query().Get("title"), request.URL.Query().Get("author"), request.URL.Query().Get("exclude"))
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"sources": sources})
	})

	serve("/rgg-sync/music", func(service *Service, response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			response.Header().Set("Cache-Control", "no-store")
			writeJSON(response, http.StatusOK, listResponse{
				Tracks: service.List(), Stats: service.Stats(), Enabled: service.Enabled(),
				StreamerPlaylist: service.StreamerPlaylist(), Favorites: service.Favorites(),
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

			if videoID, ok := ExtractVideoID(body.URL); ok {
				track, err := service.Add(request.Context(), videoID, body.URL, body.AddedBy)
				if err != nil {
					writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

					return
				}

				writeJSON(response, http.StatusOK, track)

				return
			}

			mediaURL, kind, ok := ExtractLink(body.URL)
			if !ok {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "нужна ссылка YouTube или прямой аудио/видео URL"})

				return
			}
			track, err := service.AddMedia(mediaURL, kind, body.AddedBy)
			if err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, track)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	serve("/rgg-sync/music/streamer-playlist", func(service *Service, response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, service.StreamerPlaylist())
		case http.MethodPost:
			var body struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096)).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			playlist, err := service.ImportStreamerPlaylist(request.Context(), body.URL)
			if err != nil {
				writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(response, http.StatusOK, playlist)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	serve("/rgg-sync/music/streamer-playlist/track", func(service *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096)).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		playlist, err := service.AddStreamerTrack(request.Context(), body.URL)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, playlist)
	})

	serve("/rgg-sync/music/streamer-playlist/index", func(service *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Index int `json:"index"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1024)).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		index := service.SetStreamerPlaylistIndex(body.Index)
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "index": index})
	})

	serve("/rgg-sync/music/streamer-playlist/", func(service *Service, response http.ResponseWriter, request *http.Request) {
		videoID := strings.TrimPrefix(request.URL.Path, "/rgg-sync/music/streamer-playlist/")
		if request.Method != http.MethodDelete || videoID == "" {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !service.RemoveStreamerTrack(videoID) {
			http.Error(response, "track not found", http.StatusNotFound)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})

	serve("/rgg-sync/music/favorites", func(service *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"tracks": service.Favorites()})
			return
		}
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			VideoID string `json:"videoId"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 2048)).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if err := service.SetFavoriteByID(strings.TrimSpace(body.VideoID), true); err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})

	serve("/rgg-sync/music/favorites/", func(service *Service, response http.ResponseWriter, request *http.Request) {
		videoID := strings.TrimPrefix(request.URL.Path, "/rgg-sync/music/favorites/")
		if request.Method != http.MethodDelete || videoID == "" {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := service.SetFavoriteByID(videoID, false); err != nil {
			http.Error(response, "track not found", http.StatusNotFound)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})

	serve("/rgg-sync/music/", func(service *Service, response http.ResponseWriter, request *http.Request) {
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

	serve("/rgg-sync/music/current", func(service *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"track": service.Current()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			VideoID string `json:"videoId"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		service.SetCurrent(strings.TrimSpace(body.VideoID))
		writeJSON(response, http.StatusOK, map[string]any{"ok": true})
	})

	serve("/rgg-sync/music/thumbnail", func(service *Service, response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		videoID := strings.TrimSpace(request.URL.Query().Get("id"))

		if !validVideoID(videoID) {
			http.Error(response, "invalid video id", http.StatusBadRequest)

			return
		}

		target := "https://i.ytimg.com/vi/" + videoID + "/hqdefault.jpg"
		upstream, err := http.NewRequestWithContext(request.Context(), http.MethodGet, target, nil)

		if err != nil {
			http.Error(response, "invalid thumbnail url", http.StatusBadRequest)

			return
		}

		upstream.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

		result, err := netcfg.NewHTTPClient(12 * time.Second).Do(upstream)

		if err != nil {
			http.Error(response, "thumbnail fetch failed", http.StatusBadGateway)

			return
		}

		defer func() {
			_ = result.Body.Close()
		}()

		if result.StatusCode != http.StatusOK {
			response.WriteHeader(http.StatusNotFound)

			return
		}

		contentType := result.Header.Get("Content-Type")

		if contentType == "" {
			contentType = "image/jpeg"
		}

		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		_, _ = io.Copy(response, io.LimitReader(result.Body, 10<<20))
	})

	serve("/rgg-sync/music/telegram", func(service *Service, response http.ResponseWriter, request *http.Request) {
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

	serve("/rgg-sync/music/export", func(service *Service, response http.ResponseWriter, _ *http.Request) {
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

func validVideoID(value string) bool {
	if len(value) != 11 {
		return false
	}

	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}

	return true
}
