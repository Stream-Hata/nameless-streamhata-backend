package chat

import (
	"sync"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Sink — обработчик входящих сообщений чата.
type Sink func(twitch.TwitchChatMessage)

// Writer — отправка сообщений в чат (команды, таймеры, объявления).
type Writer interface {
	Say(text string) error
}

// Service — кольцевой буфер чата с дедупликацией и подписчиками.
type Service struct {
	mu        sync.Mutex
	messages  []twitch.TwitchChatMessage
	seen      map[string]struct{}
	channelID string
	max       int
	sinks     map[int]Sink
	nextID    int
}

func NewService(maxMessages int) *Service {
	if maxMessages <= 0 {
		maxMessages = 500
	}

	return &Service{
		messages: make([]twitch.TwitchChatMessage, 0, maxMessages),
		seen:     make(map[string]struct{}),
		max:      maxMessages,
		sinks:    make(map[int]Sink),
	}
}

// Receive добавляет сообщение в буфер и раздаёт подписчикам.
func (s *Service) Receive(message twitch.TwitchChatMessage) {
	s.mu.Lock()

	if message.MessageID != "" {
		if _, ok := s.seen[message.MessageID]; ok {
			s.mu.Unlock()

			return
		}

		s.seen[message.MessageID] = struct{}{}
	}

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

// SetChannel ограничивает выдачу сообщений текущим каналом.
func (s *Service) SetChannel(channelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.channelID = channelID
}

// Recent возвращает последние limit сообщений (в хронологическом порядке).
func (s *Service) Recent(limit int) []twitch.TwitchChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.messages

	if s.channelID != "" {
		filtered = make([]twitch.TwitchChatMessage, 0, len(s.messages))

		for _, message := range s.messages {
			if message.Broadcaster.ID == s.channelID {
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
