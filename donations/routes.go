package donations

import (
	"encoding/json"
	"net/http"
)

// Snapshot — ответ панели: последние донаты, статистика и статусы провайдеров.
type Snapshot struct {
	Recent    []Event          `json:"recent"`
	Stats     Stats            `json:"stats"`
	Providers []ProviderStatus `json:"providers"`
	Goals     []Goal           `json:"goals"`
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

// RegisterRoutes вешает HTTP-ручки донатов на сервер.
func RegisterRoutes(srv Deps, service *Service) {
	serveSnapshot(srv, service)
	serveWebhooks(srv, service)
	serveOAuth(srv, service)
}

func serveSnapshot(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/donations", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		snapshot := Snapshot{
			Recent:    service.Bus().Recent(50),
			Stats:     service.Bus().Stats(),
			Providers: service.Statuses(),
			Goals:     service.Goals(),
		}

		writeJSON(response, snapshot)
	})
}

func serveWebhooks(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/donations/webhook/{provider}", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		name := request.PathValue("provider")
		provider, ok := service.provider(name)

		if !ok {
			http.Error(response, "unknown provider", http.StatusNotFound)

			return
		}

		webhook, ok := provider.(*WebhookProvider)

		if !ok {
			http.Error(response, "provider does not accept webhooks", http.StatusBadRequest)

			return
		}

		webhook.Handler(service.Bus().Publish)(response, request)
	})
}

func serveOAuth(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/donations/donationalerts/auth", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		provider, ok := service.provider("donationalerts")

		if !ok {
			http.Error(response, "donationalerts provider is not configured", http.StatusNotFound)

			return
		}

		da, ok := provider.(*DonationAlerts)

		if !ok {
			http.Error(response, "invalid provider", http.StatusInternalServerError)

			return
		}

		state, authURL, err := da.BeginAuth()
		if err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)

			return
		}

		writeJSON(response, map[string]string{
			"state": state,
			"url":   authURL,
		})
	})

	srv.ServeEndpoint("/rgg-sync/donations/donationalerts/callback", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		provider, ok := service.provider("donationalerts")

		if !ok {
			http.Error(response, "donationalerts provider is not configured", http.StatusNotFound)

			return
		}

		da, ok := provider.(*DonationAlerts)

		if !ok {
			http.Error(response, "invalid provider", http.StatusInternalServerError)

			return
		}

		state := request.URL.Query().Get("state")
		code := request.URL.Query().Get("code")

		if state == "" || code == "" {
			http.Error(response, "state and code are required", http.StatusBadRequest)

			return
		}

		if err := da.CompleteAuth(request.Context(), state, code); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)

			return
		}

		da.setState("connected", "авторизация завершена")

		// Возвращаем стримера обратно в панель.
		http.Redirect(response, request, "/app/donations", http.StatusTemporaryRedirect)
	})
}

func (s *Service) provider(name string) (Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	provider, ok := s.providers[name]

	return provider, ok
}

func writeJSON(response http.ResponseWriter, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusOK)

	_ = json.NewEncoder(response).Encode(payload)
}
