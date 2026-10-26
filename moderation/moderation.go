// Package moderation — фильтры чата (слова, ссылки, капс, эмодзи, повторы),
// действия (удаление/таймаут), сухой прогон и лог модерации.
package moderation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Filters — настройки фильтрации.
type Filters struct {
	DryRun bool `json:"dryRun"`

	BannedWords []string `json:"bannedWords"`

	BlockLinks bool `json:"blockLinks"`

	CapsEnabled   bool `json:"capsEnabled"`
	CapsPercent   int  `json:"capsPercent"`
	CapsMinLength int  `json:"capsMinLength"`

	EmojiEnabled bool `json:"emojiEnabled"`
	EmojiMax     int  `json:"emojiMax"`

	RepeatEnabled       bool `json:"repeatEnabled"`
	RepeatMax           int  `json:"repeatMax"`
	RepeatWindowSeconds int  `json:"repeatWindowSeconds"`

	ExemptModerators bool `json:"exemptModerators"`

	Action         string `json:"action"`
	TimeoutSeconds int    `json:"timeoutSeconds"`

	AIEnabled      bool    `json:"aiEnabled"`
	AIThreshold    float64 `json:"aiThreshold"`
	AIWindowChecks int     `json:"aiWindowChecks"`
}

func defaultFilters() Filters {
	return Filters{
		BannedWords:         []string{},
		CapsPercent:         70,
		CapsMinLength:       15,
		EmojiMax:            8,
		RepeatMax:           3,
		RepeatWindowSeconds: 30,
		ExemptModerators:    true,
		Action:              "delete",
		TimeoutSeconds:      600,
		AIThreshold:         0.7,
		AIWindowChecks:      30,
	}
}

// LogEntry — запись лога модерации.
type LogEntry struct {
	ID        string    `json:"id"`
	User      string    `json:"user"`
	Text      string    `json:"text"`
	Reason    string    `json:"reason"`
	Action    string    `json:"action"`
	DryRun    bool      `json:"dryRun"`
	CreatedAt time.Time `json:"createdAt"`
}

// ToxicityChecker — AI-проверка (реализуется адаптером над llm).
type ToxicityChecker interface {
	CheckToxicity(ctx context.Context, message string) (score float64, reason string, err error)
}

// Actioner выполняет действия в Twitch (реализуется chatruntime).
type Actioner interface {
	DeleteMessage(ctx context.Context, messageID string) error
	TimeoutUser(ctx context.Context, userID string, seconds int, reason string) error
	BanUser(ctx context.Context, userID string, reason string) error
}

// Service хранит настройки, лог и историю повторов.
type Service struct {
	mu      sync.Mutex
	filters Filters
	log     []LogEntry
	recent  map[string][]recentMessage

	writer  chat.Writer
	actions Actioner
	now     func() time.Time

	ai       ToxicityChecker
	aiChecks []time.Time
}

type recentMessage struct {
	hash string
	at   time.Time
}

// SetToxicityChecker подключает AI-модерацию.
func (s *Service) SetToxicityChecker(checker ToxicityChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ai = checker
}

func New(writer chat.Writer, actions Actioner) *Service {
	return &Service{
		filters: defaultFilters(),
		recent:  make(map[string][]recentMessage),
		writer:  writer,
		actions: actions,
		now:     time.Now,
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "moderation"
}

func (s *Service) Name() string {
	return "Модерация"
}

func (s *Service) Kind() string {
	return "moderation"
}

func (s *Service) Enabled() bool {
	return true
}

func (s *Service) SetEnabled(bool) error {
	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	mode := "боевой"

	if s.filters.DryRun {
		mode = "сухой прогон"
	}

	return fmt.Sprintf("режим: %s, записей в логе: %d", mode, len(s.log))
}

func (s *Service) GetFilters() Filters {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.filters
}

func (s *Service) SetFilters(filters Filters) Filters {
	s.mu.Lock()
	defer s.mu.Unlock()

	if filters.CapsPercent <= 0 || filters.CapsPercent > 100 {
		filters.CapsPercent = 70
	}

	if filters.CapsMinLength <= 0 {
		filters.CapsMinLength = 15
	}

	if filters.EmojiMax <= 0 {
		filters.EmojiMax = 8
	}

	if filters.RepeatMax <= 0 {
		filters.RepeatMax = 3
	}

	if filters.RepeatWindowSeconds <= 0 {
		filters.RepeatWindowSeconds = 30
	}

	if filters.Action != "timeout" {
		filters.Action = "delete"
	}

	if filters.TimeoutSeconds <= 0 {
		filters.TimeoutSeconds = 600
	}

	if filters.BannedWords == nil {
		filters.BannedWords = []string{}
	}

	s.filters = filters

	return s.filters
}

func (s *Service) Log() []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]LogEntry, 0, len(s.log))

	for i := len(s.log) - 1; i >= 0; i-- {
		list = append(list, s.log[i])
	}

	return list
}

