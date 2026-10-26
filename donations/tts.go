package donations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

const (
	ttsUsersKey      = "donation_tts_users"
	ttsDefaultModel  = "canopylabs/orpheus-v1-english"
	ttsDefaultVoice  = "troy"
	ttsSpeechURL     = "https://api.groq.com/openai/v1/audio/speech"
	ttsMaxTextLength = 700
)

var groqVoices = []string{
	"autumn", "diana", "hannah", "austin", "daniel", "troy", "michael", "sophia",
}

type ttsUserSetting struct {
	Voice    string `json:"voice"`
	Provider string `json:"provider,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// TTS — озвучка донатов через Groq Orpheus с кэшем на диске.
type TTS struct {
	db       *sql.DB
	writer   chat.Writer
	key      string
	fishKey  string
	model    string
	cacheDir string
	http     *http.Client

	mu       sync.Mutex
	cooldown map[string]time.Time
	stats    func() []string
}

func NewTTS(db *sql.DB, writer chat.Writer) *TTS {
	directory := strings.TrimSpace(os.Getenv("MEDIA_UPLOAD_DIR"))
	if directory == "" {
		directory = "uploads"
	}

	return &TTS{
		db:       db,
		writer:   writer,
		key:      strings.TrimSpace(os.Getenv("GROQ_API_KEY")),
		fishKey:  strings.TrimSpace(os.Getenv("FISH_AUDIO_API_KEY")),
		model:    strings.TrimSpace(os.Getenv("GROQ_TTS_MODEL")),
		cacheDir: directory,
		http:     &http.Client{Timeout: 45 * time.Second},
		cooldown: make(map[string]time.Time),
	}
}

func (t *TTS) Configured() bool {
	return t.key != "" || t.fishKey != ""
}

func (t *TTS) ProviderConfigured(provider string) bool {
	switch provider {
	case "fishaudio":
		return t.fishKey != ""
	case "groq":
		return t.key != ""
	default:
		return t.Configured()
	}
}

func (t *TTS) SetStatsProvider(provider func() []string) {
	t.stats = provider
}

func (t *TTS) SettingFor(username string) ttsUserSetting {
	return t.userSetting(username)
}

func (t *TTS) Voices() []string {
	return append([]string(nil), groqVoices...)
}

func (t *TTS) userSetting(username string) ttsUserSetting {
	setting := ttsUserSetting{Voice: ttsDefaultVoice, Enabled: true}

	if t.db == nil || strings.TrimSpace(username) == "" {
		return setting
	}

	var raw []byte

	if err := t.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		ttsUsersKey,
	).Scan(&raw); err != nil {
		return setting
	}

	var users map[string]ttsUserSetting
	if json.Unmarshal(raw, &users) == nil {
		if saved, ok := users[strings.ToLower(username)]; ok {
			return saved
		}
	}

	return setting
}

func (t *TTS) saveUserSetting(username string, setting ttsUserSetting) error {
	if t.db == nil || strings.TrimSpace(username) == "" {
		return errors.New("tts: no database")
	}

	users := make(map[string]ttsUserSetting)

	var raw []byte

	if err := t.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		ttsUsersKey,
	).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &users)
	}

	users[strings.ToLower(username)] = setting
	encoded, err := json.Marshal(users)
	if err != nil {
		return err
	}

	_, err = t.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		ttsUsersKey,
		string(encoded),
	)

	return err
}

func (t *TTS) Speak(ctx context.Context, provider, text, voice, model string) (string, error) {
	if provider == "" || provider == "auto" {
		if hasCyrillic(text) {
			provider = "google"
		} else if t.fishKey != "" {
			provider = "fishaudio"
		} else {
			provider = "groq"
		}
	}

	if provider == "google" {
		return t.speakGoogle(ctx, text)
	}

	if provider == "fishaudio" {
		return t.speakFish(ctx, text, voice)
	}

	result, err := t.speakGroq(ctx, text, voice, model)
	if err != nil && hasCyrillic(text) {
		return t.speakGoogle(ctx, text)
	}

	return result, err
}

func hasCyrillic(value string) bool {
	for _, char := range value {
		if char >= 'а' && char <= 'я' || char >= 'А' && char <= 'Я' || char == 'ё' || char == 'Ё' {
			return true
		}
	}

	return false
}

func (t *TTS) speakGoogle(ctx context.Context, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("tts: empty text")
	}

	runes := []rune(text)
	if len(runes) > 190 {
		text = string(runes[:190])
	}

	hash := sha256.Sum256([]byte("google\x00ru\x00" + text))
	name := "tts-google-" + hex.EncodeToString(hash[:16]) + ".mp3"
	target := filepath.Join(t.cacheDir, name)

	if _, err := os.Stat(target); err == nil {
		return "/rgg-sync/media/file/" + name, nil
	}

	endpoint := "https://translate.google.com/translate_tts?ie=UTF-8&client=tw-ob&tl=ru&q=" + url.QueryEscape(text)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := t.http.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tts: Google returned %d", response.StatusCode)
	}

	if err := os.MkdirAll(t.cacheDir, 0o755); err != nil {
		return "", err
	}

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(file, io.LimitReader(response.Body, 20<<20)); err != nil {
		_ = file.Close()
		_ = os.Remove(target)

		return "", err
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(target)

		return "", err
	}

	return "/rgg-sync/media/file/" + name, nil
}

func (t *TTS) speakGroq(ctx context.Context, text, voice, model string) (string, error) {
	text = strings.TrimSpace(text)

	if text == "" {
		return "", errors.New("tts: empty text")
	}

	if len([]rune(text)) > ttsMaxTextLength {
		text = string([]rune(text)[:ttsMaxTextLength])
	}

	if voice == "" {
		voice = ttsDefaultVoice
	}

	if model == "" {
		model = t.model
	}

	if model == "" {
		model = ttsDefaultModel
	}

	if !t.Configured() {
		return "", errors.New("tts: GROQ_API_KEY is not configured")
	}

	hash := sha256.Sum256([]byte(model + "\x00" + voice + "\x00" + text))
	name := "tts-" + hex.EncodeToString(hash[:16]) + ".mp3"
	target := filepath.Join(t.cacheDir, name)

	if _, err := os.Stat(target); err == nil {
		return "/rgg-sync/media/file/" + name, nil
	}

	payload, err := json.Marshal(map[string]any{
		"model":           model,
		"input":           text,
		"voice":           voice,
		"response_format": "mp3",
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsSpeechURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}

	request.Header.Set("Authorization", "Bearer "+t.key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "audio/mpeg")

	response, err := t.http.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))

		return "", fmt.Errorf("tts: Groq returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := os.MkdirAll(t.cacheDir, 0o755); err != nil {
		return "", err
	}

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(file, io.LimitReader(response.Body, 20<<20)); err != nil {
		_ = file.Close()
		_ = os.Remove(target)

		return "", err
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(target)

		return "", err
	}

	return "/rgg-sync/media/file/" + name, nil
}

func (t *TTS) speakFish(ctx context.Context, text, voice string) (string, error) {
	text = strings.TrimSpace(text)

	if text == "" {
		return "", errors.New("tts: empty text")
	}

	if len([]rune(text)) > ttsMaxTextLength {
		text = string([]rune(text)[:ttsMaxTextLength])
	}

	if strings.TrimSpace(voice) == "" {
		return "", errors.New("tts: Fish Audio reference_id is empty; укажите голос модели")
	}

	if t.fishKey == "" {
		return "", errors.New("tts: FISH_AUDIO_API_KEY is not configured")
	}

	hash := sha256.Sum256([]byte("fish\x00" + voice + "\x00" + text))
	name := "tts-fish-" + hex.EncodeToString(hash[:16]) + ".mp3"
	target := filepath.Join(t.cacheDir, name)

	if _, err := os.Stat(target); err == nil {
		return "/rgg-sync/media/file/" + name, nil
	}

	payload, err := json.Marshal(map[string]any{
		"text":         text,
		"reference_id": voice,
		"format":       "mp3",
		"mp3_bitrate":  128,
		"chunk_length": 200,
		"normalize":    true,
		"latency":      "normal",
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.fish.audio/v1/tts", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}

	request.Header.Set("Authorization", "Bearer "+t.fishKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "audio/mpeg")

	response, err := t.http.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))

		return "", fmt.Errorf("tts: Fish Audio returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	if err := os.MkdirAll(t.cacheDir, 0o755); err != nil {
		return "", err
	}

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(file, io.LimitReader(response.Body, 20<<20)); err != nil {
		_ = file.Close()
		_ = os.Remove(target)

		return "", err
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(target)

		return "", err
	}

	return "/rgg-sync/media/file/" + name, nil
}

// HandleMessage обрабатывает !donatevoice / !dtts и !donations.
func (t *TTS) HandleMessage(message twitch.TwitchChatMessage) {
	text := strings.TrimSpace(message.Text)
	lower := strings.ToLower(text)
	fields := strings.Fields(lower)

	if len(fields) == 0 {
		return
	}

	command := fields[0]

	if command != "!donatevoice" && command != "!dtts" && command != "!donations" {
		return
	}

	login := strings.ToLower(message.Author.Login)
	if login == "" {
		login = strings.ToLower(message.Author.DisplayName)
	}

	if command == "!donations" {
		if t.writer == nil {
			return
		}

		stats := t.busStats()
		if len(stats) == 0 {
			_ = t.writer.Say("Донатов пока нет.")

			return
		}

		_ = t.writer.Say("Топ донатов: " + strings.Join(stats, " · "))

		return
	}

	args := strings.Fields(text)
	setting := t.userSetting(login)

	if len(args) == 1 || (len(args) > 1 && strings.EqualFold(args[1], "show")) {
		state := "выключена"
		if setting.Enabled {
			state = "включена"
		}

		_ = t.writer.Say(fmt.Sprintf("@%s: озвучка %s, голос %s. !donatevoice voice <голос>, !donatevoice on/off", message.Author.DisplayName, state, setting.Voice))

		return
	}

	switch strings.ToLower(args[1]) {
	case "provider":
		if len(args) < 3 {
			_ = t.writer.Say("Использование: !donatevoice provider groq|fishaudio|browser")

			return
		}

		provider := strings.ToLower(args[2])
		if provider != "groq" && provider != "fishaudio" && provider != "browser" {
			_ = t.writer.Say("Провайдер: groq, fishaudio или browser")

			return
		}

		setting.Provider = provider
		_ = t.saveUserSetting(login, setting)
		_ = t.writer.Say("Провайдер озвучки: " + provider)
	case "on":
		setting.Enabled = true
		_ = t.saveUserSetting(login, setting)
		_ = t.writer.Say("Озвучка донатов включена.")
	case "off":
		setting.Enabled = false
		_ = t.saveUserSetting(login, setting)
		_ = t.writer.Say("Озвучка донатов выключена.")
	case "reset":
		setting = ttsUserSetting{Voice: ttsDefaultVoice, Enabled: true}
		_ = t.saveUserSetting(login, setting)
		_ = t.writer.Say("Настройки озвучки сброшены.")
	case "voices":
		_ = t.writer.Say("Голоса Groq: " + strings.Join(groqVoices, ", "))
	case "voice":
		if len(args) < 3 {
			_ = t.writer.Say("Использование: !donatevoice voice <голос>. Голоса: " + strings.Join(groqVoices, ", "))

			return
		}

		voice := strings.ToLower(args[2])
		if setting.Provider != "fishaudio" && !containsVoice(groqVoices, voice) {
			_ = t.writer.Say("Неизвестный голос. Для Groq доступно: " + strings.Join(groqVoices, ", "))

			return
		}

		setting.Voice = voice
		_ = t.saveUserSetting(login, setting)
		_ = t.writer.Say("Голос доната: " + voice)
	default:
		_ = t.writer.Say("Команды: !donatevoice voice <голос>, !donatevoice on, !donatevoice off, !donatevoice reset, !donatevoice voices")
	}
}

func (t *TTS) busStats() []string {
	// Заполняется через SetStatsProvider, чтобы не связывать пакет с шиной напрямую.
	if t.stats == nil {
		return nil
	}

	return t.stats()
}

func containsVoice(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}

	return false
}
