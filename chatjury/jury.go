// Package chatjury — «чат-жюри»: AI отбирает советы, чат голосует,
// вердикт формируется автоматически.
package chatjury

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

var (
	errInvalidDuration   = errors.New("duration_seconds must be a positive integer")
	errInvalidConfidence = errors.New("min_confidence must be between 0 and 1")
)

/* ── Типы ─────────────────────────────────────────────────────────── */

type ChatJuryMessage struct {
	ID      string `json:"id"`
	Author  Author `json:"author"`
	Text    string `json:"text"`
	Created string `json:"created_at"`

	AdviceConfidence float64 `json:"advice_confidence"`
}

type Author struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type Votes struct {
	Yea int `json:"yea"`
	Nei int `json:"nei"`
}

type Settings struct {
	Enabled         bool    `json:"enabled"`
	DurationSeconds int     `json:"duration_seconds"`
	MinConfidence   float64 `json:"min_confidence"`
}

// SettingsPatch — частичное обновление настроек (nil = не менять).
type SettingsPatch struct {
	Enabled         *bool    `json:"enabled"`
	DurationSeconds *int     `json:"duration_seconds"`
	MinConfidence   *float64 `json:"min_confidence"`
}

// Public — состояние текущего голосования для панели.
type Public struct {
	ID       string          `json:"id"`
	Status   string          `json:"status"`
	Message  ChatJuryMessage `json:"message"`
	Votes    Votes           `json:"votes"`
	Total    int             `json:"total"`
	Approval float64         `json:"approval_rate"`
	Verdict  *string         `json:"verdict"`

	StartedAt string `json:"started_at"`
	EndsAt    string `json:"ends_at"`
}

// Result — итог закрытого голосования.
type Result struct {
	ID       string          `json:"id"`
	Message  ChatJuryMessage `json:"message"`
	Status   string          `json:"status"`
	Yea      int             `json:"yea"`
	Nei      int             `json:"nei"`
	Total    int             `json:"total"`
	Approval float64         `json:"approval_rate"`
	Verdict  *string         `json:"verdict"`

	StartedAt string `json:"started_at"`
	EndsAt    string `json:"ends_at"`
}

type jury struct {
	id      string
	message ChatJuryMessage
	status  string
	votes   Votes
	voters  map[string]struct{}

	startedAt time.Time
	endsAt    time.Time
}

const (
	statusVoting   = "voting"
	statusFinished = "finished"

	defaultDurationSeconds = 30
	defaultMinConfidence   = 0.8
)

/* ── Сервис ───────────────────────────────────────────────────────── */

// Service хранит текущее голосование, настройки и последний вердикт.
type Service struct {
	mu sync.Mutex

	current    *jury
	lastResult *Result

	settings Settings

	verdicts llm.VerdictGenerator

	finishTimer     *time.Timer
	verdictQueryID  int
	onFinishedHooks []func()
	startedSequence int
}

func NewService(verdicts llm.VerdictGenerator) *Service {
	if verdicts == nil {
		verdicts = llm.FallbackVerdictGenerator{}
	}

	return &Service{
		settings: Settings{
			Enabled:         true,
			DurationSeconds: defaultDurationSeconds,
			MinConfidence:   defaultMinConfidence,
		},
		verdicts: verdicts,
	}
}

func (s *Service) OnFinished(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.onFinishedHooks = append(s.onFinishedHooks, hook)
}

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings
}

func (s *Service) UpdateSettings(patch SettingsPatch) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if patch.Enabled != nil {
		s.settings.Enabled = *patch.Enabled
	}

	if patch.DurationSeconds != nil {
		if *patch.DurationSeconds <= 0 {
			return s.settings, errInvalidDuration
		}

		s.settings.DurationSeconds = *patch.DurationSeconds
	}

	if patch.MinConfidence != nil {
		if *patch.MinConfidence < 0 || *patch.MinConfidence > 1 {
			return s.settings, errInvalidConfidence
		}

		s.settings.MinConfidence = *patch.MinConfidence
	}

	return s.settings, nil
}

