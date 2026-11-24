package chat

import (
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func message(id, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{MessageID: id, Text: text}
}

func TestReceiveNormalizesTranslit(t *testing.T) {
	service := NewService(10)
	received := make([]twitch.TwitchChatMessage, 0, 1)

	service.Subscribe(func(m twitch.TwitchChatMessage) {
		received = append(received, m)
	})

	service.Receive(message("m1", "privet kak dela"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "привет как дела" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}

	if len(received) != 1 || received[0].Text != "привет как дела" {
		t.Fatalf("unexpected sinks: %+v", received)
	}
}

func TestReceiveSkipsCommands(t *testing.T) {
	service := NewService(10)
	service.Receive(message("m1", "!yea"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "!yea" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestReceiveKeepsEnglish(t *testing.T) {
	service := NewService(10)
	service.Receive(message("m1", "hello how are you"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "hello how are you" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestReceiveDeduplicates(t *testing.T) {
	service := NewService(10)
	service.Receive(message("m1", "privet"))
	service.Receive(message("m1", "privet"))

	if recent := service.Recent(10); len(recent) != 1 {
		t.Fatalf("expected one message, got %d", len(recent))
	}
}