/* ── Обработка сообщений ──────────────────────────────────────────── */

// HandleMessage проверяет сообщение и применяет действие.
func (s *Service) HandleMessage(ctx context.Context, message twitch.TwitchChatMessage) {
	if message.Author.ID == "" {
		return
	}

	reason := s.reason(message)

	if reason == "" {
		return
	}

	filters := s.GetFilters()
	action := filters.Action
	entry := LogEntry{
		ID:        fmt.Sprintf("mod-%d", s.now().UnixNano()),
		User:      message.Author.DisplayName,
		Text:      message.Text,
		Reason:    reason,
		Action:    action,
		DryRun:    filters.DryRun,
		CreatedAt: s.now().UTC(),
	}

	s.mu.Lock()
	s.log = append(s.log, entry)

	if len(s.log) > 500 {
		s.log = s.log[len(s.log)-500:]
	}

	s.mu.Unlock()

	if filters.DryRun || s.actions == nil {
		if s.writer != nil && filters.DryRun {
			_ = s.writer.Say(fmt.Sprintf("[модерация: сухой прогон] %s — %s", entry.User, reason))
		}

		return
	}

	switch action {
	case "timeout":
		_ = s.actions.TimeoutUser(ctx, message.Author.ID, filters.TimeoutSeconds, reason)
	default:
		_ = s.actions.DeleteMessage(ctx, message.MessageID)
	}
}

// reason возвращает причину нарушения или пустую строку.
func (s *Service) reason(message twitch.TwitchChatMessage) string {
	text := strings.TrimSpace(message.Text)

	if text == "" {
		return ""
	}

	filters := s.GetFilters()

	if filters.ExemptModerators && isModerator(message) {
		return ""
	}

	if reason := matchBannedWord(text, filters.BannedWords); reason != "" {
		return reason
	}

	if filters.BlockLinks && containsLink(text) {
		return "ссылка в чате"
	}

	if filters.CapsEnabled && isCapsSpam(text, filters.CapsPercent, filters.CapsMinLength) {
		return "капс"
	}

	if filters.EmojiEnabled && emojiCount(text) > filters.EmojiMax {
		return "спам эмодзи"
	}

	if filters.RepeatEnabled && s.isRepeating(message, filters) {
		return "повтор сообщения"
	}

	if filters.AIEnabled {
		if reason := s.checkAI(message, filters); reason != "" {
			return reason
		}
	}

	return ""
}

// checkAI спрашивает классификатор (с лимитом проверок в минуту).
func (s *Service) checkAI(message twitch.TwitchChatMessage, filters Filters) string {
	s.mu.Lock()
	checker := s.ai

	if checker == nil {
		s.mu.Unlock()

		return ""
	}

	now := s.now()
	window := make([]time.Time, 0, len(s.aiChecks))

	for _, at := range s.aiChecks {
		if now.Sub(at) < time.Minute {
			window = append(window, at)
		}
	}

	s.aiChecks = window

	if len(window) >= filters.AIWindowChecks {
		s.mu.Unlock()

		return ""
	}

	s.aiChecks = append(s.aiChecks, now)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	score, reason, err := checker.CheckToxicity(ctx, message.Text)
	if err != nil || score < filters.AIThreshold {
		return ""
	}

	if reason == "" {
		reason = "токсичность"
	}

	return "AI: " + reason
}