// GetCurrentPublic отдаёт состояние голосования для панели.
// Фронт ждёт «голый» объект или null.
func (s *Service) GetCurrentPublic() *Public {
	s.mu.Lock()
	defer s.mu.Unlock()

	j := s.currentForLocked(time.Now())

	if j == nil {
		return nil
	}

	total := j.votes.Yea + j.votes.Nei

	return &Public{
		ID:        j.id,
		Status:    j.status,
		Message:   j.message,
		Votes:     j.votes,
		Total:     total,
		Approval:  approvalRate(j.votes.Yea, total),
		Verdict:   s.verdictLocked(j.id),
		StartedAt: j.startedAt.UTC().Format(time.RFC3339Nano),
		EndsAt:    j.endsAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *Service) GetLastResult() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastResult
}

// Start открывает голосование по совету; nil, если уже идёт.
func (s *Service) Start(message ChatJuryMessage) *jury {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	if current := s.currentForLocked(now); current != nil && current.status == statusVoting {
		return nil
	}

	s.clearTimerLocked()

	duration := time.Duration(s.settings.DurationSeconds) * time.Second

	s.startedSequence++
	j := &jury{
		id:        newID(s.startedSequence, now),
		message:   message,
		status:    statusVoting,
		voters:    make(map[string]struct{}),
		startedAt: now,
		endsAt:    now.Add(duration),
	}

	s.current = j
	s.finishTimer = time.AfterFunc(duration, s.finish)

	return j
}

// Vote принимает голос (один на пользователя).
func (s *Service) Vote(userID string, yea bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	j := s.currentForLocked(time.Now())

	if j == nil || j.status != statusVoting || !s.settings.Enabled {
		return false
	}

	if _, voted := j.voters[userID]; voted {
		return false
	}

	j.voters[userID] = struct{}{}

	if yea {
		j.votes.Yea++
	} else {
		j.votes.Nei++
	}

	return true
}

// Finish закрывает голосование досрочно (кнопка «Стоп»).
func (s *Service) Finish() {
	s.finish()
}

// finish закрывает голосование и запускает генерацию вердикта.
func (s *Service) finish() {
	s.mu.Lock()

	j := s.current

	if j == nil || j.status == statusFinished {
		s.mu.Unlock()

		return
	}

	s.clearTimerLocked()

	j.status = statusFinished

	total := j.votes.Yea + j.votes.Nei
	result := &Result{
		ID:        j.id,
		Message:   j.message,
		Status:    statusFinished,
		Yea:       j.votes.Yea,
		Nei:       j.votes.Nei,
		Total:     total,
		Approval:  approvalRate(j.votes.Yea, total),
		StartedAt: j.startedAt.UTC().Format(time.RFC3339Nano),
		EndsAt:    j.endsAt.UTC().Format(time.RFC3339Nano),
	}

	s.lastResult = result
	s.verdictQueryID++
	queryID := s.verdictQueryID

	hooks := append([]func(){}, s.onFinishedHooks...)
	s.mu.Unlock()

	go s.generateVerdict(queryID, result)

	for _, hook := range hooks {
		hook()
	}
}

func (s *Service) generateVerdict(queryID int, result *Result) {
	ctx, cancel := contextWithTimeout(30 * time.Second)
	defer cancel()

	verdict, err := s.verdicts.Generate(ctx, llm.VerdictInput{
		Message:      result.Message.Text,
		Author:       result.Message.Author.DisplayName,
		Yea:          result.Yea,
		Nei:          result.Nei,
		ApprovalRate: result.Approval,
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil || queryID != s.verdictQueryID {
		return
	}

	if s.lastResult == nil || s.lastResult.ID != result.ID {
		return
	}

	text := verdict.Text
	s.lastResult.Verdict = &text
}

func (s *Service) currentForLocked(now time.Time) *jury {
	j := s.current

	if j == nil {
		return nil
	}

	if j.status == statusVoting && !now.Before(j.endsAt) {
		s.mu.Unlock()
		s.finish()
		s.mu.Lock()

		j = s.current
	}

	return j
}

func (s *Service) verdictLocked(juryID string) *string {
	if s.lastResult == nil || s.lastResult.ID != juryID {
		return nil
	}

	return s.lastResult.Verdict
}

func (s *Service) clearTimerLocked() {
	if s.finishTimer != nil {
		s.finishTimer.Stop()
		s.finishTimer = nil
	}
}

func newID(sequence int, now time.Time) string {
	buf := make([]byte, 8)

	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("jury-%d-%d", sequence, now.UnixNano())
	}

	return hex.EncodeToString(buf)
}

func contextWithTimeout(duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), duration)
}

