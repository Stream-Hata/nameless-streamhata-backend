package trust

import (
	"io"
	"net/http"

	"github.com/p7x-dev/nameless-streamhata-backend/pluginmanifest"
)

func (s *Service) manifestValidateRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, pluginmanifest.MaxSize))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "failed to read manifest"})

		return
	}
	manifest, err := pluginmanifest.Parse(body)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"valid": true, "manifest": manifest})
}