var linkPattern = regexp.MustCompile(`(?i)(https?://|www\.|\.[a-z]{2,}/)`)

func containsLink(text string) bool {
	return linkPattern.MatchString(text)
}

func matchBannedWord(text string, words []string) string {
	lower := strings.ToLower(text)

	for _, word := range words {
		word = strings.TrimSpace(word)

		if word == "" {
			continue
		}

		if strings.Contains(lower, strings.ToLower(word)) {
			return "запрещённое слово: " + word
		}
	}

	return ""
}

func isCapsSpam(text string, percent, minLength int) bool {
	letters := 0
	upper := 0

	for _, char := range text {
		if !unicode.IsLetter(char) {
			continue
		}

		letters++

		if unicode.IsUpper(char) {
			upper++
		}
	}

	if letters < minLength {
		return false
	}

	return upper*100/letters >= percent
}

func emojiCount(text string) int {
	count := 0

	for _, char := range text {
		if char > 0x2700 {
			count++
		}
	}

	return count
}

func isModerator(message twitch.TwitchChatMessage) bool {
	// Twitch IRC не отдаёт badges в нашей модели; считаем, что модерация
	// распространяется только на broadcaster'а — этого достаточно для
	// «сухого» режима и защиты самого стримера.
	return message.Author.ID == message.Broadcaster.ID
}

func (s *Service) isRepeating(message twitch.TwitchChatMessage, filters Filters) bool {
	window := time.Duration(filters.RepeatWindowSeconds) * time.Second
	hash := strings.ToLower(strings.TrimSpace(message.Text))
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.recent[message.Author.ID]
	trimmed := history[:0]

	count := 0

	for _, item := range history {
		if now.Sub(item.at) <= window {
			trimmed = append(trimmed, item)

			if item.hash == hash {
				count++
			}
		}
	}

	if count+1 > filters.RepeatMax {
		return true
	}

	s.recent[message.Author.ID] = append(trimmed, recentMessage{hash: hash, at: now})

	return false
}

// ModerateUser выполняет действие модератора и пишет его в лог.
func (s *Service) ModerateUser(
	action, userID, displayName, messageID, reason string,
	seconds int,
) error {
	if s.actions == nil {
		return fmt.Errorf("moderation: actions are not available")
	}

	var err error

	switch action {
	case "ban":
		err = s.actions.BanUser(context.Background(), userID, reason)
	case "timeout":
		if seconds <= 0 {
			seconds = 600
		}

		err = s.actions.TimeoutUser(context.Background(), userID, seconds, reason)
	case "delete":
		err = s.actions.DeleteMessage(context.Background(), messageID)
	default:
		return fmt.Errorf("moderation: unknown action %q", action)
	}

	if err != nil {
		return err
	}

	s.mu.Lock()
	s.log = append(s.log, LogEntry{
		ID:        fmt.Sprintf("mod-%d", s.now().UnixNano()),
		User:      displayName,
		Text:      "",
		Reason:    reason,
		Action:    action,
		DryRun:    false,
		CreatedAt: s.now().UTC(),
	})

	if len(s.log) > 500 {
		s.log = s.log[len(s.log)-500:]
	}

	s.mu.Unlock()

	return nil
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/moderation", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, map[string]any{
				"filters": service.GetFilters(),
				"log":     service.Log(),
			})
		case http.MethodPatch, http.MethodPost:
			var filters Filters

			if err := json.NewDecoder(request.Body).Decode(&filters); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			writeJSON(response, http.StatusOK, service.SetFilters(filters))
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/moderation/log", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{"log": service.Log()})
	})

	srv.ServeEndpoint("/rgg-sync/moderation/action", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Action      string `json:"action"`
			UserID      string `json:"userId"`
			DisplayName string `json:"displayName"`
			MessageID   string `json:"messageId"`
			Reason      string `json:"reason"`
			Seconds     int    `json:"seconds"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		if err := service.ModerateUser(
			body.Action,
			body.UserID,
			body.DisplayName,
			body.MessageID,
			body.Reason,
			body.Seconds,
		); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true})
	})

	RegisterWordListRoutes(srv)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
