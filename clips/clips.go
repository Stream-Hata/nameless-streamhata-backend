// Package clips — создание клипов Twitch по кнопке панели.
package clips

import (
	"context"
	"encoding/json"
	"net/http"
)

// Creator создаёт клип (реализуется chatruntime).
type Creator interface {
	CreateClip(ctx context.Context) (string, error)
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, creator Creator) {
	srv.ServeEndpoint("/rgg-sync/clips", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		editURL, err := creator.CreateClip(request.Context())
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]string{"editUrl": editURL})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
