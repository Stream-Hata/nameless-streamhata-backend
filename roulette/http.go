package roulette

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/roulette/config", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, s.GetConfig())

			return
		}

		if request.Method != http.MethodPut && request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var config Config

		if err := json.NewDecoder(request.Body).Decode(&config); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		writeJSON(response, http.StatusOK, s.SetConfig(config))
	})

	srv.ServeEndpoint("/rgg-sync/roulette/state", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, s.State())
	})

	srv.ServeEndpoint("/rgg-sync/roulette/history", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))

		writeJSON(response, http.StatusOK, map[string]any{"items": s.History(limit)})
	})

	srv.ServeEndpoint("/rgg-sync/roulette/reel", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))

		writeJSON(response, http.StatusOK, map[string]any{"items": s.Reel(limit)})
	})

	srv.ServeEndpoint("/rgg-sync/roulette/spin", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Mode string `json:"mode"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		round, err := s.Spin(body.Mode)
		if errors.Is(err, ErrEmptyPool) {
			writeJSON(response, http.StatusConflict, map[string]string{"error": "empty pool"})

			return
		}

		if errors.Is(err, ErrMode) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "unknown mode"})

			return
		}

		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, round)
	})

	srv.ServeEndpoint("/rgg-sync/roulette/reset", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		s.Reset()
		writeJSON(response, http.StatusOK, s.State())
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
