// Package netcfg — сетевые настройки: общий HTTP-клиент и прокси,
// настраиваемые из панели и применяемые ко всем исходящим запросам.
package netcfg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Settings — настройки сети.
type Settings struct {
	ProxyURL string `json:"proxyUrl"`
	Enabled  bool   `json:"enabled"`
}

// Provider хранит настройки и раздаёт клиентов с динамическим прокси.
type Provider struct {
	mu       sync.RWMutex
	settings Settings
	db       *sql.DB
}

var defaultProvider = &Provider{}

// Init создаёт провайдера, загружает настройки из БД и делает его глобальным.
func Init(db *sql.DB) *Provider {
	provider := &Provider{db: db}
	provider.load()
	defaultProvider = provider

	return provider
}

func Default() *Provider {
	return defaultProvider
}

func (p *Provider) Get() Settings {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.settings
}

// Update сохраняет настройки (невалидный URL отсекается).
func (p *Provider) Update(settings Settings) (Settings, error) {
	if settings.Enabled && settings.ProxyURL != "" {
		parsed, err := url.Parse(settings.ProxyURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return p.Get(), fmt.Errorf("некорректный proxy URL (пример: http://127.0.0.1:1080)")
		}
	}

	p.mu.Lock()
	p.settings = settings
	p.mu.Unlock()

	p.persist()

	return settings, nil
}

// proxyFunc — динамический выбор прокси для каждого запроса.
func (p *Provider) proxyFunc(request *http.Request) (*url.URL, error) {
	settings := p.Get()

	if !settings.Enabled || settings.ProxyURL == "" {
		return nil, nil
	}

	return url.Parse(settings.ProxyURL)
}

// ProxyFunc — для gorilla/websocket Dialer.
func ProxyFunc() func(*http.Request) (*url.URL, error) {
	return defaultProvider.proxyFunc
}

// NewHTTPClient — клиент, использующий актуальный прокси на каждый запрос.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 defaultProvider.proxyFunc,
			MaxIdleConns:          32,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
		},
	}
}

// Test проверяет доступность 7TV через текущий прокси.
func (p *Provider) Test(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://7tv.io/v3/emote-sets/global", nil)
	if err != nil {
		return err
	}

	response, err := NewHTTPClient(15 * time.Second).Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode >= 400 {
		return fmt.Errorf("сеть вернула %d", response.StatusCode)
	}

	return nil
}

func (p *Provider) load() {
	if p.db == nil {
		return
	}

	var raw []byte

	if err := p.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = 'network'`,
	).Scan(&raw); err != nil {
		return
	}

	var settings Settings

	if err := json.Unmarshal(raw, &settings); err == nil {
		p.mu.Lock()
		p.settings = settings
		p.mu.Unlock()
	}
}

func (p *Provider) persist() {
	if p.db == nil {
		return
	}

	raw, err := json.Marshal(p.Get())
	if err != nil {
		return
	}

	_, _ = p.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('network', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		string(raw),
	)
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, provider *Provider) {
	srv.ServeEndpoint("/rgg-sync/network", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, provider.Get())

			return
		}

		if request.Method != http.MethodPost && request.Method != http.MethodPatch {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var settings Settings

		if err := json.NewDecoder(request.Body).Decode(&settings); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		updated, err := provider.Update(settings)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, updated)
	})

	srv.ServeEndpoint("/rgg-sync/network/test", func(response http.ResponseWriter, request *http.Request) {
		if err := provider.Test(request.Context()); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
