package viewer

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, resolve func(http.ResponseWriter, *http.Request) *Runtime) {
	serve := func(path string, handler func(*Runtime, http.ResponseWriter, *http.Request)) {
		srv.ServeEndpoint(path, func(response http.ResponseWriter, request *http.Request) {
			if service := resolve(response, request); service != nil {
				handler(service, response, request)
			}
		})
	}
	serve("/rgg-sync/viewers/latest-streak", func(
		runtime *Runtime, response http.ResponseWriter,
		request *http.Request,
	) {
		streak := runtime.GetLatestStreak()

		if streak == nil {
			writeJSON(response, http.StatusOK, nil)

			return
		}

		writeJSON(response, http.StatusOK, streak)
	})

	serve("/rgg-sync/viewers/", func(
		runtime *Runtime, response http.ResponseWriter,
		request *http.Request,
	) {
		userID := strings.TrimPrefix(request.URL.Path, "/rgg-sync/viewers/")

		if userID == "" {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "Viewer not found"})

			return
		}

		viewer, ok := runtime.GetViewer(userID)

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "Viewer not found"})

			return
		}

		writeJSON(response, http.StatusOK, viewer)
	})

	serve("/rgg-sync/viewers", func(
		runtime *Runtime, response http.ResponseWriter,
		request *http.Request,
	) {
		query := request.URL.Query()

		streak := query.Get("streak")

		if streak != "asc" && streak != "desc" {
			streak = ""
		}

		writeJSON(response, http.StatusOK, runtime.Snapshot(query.Get("letter"), streak))
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
