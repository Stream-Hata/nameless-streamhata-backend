package chat

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Sink — обработчик входящих сообщений чата.
type Sink func(twitch.TwitchChatMessage)

// Writer — отправка сообщений в чат (команды, таймеры, объявления).
type Writer interface {
	Say(text string) error
}

type replyWriter interface {
	Reply(parentID, text string) error
}

const translationLabel = "Перевод сообщения"

func IsTranslationReply(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "@") {
		_, rest, ok := strings.Cut(text, " ")
		if ok {
			text = strings.TrimLeft(rest, " ")
		}
	}
	return strings.HasPrefix(text, translationLabel+": ") || strings.HasPrefix(text, translationLabel+" от @")
}

// Service — кольцевой буфер чата с дедупликацией и подписчиками.
type Service struct {
	mu                sync.Mutex
	messages          []twitch.TwitchChatMessage
	seen              map[string]struct{}
	channels          map[string]struct{}
	original          string
	max               int
	sinks             map[int]Sink
	translator        LLMTranslator
	translationBypass func(twitch.TwitchChatMessage) bool
	writer            Writer
	nextID            int
	observer          func(ChatEvent)
	autoTranslation   bool
	received          atomic.Uint64
	deduplicated      atomic.Uint64
	translated        atomic.Uint64
	translationFailed atomic.Uint64
	unchanged         atomic.Uint64
	skipped           atomic.Uint64
}

// ChatEvent keeps the original and normalized text for private diagnostics.
type ChatEvent struct {
	MessageID    string `json:"message_id"`
	ChannelID    string `json:"channel_id"`
	ChannelLogin string `json:"channel_login"`
	AuthorID     string `json:"author_id"`
	AuthorLogin  string `json:"author_login"`
	Original     string `json:"original_text"`
	Text         string `json:"text"`
	SentAt       string `json:"sent_at"`
	Outcome      string `json:"outcome"`
}

type MetricsSnapshot struct {
	Received          uint64
	Deduplicated      uint64
	Translated        uint64
	TranslationFailed uint64
	Unchanged         uint64
	Skipped           uint64
}

func NewService(maxMessages int, translator LLMTranslator) *Service {
	if maxMessages <= 0 {
		maxMessages = 500
	}

	return &Service{
		translator:      translator,
		autoTranslation: true,
		messages:        make([]twitch.TwitchChatMessage, 0, maxMessages),
		seen:            make(map[string]struct{}),
		channels:        make(map[string]struct{}),
		max:             maxMessages,
		sinks:           make(map[int]Sink),
	}
}

func (service *Service) AutoTranslationEnabled() bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.autoTranslation
}

func (service *Service) SetAutoTranslationEnabled(enabled bool) {
	service.mu.Lock()
	service.autoTranslation = enabled
	service.mu.Unlock()
}

func (service *Service) SetWriter(writer Writer) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.writer = writer
}

// SetObserver installs a non-blocking diagnostic sink for accepted chat messages.
func (service *Service) SetObserver(observer func(ChatEvent)) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.observer = observer
}

func (service *Service) Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		Received:          service.received.Load(),
		Deduplicated:      service.deduplicated.Load(),
		Translated:        service.translated.Load(),
		TranslationFailed: service.translationFailed.Load(),
		Unchanged:         service.unchanged.Load(),
		Skipped:           service.skipped.Load(),
	}
}

// SetTranslationBypass installs a channel-specific handler for messages that
// should be handled as structured input instead of being translated.
func (service *Service) SetTranslationBypass(handler func(twitch.TwitchChatMessage) bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.translationBypass = handler
}

// SetProtectedTerms configures catalog terms that must retain their spelling
// during chat translation, such as game titles and console platforms.
func (service *Service) SetProtectedTerms(terms []string) {
	if translator, ok := service.translator.(interface{ SetProtectedTerms([]string) }); ok {
		translator.SetProtectedTerms(terms)
	}
}

// Receive добавляет сообщение в буфер и раздаёт подписчикам.
func (s *Service) Receive(message twitch.TwitchChatMessage) {
	s.receive(message, true)
}

// ReceiveOwn сохраняет сообщение, отправленное приложением.
func (s *Service) ReceiveOwn(message twitch.TwitchChatMessage) {
	s.receive(message, false)
}

