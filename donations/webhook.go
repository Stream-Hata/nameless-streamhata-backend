package donations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
)

// WebhookProvider — провайдер на вебхуках: DonationTo, Ko-fi, Donate.stream,
// DonatePay, Boosty-агенты и любой сервис с «URL уведомлений».
type WebhookProvider struct {
	name    string
	secret  string
	confirm string
	mu      sync.Mutex
	state   ProviderStatus
	enabled bool
}

func NewWebhookProvider(name, secret string) *WebhookProvider {
	return &WebhookProvider{
		name:    name,
		secret:  secret,
		enabled: true,
		state: ProviderStatus{
			Name:   name,
			State:  "listening",
			Detail: "ожидает вебхуки",
		},
	}
}

func (w *WebhookProvider) SetConfirm(value string) {
	w.confirm = value
}

func (w *WebhookProvider) Name() string {
	return w.name
}

func (w *WebhookProvider) Start(_ context.Context, _ Sink) error {
	return nil
}

func (w *WebhookProvider) Stop() error {
	return nil
}

// SetEnabled включает/выключает приём вебхуков.
func (w *WebhookProvider) SetEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.enabled = enabled
}

func (w *WebhookProvider) isEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.enabled
}

func (w *WebhookProvider) Status() ProviderStatus {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.state
}

func (w *WebhookProvider) setState(state, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.state = ProviderStatus{Name: w.name, State: state, Detail: detail}
}

// Handler принимает вебхук сервиса и публикует нормализованное событие.
func (w *WebhookProvider) Handler(sink Sink) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !w.isEnabled() {
			http.Error(response, "provider is disabled", http.StatusServiceUnavailable)

			return
		}

		if w.name == "donatestream" {
			body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)

				return
			}

			var probe struct {
				Type    string `json:"type"`
				UID     string `json:"uid"`
				Code    string `json:"code"`
				Confirm string `json:"confirm"`
				Token   string `json:"token"`
				Secret  string `json:"secret"`
			}

			if json.Unmarshal(body, &probe) == nil && strings.EqualFold(probe.Type, "confirm") && probe.UID != "" {
				log.Printf("[donatestream] confirm uid=%s query=%s headers=%v body=%s", probe.UID, request.URL.RawQuery, request.Header, string(body))
				confirm := w.confirm
				if confirm == "" {
					confirm = os.Getenv("DONATIONS_DONATESTREAM_CONFIRM")
				}

				if value := firstNonEmpty(
					request.URL.Query().Get("code"),
					request.URL.Query().Get("confirm"),
					request.URL.Query().Get("token"),
					request.URL.Query().Get("secret"),
					request.Header.Get("X-Confirm-Code"),
					request.Header.Get("X-Webhook-Code"),
					probe.Code,
					probe.Confirm,
					probe.Token,
					probe.Secret,
				); value != "" {
					confirm = value
				}

				if confirm != "" {
					response.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = response.Write([]byte(confirm))

					return
				}

				response.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(response).Encode(map[string]string{"uid": probe.UID})

				return
			}

			events := parseJSONPayload(body)
			if len(events) == 0 {
				w.setState("listening", "получен пустой вебхук")
				response.WriteHeader(http.StatusNoContent)

				return
			}

			for _, event := range events {
				event.Provider = w.name
				sink(event)
			}

			w.setState("listening", "получено событий: "+strconv.Itoa(len(events)))
			response.WriteHeader(http.StatusOK)

			return
		}

		events, err := w.parse(request)

		if err != nil {
			w.setState("error", err.Error())
			http.Error(response, err.Error(), http.StatusBadRequest)

			return
		}

		if len(events) == 0 {
			// Формат распознан, но полезной нагрузки нет — принимаем молча,
			// чтобы сервис не ретраил доставку.
			w.setState("listening", "получен пустой вебхук")

			response.WriteHeader(http.StatusNoContent)

			return
		}

		for _, event := range events {
			event.Provider = w.name
			sink(event)
		}

		w.setState("listening", "получено событий: "+strconv.Itoa(len(events)))

		response.WriteHeader(http.StatusOK)
	}
}

func (w *WebhookProvider) parse(request *http.Request) ([]Event, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if w.name == "kofi" {
		return w.parseKoFi(body)
	}

	if !w.verifyRequest(request) {
		return nil, http.ErrNoCookie
	}

	return parseJSONPayload(body), nil
}

func (w *WebhookProvider) verifyRequest(request *http.Request) bool {
	if w.secret == "" {
		return true
	}

	provided := request.URL.Query().Get("secret")

	if provided == "" {
		provided = request.Header.Get("X-Webhook-Secret")
	}

	if provided == "" {
		provided = request.Header.Get("X-Donation-Secret")
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(w.secret)) == 1
}

// parseKoFi разбирает вебхук Ko-fi: form-data с полем data = JSON-строка.
func (w *WebhookProvider) parseKoFi(body []byte) ([]Event, error) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}

	raw := values.Get("data")

	if raw == "" {
		// Некоторые прокси пересылают JSON напрямую.
		raw = string(body)
	}

	var data map[string]any

	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}

	if w.secret != "" {
		token, _ := data["verification_token"].(string)

		if subtle.ConstantTimeCompare([]byte(token), []byte(w.secret)) != 1 {
			return nil, http.ErrNoCookie
		}
	}

	event, ok := normalizeMap(data)
	if !ok {
		return nil, nil
	}

	return []Event{event}, nil
}

func parseJSONPayload(body []byte) []Event {
	var data map[string]any

	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}

	candidates := []map[string]any{
		unwrapData(data),
		data,
	}

	// Вложенные списки событий (некоторые сервисы шлют batch).
	for _, key := range []string{"events", "donations", "items"} {
		if items, ok := data[key].([]any); ok {
			var events []Event

			for _, item := range items {
				if mapped, ok := item.(map[string]any); ok {
					if event, ok := normalizeMap(unwrapData(mapped)); ok {
						events = append(events, event)
					}
				}
			}

			if len(events) > 0 {
				return events
			}
		}
	}

	for _, candidate := range candidates {
		if event, ok := normalizeMap(candidate); ok {
			return []Event{event}
		}
	}

	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}