// classifyWithTimeout выполняет классификацию с таймаутом и освобождает контекст.
func (s *Scheduler) classifyWithTimeout(text string) (llm.Classification, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := s.classifier.Classify(ctx, text)
	if err != nil {
		return heuristicClassification(text), nil
	}

	return result, nil
}

func heuristicClassification(text string) llm.Classification {
	lower := strings.ToLower(text)
	markers := []string{
		"попробуй", "советую", "лучше", "надо", "нужно", "сделай", "поставь",
		"включи", "выключи", "не забудь", "рекоменд", "стоит", "можно",
		"try", "should", "use ", "buy ", "pick ", "choose ", "avoid", "don't",
	}

	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return llm.Classification{IsAdvice: true, Confidence: 0.85}
		}
	}

	return llm.Classification{}
}

func approvalRate(yea, total int) float64 {
	if total == 0 {
		return 0
	}

	return float64(yea) / float64(total)
}

/* ── Планировщик ──────────────────────────────────────────────────── */

// Scheduler слушает чат: голоса, классификация советов, автозапуск.
type Scheduler struct {
	service    *Service
	classifier llm.Classifier
	triggers   *triggers.Service

	mu         sync.Mutex
	candidates map[string]candidate
	used       map[string]struct{}
}

type candidate struct {
	message    twitch.TwitchChatMessage
	confidence float64
}

func NewScheduler(service *Service, classifier llm.Classifier, chatService *chat.Service) *Scheduler {
	if classifier == nil {
		classifier = llm.DisabledClassifier{}
	}

	scheduler := &Scheduler{
		service:    service,
		classifier: classifier,
		triggers:   triggers.New(nil),
		candidates: make(map[string]candidate),
		used:       make(map[string]struct{}),
	}

	service.OnFinished(func() {
		scheduler.tryStart()
	})

	chatService.Subscribe(scheduler.HandleMessage)

	return scheduler
}

// SetTriggers подключает настраиваемые стримером команды голосования.
func (s *Scheduler) SetTriggers(service *triggers.Service) {
	s.triggers = service
}

// HandleMessage — точка входа из шины чата (не блокирует приём).
func (s *Scheduler) HandleMessage(message twitch.TwitchChatMessage) {
	go s.processMessage(message)
}

func voteIntent(text string) (yea, nei bool) {
	lower := strings.ToLower(strings.TrimSpace(text))
	token := strings.Trim(lower, "!?.,;:()[]{}\"'«»")

	if token == "" || strings.ContainsAny(token, " \t\n") {
		return false, false
	}

	switch token {
	case "ya", "yea", "yeah", "yes", "yay", "да", "за", "+", "+1", "✅", "✔":
		return true, false
	case "nei", "ney", "no", "nope", "нет", "против", "-", "-1", "❌", "✖":
		return false, true
	}

	return false, false
}

func emoteVoteIntent(emotes []twitch.ChatEmote) (yea, nei bool) {
	for _, emote := range emotes {
		switch strings.ToLower(strings.TrimSpace(emote.Name)) {
		case "yea", "yeah", "yay", "ya":
			return true, false
		case "nei", "ney", "no", "nope":
			return false, true
		}
	}

	return false, false
}

