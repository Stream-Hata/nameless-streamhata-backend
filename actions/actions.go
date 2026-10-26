// Package actions — HTTP API для деков (Stream Deck и т.п.):
// именованные действия под токеном из env DECK_API_KEY.
package actions

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// Handler выполняет действие и возвращает текст результата.
type Handler func(ctx context.Context, params map[string]string) (string, error)

type action struct {
	Name        string
	Description string
	Handler     Handler
}

type Registry struct {
	mu      sync.RWMutex
	actions map[string]action
	token   string
}

func New() *Registry {
	return &Registry{
		actions: make(map[string]action),
		token:   os.Getenv("DECK_API_KEY"),
	}
}

func (r *Registry) Register(name, description string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.actions[name] = action{Name: name, Description: description, Handler: handler}
}

type Item struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (r *Registry) List() []Item {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]Item, 0, len(r.actions))

	for _, item := range r.actions {
		items = append(items, Item{Name: item.Name, Description: item.Description})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	return items
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (r *Registry) ID() string {
	return "actions"
}

func (r *Registry) Name() string {
	return "API для деков"
}

func (r *Registry) Kind() string {
	return "actions"
}

func (r *Registry) Enabled() bool {
	return r.token != ""
}

func (r *Registry) SetEnabled(bool) error {
	return nil
}

func (r *Registry) Status() string {
	if r.token == "" {
		return "токен не задан (DECK_API_KEY)"
	}

	return "действий: " + strings.Join(r.names(), ", ")
}

func (r *Registry) names() []string {
	items := r.List()
	names := make([]string, 0, len(items))

	for _, item := range items {
		names = append(names, item.Name)
	}

	return names
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, registry *Registry) {
	srv.ServeEndpoint("/rgg-sync/actions", func(response http.ResponseWriter, request *http.Request) {
		if registry.token == "" {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{
				"error": "API выключен: задайте DECK_API_KEY в .env",
			})

			return
		}

		if !registry.authorized(request) {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "неверный X-API-Key"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"actions": registry.List()})
	})

	srv.ServeEndpoint("/rgg-sync/actions/{name}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if registry.token == "" {
			writeJSON(response, http.StatusServiceUnavailable, map[string]string{
				"error": "API выключен: задайте DECK_API_KEY в .env",
			})

			return
		}

		if !registry.authorized(request) {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "неверный X-API-Key"})

			return
		}

		name := request.PathValue("name")

		registry.mu.RLock()
		item, ok := registry.actions[name]
		registry.mu.RUnlock()

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "неизвестное действие"})

			return
		}

		params := map[string]string{}

		_ = json.NewDecoder(request.Body).Decode(&params)

		result, err := item.Handler(request.Context(), params)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "result": result})
	})
}

func (r *Registry) authorized(request *http.Request) bool {
	provided := request.Header.Get("X-API-Key")

	if provided == "" {
		provided = request.URL.Query().Get("token")
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(r.token)) == 1
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
