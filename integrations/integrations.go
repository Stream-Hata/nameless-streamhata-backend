// Package integrations — реестр подключаемых подсистем (донаты, музыка,
// будущие сервисы): единый список для страницы настроек и включение/выключение.
package integrations

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

// Component — подсистема, которой можно управлять из настроек.
type Component interface {
	ID() string
	Name() string
	Kind() string
	Enabled() bool
	SetEnabled(enabled bool) error
	Status() string
}

type Item struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

type Registry struct {
	mu    sync.RWMutex
	items map[string]Component
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Component)}
}

func (r *Registry) Register(component Component) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.items[component.ID()] = component
}

func (r *Registry) List() []Item {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]Item, 0, len(r.items))

	for _, component := range r.items {
		list = append(list, Item{
			ID:      component.ID(),
			Name:    component.Name(),
			Kind:    component.Kind(),
			Enabled: component.Enabled(),
			Status:  component.Status(),
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})

	return list
}

func (r *Registry) SetEnabled(id string, enabled bool) (Item, bool) {
	r.mu.RLock()
	component, ok := r.items[id]
	r.mu.RUnlock()

	if !ok {
		return Item{}, false
	}

	_ = component.SetEnabled(enabled)

	return Item{
		ID:      component.ID(),
		Name:    component.Name(),
		Kind:    component.Kind(),
		Enabled: component.Enabled(),
		Status:  component.Status(),
	}, true
}

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, registry *Registry) {
	srv.ServeEndpoint("/rgg-sync/integrations", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"integrations": registry.List(),
		})
	})

	srv.ServeEndpoint("/rgg-sync/integrations/{id}", func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Enabled bool `json:"enabled"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		item, ok := registry.SetEnabled(request.PathValue("id"), body.Enabled)

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "unknown integration"})

			return
		}

		writeJSON(response, http.StatusOK, item)
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
