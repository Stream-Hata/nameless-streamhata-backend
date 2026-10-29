// Package notify — внешние уведомления: Discord webhook и Telegram-бот.
package notify

import (
	"bytes"
	"context"
	"database/sql"
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
	Enabled         bool     `json:"enabled"`
	DiscordWebhook  string   `json:"discordWebhook"`
	TelegramToken   string   `json:"telegramToken"`
	TelegramChatID  string   `json:"telegramChatId"`
	TelegramChatIDs []string `json:"telegramChatIds"`
	OnStreamOnline  bool     `json:"onStreamOnline"`
	OnStreamOffline bool     `json:"onStreamOffline"`
	OnAlerts        bool     `json:"onAlerts"`
	OnDonations     bool     `json:"onDonations"`
	Digest          bool     `json:"digest"`
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
	mu               sync.Mutex
	settings         Settings
	telegramCommands []TelegramCommand
	telegramChats    map[int64]TelegramChat
	http             *http.Client
	db               *sql.DB
}

func New(db *sql.DB) *Service {
	service := &Service{
		settings:      defaultSettings(),
		telegramChats: make(map[int64]TelegramChat),
		http:          netcfg.NewHTTPClient(15 * time.Second),
		db:            db,
	}
	service.load()
	service.loadTelegramCommands()

	return service
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
	if settings.TelegramChatIDs == nil {
		settings.TelegramChatIDs = s.settings.TelegramChatIDs
	}
	if len(settings.TelegramChatIDs) == 0 && strings.TrimSpace(settings.TelegramChatID) != "" {
		settings.TelegramChatIDs = []string{strings.TrimSpace(settings.TelegramChatID)}
	}
	settings.TelegramChatIDs = uniqueChatIDs(settings.TelegramChatIDs)
	if len(settings.TelegramChatIDs) > 0 {
		settings.TelegramChatID = settings.TelegramChatIDs[0]
	}
	s.settings = settings
	s.mu.Unlock()
	s.persist()

	return s.GetSettings()
}

func uniqueChatIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))

	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func (s *Service) telegramChatIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	ids := uniqueChatIDs(s.settings.TelegramChatIDs)
	if len(ids) == 0 && strings.TrimSpace(s.settings.TelegramChatID) != "" {
		ids = []string{strings.TrimSpace(s.settings.TelegramChatID)}
	}

	return ids
}

func (s *Service) telegramAllChatIDs() []string {
	ids := s.telegramChatIDs()
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		seen[id] = struct{}{}
	}

	s.mu.Lock()
	for chat := range s.telegramChats {
		id := fmt.Sprintf("%d", chat)
		if _, ok := seen[id]; !ok {
			ids = append(ids, id)
			seen[id] = struct{}{}
		}
	}
	s.mu.Unlock()

	return ids
}

func (s *Service) isTelegramChat(chat TelegramChat) bool {
	// Бот отвечает только в чатах, привязанных к аккаунту стримера.
	chatID := fmt.Sprintf("%d", chat.ID)
	for _, id := range s.telegramChatIDs() {
		if id == chatID {
			return true
		}
	}

	return false
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'notify_settings'`).Scan(&raw); err != nil {
		return
	}

	var settings Settings
	if json.Unmarshal(raw, &settings) == nil {
		if len(settings.TelegramChatIDs) == 0 && strings.TrimSpace(settings.TelegramChatID) != "" {
			settings.TelegramChatIDs = []string{strings.TrimSpace(settings.TelegramChatID)}
		}
		s.settings = settings
	}
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.GetSettings())
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('notify_settings', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
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

	if settings.TelegramToken != "" {
		for _, chatID := range s.telegramChatIDs() {
			if err := s.sendTelegram(ctx, settings.TelegramToken, chatID, text); err != nil {
				_ = err
			}
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

	if settings.TelegramToken == "" {
		return fmt.Errorf("укажите Telegram bot token")
	}

	ids := s.telegramAllChatIDs()
	if len(ids) == 0 {
		return fmt.Errorf("укажите Telegram chat id")
	}

	var lastErr error
	for _, chatID := range ids {
		if err := s.sendTelegram(ctx, settings.TelegramToken, chatID, "JuryKit: тестовое уведомление — канал связи работает"); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// TelegramChats returns chats the bot has seen in incoming updates.
func (s *Service) TelegramChats(ctx context.Context) ([]map[string]any, error) {
	_ = ctx
	settings := s.GetSettings()
	if settings.TelegramToken == "" {
		return nil, fmt.Errorf("укажите Telegram bot token")
	}

	chats := s.TelegramChatsSeen()
	result := make([]map[string]any, 0, len(chats))
	for _, chat := range chats {
		result = append(result, map[string]any{
			"id":       chat.ID,
			"title":    chat.Title,
			"username": chat.Username,
			"type":     chat.Type,
		})
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
	if len([]rune(text)) > 4000 {
		text = string([]rune(text)[:4000]) + "…"
	}

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
		var errorPayload struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(response.Body).Decode(&errorPayload)
		if errorPayload.Description != "" {
			return fmt.Errorf("telegram: %s", errorPayload.Description)
		}

		return fmt.Errorf("telegram returned %d", response.StatusCode)
	}

	return nil
}

/* ── События ──────────────────────────────────────────────────────── */

// StreamEvent — события эфира.
func (s *Service) StreamEvent(online bool, title, channelLogin string) {
	settings := s.GetSettings()

	if online && !settings.OnStreamOnline {
		return
	}

	if !online && !settings.OnStreamOffline {
		return
	}

	if online {
		message := fmt.Sprintf("🔴 Стрим начался: %s", orDefault(title, "без названия"))
		if channelLogin != "" {
			message += fmt.Sprintf("\nhttps://twitch.tv/%s", channelLogin)
		}
		s.Send(context.Background(), message)

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
	registerTelegramCommandRoutes(srv, service)
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