func (s *Scheduler) processMessage(message twitch.TwitchChatMessage) {
	text := strings.TrimSpace(message.Text)

	if text == "" {
		return
	}

	if action, _, ok := s.triggers.Match(text); ok {
		if action == "jury.yea" {
			s.service.Vote(message.Author.ID, true)

			return
		}

		if action == "jury.nei" {
			s.service.Vote(message.Author.ID, false)

			return
		}
	}

	if yea, nei := emoteVoteIntent(message.Emotes); yea != nei {
		s.service.Vote(message.Author.ID, yea)

		return
	}

	// Совместимость: голоса работают даже если триггеры ещё не перенастроены.
	if yea, nei := voteIntent(text); yea != nei {
		s.service.Vote(message.Author.ID, yea)

		return
	}

	s.mu.Lock()

	if _, used := s.used[message.MessageID]; used {
		s.mu.Unlock()

		return
	}

	s.mu.Unlock()

	classification, err := s.classifyWithTimeout(text)
	if err != nil {
		return
	}

	settings := s.service.GetSettings()

	if !classification.IsAdvice || classification.Confidence < settings.MinConfidence {
		return
	}

	s.mu.Lock()
	s.candidates[message.MessageID] = candidate{message: message, confidence: classification.Confidence}
	s.mu.Unlock()

	s.tryStart()
}

func (s *Scheduler) tryStart() {
	settings := s.service.GetSettings()

	if !settings.Enabled {
		return
	}

	s.mu.Lock()

	ids := make([]string, 0, len(s.candidates))

	for id := range s.candidates {
		ids = append(ids, id)
	}

	if len(ids) == 0 {
		s.mu.Unlock()

		return
	}

	sort.Strings(ids)
	selected := s.candidates[ids[len(ids)/2]]
	delete(s.candidates, selected.message.MessageID)
	s.used[selected.message.MessageID] = struct{}{}
	s.mu.Unlock()

	message := ChatJuryMessage{
		ID: selected.message.MessageID,
		Author: Author{
			ID:          selected.message.Author.ID,
			Username:    selected.message.Author.Login,
			DisplayName: selected.message.Author.DisplayName,
		},
		Text:             selected.message.Text,
		Created:          selected.message.SentAt,
		AdviceConfidence: selected.confidence,
	}

	s.service.Start(message)
}

/* ── Роуты ────────────────────────────────────────────────────────── */

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/chat-jury/current", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, service.GetCurrentPublic())
	})

	srv.ServeEndpoint("/rgg-sync/chat-jury/result", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, service.GetLastResult())
	})

	srv.ServeEndpoint("/rgg-sync/chat-jury/start", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Text        string  `json:"text"`
			Author      string  `json:"author"`
			DisplayName string  `json:"displayName"`
			Confidence  float64 `json:"confidence"`
		}

		_ = json.NewDecoder(request.Body).Decode(&body)

		if strings.TrimSpace(body.Text) == "" {
			body.Text = "Стоит ли послушать этот совет?"
		}

		if body.Author == "" {
			body.Author = "streamer"
		}

		if body.DisplayName == "" {
			body.DisplayName = body.Author
		}

		if body.Confidence <= 0 {
			body.Confidence = 0.9
		}

		service.Start(ChatJuryMessage{
			ID:               fmt.Sprintf("manual-%d", time.Now().UnixNano()),
			Author:           Author{ID: body.Author, Username: body.Author, DisplayName: body.DisplayName},
			Text:             body.Text,
			Created:          time.Now().UTC().Format(time.RFC3339),
			AdviceConfidence: body.Confidence,
		})

		writeJSON(response, http.StatusOK, service.GetCurrentPublic())
	})

	srv.ServeEndpoint("/rgg-sync/chat-jury/finish", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		service.Finish()
		writeJSON(response, http.StatusOK, service.GetLastResult())
	})

	srv.ServeEndpoint("/rgg-sync/chat-jury/settings", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch || request.Method == http.MethodPost {
			var patch SettingsPatch

			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			settings, err := service.UpdateSettings(patch)
			if err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, settings)

			return
		}

		writeJSON(response, http.StatusOK, service.GetSettings())
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
