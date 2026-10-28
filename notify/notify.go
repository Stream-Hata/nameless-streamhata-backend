// Package notify — внешние уведомления: Discord webhook и Telegram-бот.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Settings — куда и о чём слать уведомления.
type Settings struct {
	Enabled         bool   `json:"enabled"`
	DiscordWebhook  string `json:"discordWebhook"`
	TelegramToken   string `json:"telegramToken"`
	TelegramChatID  string `json:"telegramChatId"`
	OnStreamOnline  bool   `json:"onStreamOnline"`
	OnStreamOffline bool   `json:"onStreamOffline"`
	OnAlerts        bool   `json:"onAlerts"`
	OnDonations     bool   `json:"onDonations"`
	Digest          bool   `json:"digest"`
}

func defaultSettings() Settings {
	return Settings{
		Enabled:         false,
		OnStreamOnline:  true,
		OnStreamOffline: true,
		OnAlerts:        true,
		OnDonations:     true,
		Digest:          true,
	}
}

type Service struct {
	mu       sync.Mutex
	settings Settings
	http     *http.Client
}

func New() *Service {
	return &Service{
		settings: defaultSettings(),
		http:     netcfg.NewHTTPClient(15 * time.Second),
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "notify"
}

func (s *Service) Name() string {
	return "Discord / Telegram"
}

func (s *Service) Kind() string {
	return "notify"
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings.Enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings.Enabled = enabled

	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	channels := make([]string, 0, 2)

	if s.settings.DiscordWebhook != "" {
		channels = append(channels, "discord")
	}

	if s.settings.TelegramToken != "" && s.settings.TelegramChatID != "" {
		channels = append(channels, "telegram")
	}

	if len(channels) == 0 {
		return "каналы не настроены"
	}

	return "каналы: " + strings.Join(channels, ", ")
}

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings
}

func (s *Service) SetSettings(settings Settings) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings = settings

	return s.settings
}

// Send рассылает сообщение во все настроенные каналы.
func (s *Service) Send(ctx context.Context, text string) {
	settings := s.GetSettings()

	if !settings.Enabled || strings.TrimSpace(text) == "" {
		return
	}

	if settings.DiscordWebhook != "" {
		if err := s.sendDiscord(ctx, settings.DiscordWebhook, text); err != nil {
			// Тихо: уведомления не должны ронять основные сценарии.
			_ = err
		}
	}

	if settings.TelegramToken != "" && settings.TelegramChatID != "" {
		if err := s.sendTelegram(ctx, settings.TelegramToken, settings.TelegramChatID, text); err != nil {
			_ = err
		}
	}
}

// SendTelegramText отправляет текст в Telegram по сохранённым настройкам.
func (s *Service) SendTelegramText(ctx context.Context, text string) error {
	settings := s.GetSettings()

	if settings.TelegramToken == "" || settings.TelegramChatID == "" {
		return fmt.Errorf("telegram не настроен: укажите token и chat id")
	}

	return s.sendTelegram(ctx, settings.TelegramToken, settings.TelegramChatID, text)
}

// Test sends a Telegram test message even when notifications are disabled.
func (s *Service) Test(ctx context.Context) error {
	settings := s.GetSettings()

	if settings.TelegramToken == "" || settings.TelegramChatID == "" {
		return fmt.Errorf("укажите Telegram bot token и chat id")
	}

	return s.sendTelegram(ctx, settings.TelegramToken, settings.TelegramChatID, "JuryKit: тестовое уведомление — канал связи работает")
}

// TelegramChats returns chats seen by the bot via getUpdates.
func (s *Service) TelegramChats(ctx context.Context) ([]map[string]any, error) {
	settings := s.GetSettings()
	if settings.TelegramToken == "" {
		return nil, fmt.Errorf("укажите Telegram bot token")
	}

	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100", settings.TelegramToken)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	response, err := s.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram getUpdates returned %d", response.StatusCode)
	}

	var payload struct {
		Result []struct {
			Message *struct {
				Chat struct {
					ID       int64  `json:"id"`
					Title    string `json:"title"`
					Username string `json:"username"`
					Type     string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}

	seen := make(map[int64]map[string]any)
	for _, update := range payload.Result {
		if update.Message == nil || update.Message.Chat.ID == 0 {
			continue
		}
		chat := update.Message.Chat
		seen[chat.ID] = map[string]any{
			"id":       chat.ID,
			"title":    chat.Title,
			"username": chat.Username,
			"type":     chat.Type,
		}
	}

	result := make([]map[string]any, 0, len(seen))
	for _, chat := range seen {
		result = append(result, chat)
	}
	return result, nil
}

func (s *Service) sendDiscord(ctx context.Context, webhook, text string) error {
	payload, err := json.Marshal(map[string]string{"content": text})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := s.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned %d", response.StatusCode)
	}

	return nil
}

func (s *Service) sendTelegram(ctx context.Context, token, chatID, text string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	payload, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}

	request.Header.Set("Content-Type", "application/json")

	response, err := s.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode >= 300 {
		return fmt.Errorf("telegram returned %d", response.StatusCode)
	}

	return nil
}

/* ── События ──────────────────────────────────────────────────────── */

// StreamEvent — события эфира.
func (s *Service) StreamEvent(online bool, title string) {
	settings := s.GetSettings()

	if online && !settings.OnStreamOnline {
		return
	}

	if !online && !settings.OnStreamOffline {
		return
	}

	if online {
		s.Send(context.Background(), fmt.Sprintf("🔴 Стрим начался: %s", orDefault(title, "без названия")))

		return
	}

	s.Send(context.Background(), "⚫️ Стрим завершён")
}

// AlertEvent — алерты канала (follow/sub/cheer/raid...).
func (s *Service) AlertEvent(alertType, username, message string) {
	settings := s.GetSettings()

	if !settings.OnAlerts {
		return
	}

	icons := map[string]string{
		"follow":     "💜",
		"subscribe":  "⭐",
		"gift":       "🎁",
		"resub":      "🔁",
		"cheer":      "💎",
		"raid":       "🚀",
		"hype_train": "🔥",
		"goal":       "🎯",
	}

	icon := orDefault(icons[alertType], "🔔")

	s.Send(context.Background(), fmt.Sprintf("%s %s — %s", icon, username, message))
}

// DonationEvent — донаты.
func (s *Service) DonationEvent(username string, amount float64, currency, message string) {
	settings := s.GetSettings()

	if !settings.OnDonations {
		return
	}

	text := fmt.Sprintf("💰 %s — %.0f %s", username, amount, currency)

	if message != "" {
		text += ": " + message
	}

	s.Send(context.Background(), text)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/notify", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, service.GetSettings())

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

		writeJSON(response, http.StatusOK, service.SetSettings(settings))
	})

	srv.ServeEndpoint("/rgg-sync/notify/telegram/chats", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		chats, err := service.TelegramChats(request.Context())
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"chats": chats})
	})

	srv.ServeEndpoint("/rgg-sync/notify/test", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if err := service.Test(request.Context()); err != nil {
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
