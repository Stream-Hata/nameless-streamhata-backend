// Package appdownload отдаёт desktop-сборку напрямую, без страницы релизов.
package appdownload

import (
	"net/http"
	"os"
	"path/filepath"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps) {
	handler := func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if url := os.Getenv("APP_DOWNLOAD_URL"); url != "" {
			http.Redirect(response, request, url, http.StatusFound)

			return
		}

		if file := os.Getenv("APP_DOWNLOAD_FILE"); file != "" {
			path, err := filepath.Abs(file)
			if err != nil {
				http.Error(response, "invalid download file", http.StatusInternalServerError)

				return
			}

			http.ServeFile(response, request, path)

			return
		}

		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.WriteHeader(http.StatusNotFound)
		_, _ = response.Write([]byte(`{"error":"desktop build is not configured yet"}`))
	}

	srv.ServeEndpoint("/rgg-sync/app/download", handler)
}