func (s *Service) receive(message twitch.TwitchChatMessage, translate bool) {
	s.mu.Lock()

	if message.MessageID != "" {
		if _, ok := s.seen[message.MessageID]; ok {
			s.deduplicated.Add(1)
			observer := s.observer
			s.mu.Unlock()
			if observer != nil {
				observer(ChatEvent{
					MessageID: message.MessageID, ChannelID: message.Broadcaster.ID, ChannelLogin: message.Broadcaster.Login,
					AuthorID: message.Author.ID, AuthorLogin: message.Author.Login, Original: message.Text,
					Text: message.Text, SentAt: message.SentAt, Outcome: "duplicate",
				})
			}

			return
		}

		s.seen[message.MessageID] = struct{}{}
	}
	s.received.Add(1)

	writer := s.writer
	translationBypass := s.translationBypass
	autoTranslation := s.autoTranslation
	observer := s.observer
	s.mu.Unlock()

	original := message.Text
	text := strings.TrimSpace(original)
	outcome := "skipped"
	bypassed := translate && translationBypass != nil && translationBypass(message)
	eligible := translate && autoTranslation && !bypassed && s.translator != nil && shouldTranslateMessage(message) && !strings.HasPrefix(text, "!") && !strings.HasPrefix(text, "/")
	if eligible && messageNeedsTranslation(s.translator, message) {
		outcome = "unchanged"
		var translated string
		var err error
		if messageTranslator, ok := s.translator.(interface {
			TranslateChatMessage(twitch.TwitchChatMessage) (string, error)
		}); ok {
			translated, err = messageTranslator.TranslateChatMessage(message)
		} else {
			translated, err = s.translator.Translate(original)
		}

		if err != nil {
			outcome = "translation_failed"
			s.translationFailed.Add(1)
			log.Printf("[Chat] translation failed: %v", err)
		} else if translated = strings.TrimSpace(translated); translated != text && validTranslation(translated) {
			message.Text = translated
			outcome = "translated"
			s.translated.Add(1)
		} else {
			s.unchanged.Add(1)
		}
	}
	if outcome == "skipped" {
		s.skipped.Add(1)
	}
	if message.Text != original {
		// Позиции эмоутов относятся к исходному тексту.
		message.Emotes = nil
	}
	log.Printf("[Chat] processed channel=%q author=%q id=%q input_len=%d output_len=%d", message.Broadcaster.Login, message.Author.Login, message.MessageID, len([]rune(original)), len([]rune(message.Text)))
	if observer != nil {
		observer(ChatEvent{
			MessageID: message.MessageID, ChannelID: message.Broadcaster.ID, ChannelLogin: message.Broadcaster.Login,
			AuthorID: message.Author.ID, AuthorLogin: message.Author.Login, Original: original,
			Text: message.Text, SentAt: message.SentAt, Outcome: outcome,
		})
	}

	s.mu.Lock()
	s.messages = append(s.messages, message)

	if len(s.messages) > s.max {
		drop := len(s.messages) - s.max

		for _, old := range s.messages[:drop] {
			delete(s.seen, old.MessageID)
		}

		s.messages = append(s.messages[:0], s.messages[drop:]...)
	}

	sinks := make([]Sink, 0, len(s.sinks))

	for _, sink := range s.sinks {
		sinks = append(sinks, sink)
	}

	s.mu.Unlock()

	for _, sink := range sinks {
		sink(message)
	}

	if writer != nil && message.Text != original && !strings.ContainsAny(message.Text, "\r\n") {
		label := translationLabel + ": "
		reply, canReply := writer.(replyWriter)
		canReply = canReply && message.MessageID != ""
		if login := message.Author.Login; !canReply && login != "" {
			label = translationLabel + " от @" + login + ": "
		}
		outgoing := []rune(label + message.Text)
		const maxChatLength = 500
		if len(outgoing) > maxChatLength {
			outgoing[maxChatLength-1] = '…'
			outgoing = outgoing[:maxChatLength]
		}
		var err error
		if canReply {
			err = reply.Reply(message.MessageID, string(outgoing))
		} else {
			err = writer.Say(string(outgoing))
		}
		if err != nil {
			log.Printf("[Chat] sending translation failed: %v", err)
		}
	}
}

func messageNeedsTranslation(translator LLMTranslator, message twitch.TwitchChatMessage) bool {
	if checker, ok := translator.(interface {
		NeedsTranslation(twitch.TwitchChatMessage) bool
	}); ok {
		return checker.NeedsTranslation(message)
	}
	text := textWithoutEmotes(message)
	return hasForeignLetters(text) || needsRussianPunctuationCorrection(text)
}

func textWithoutEmotes(message twitch.TwitchChatMessage) string {
	runes := []rune(message.Text)
	for _, emote := range message.Emotes {
		if emote.Start < 0 || emote.End < emote.Start || emote.End >= len(runes) {
			continue
		}
		for i := emote.Start; i <= emote.End; i++ {
			runes[i] = ' '
		}
	}
	return string(runes)
}

func hasForeignLetters(text string) bool {
	return strings.ContainsFunc(text, func(character rune) bool {
		if !unicode.IsLetter(character) {
			return false
		}
		if !unicode.Is(unicode.Cyrillic, character) {
			return true
		}
		return strings.ContainsRune("іїєґўљњћђџјѓќѕІЇЄҐЎЉЊЋЂЏЈЃЌЅ", character)
	})
}

// needsRussianPunctuationCorrection avoids sending ordinary Russian chat to the
// model, but allows likely unpunctuated clause boundaries through for repair.
func needsRussianPunctuationCorrection(text string) bool {
	if hasForeignLetters(text) || strings.Contains(text, ",") {
		return false
	}
	text = " " + strings.ToLower(strings.Join(strings.Fields(text), " ")) + " "
	for _, phrase := range []string{
		" думаю что ", " считаю что ", " полагаю что ", " надеюсь что ",
		" кажется что ", " мне кажется что ", " знаю что ", " понял что ",
		" он сказал что ", " она сказала что ", " говорят что ",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return strings.Contains(text, " если ") || strings.Contains(text, " когда ") || strings.Contains(text, " хотя ")
}

// shouldTranslateMessage prevents emoji-only and Twitch-emote-only messages
// from being sent to the language model. Emote offsets are rune-based, like
// Twitch's IRC tags.
func shouldTranslateMessage(message twitch.TwitchChatMessage) bool {
	runes := []rune(message.Text)
	if len(runes) == 0 {
		return false
	}
	emote := make([]bool, len(runes))
	for _, item := range message.Emotes {
		if item.Start < 0 || item.End < item.Start || item.End >= len(runes) {
			continue
		}
		for i := item.Start; i <= item.End; i++ {
			emote[i] = true
		}
	}
	for i, r := range runes {
		if !emote[i] && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// Subscribe регистрирует обработчик; возвращает функцию отписки.
func (s *Service) Subscribe(sink Sink) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextID
	s.nextID++
	s.sinks[id] = sink

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		delete(s.sinks, id)
	}
}

// SetChannel добавляет канал в активный набор.
// При переподключении к другому каналу оба остаются видимыми.
func (s *Service) SetChannel(channelID string) {
	if channelID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.original == "" {
		s.original = channelID
	}

	s.channels[channelID] = struct{}{}
}

// Recent возвращает последние limit сообщений (в хронологическом порядке).
func (s *Service) Recent(limit int) []twitch.TwitchChatMessage {
	return s.RecentMode(limit, "all")
}

// RecentMode: mode="original" оставляет только первый подключённый канал.
func (s *Service) RecentMode(limit int, mode string) []twitch.TwitchChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	channelID := ""
	if mode == "original" && s.original != "" {
		channelID = s.original
	}
	return s.recent(limit, channelID)
}

func (s *Service) RecentChannel(limit int, channelID string) []twitch.TwitchChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recent(limit, channelID)
}

func (s *Service) recent(limit int, channelID string) []twitch.TwitchChatMessage {
	filtered := s.messages
	if channelID != "" {
		filtered = make([]twitch.TwitchChatMessage, 0, len(s.messages))
		for _, message := range s.messages {
			if message.Broadcaster.ID == channelID {
				filtered = append(filtered, message)
			}
		}
	} else if len(s.channels) > 0 {
		filtered = make([]twitch.TwitchChatMessage, 0, len(s.messages))

		for _, message := range s.messages {
			if _, ok := s.channels[message.Broadcaster.ID]; ok {
				filtered = append(filtered, message)
			}
		}
	}

	if limit <= 0 || limit > len(filtered) {
		limit = len(filtered)
	}

	result := make([]twitch.TwitchChatMessage, limit)
	copy(result, filtered[len(filtered)-limit:])

	return result
}
