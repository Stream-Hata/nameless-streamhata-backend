package quotes

import (
	"strings"
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type fakeWriter struct {
	messages []string
}

func (f *fakeWriter) Say(text string) error {
	f.messages = append(f.messages, text)

	return nil
}

func message(text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID:   "m1",
		Broadcaster: twitch.ChatUser{ID: "channel"},
		Author:      twitch.ChatUser{ID: "1", Login: "ivan", DisplayName: "Ivan"},
		Text:        text,
	}
}

func TestAddAndRandomQuote(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)

	service.HandleMessage(message("!quote add спрячьтесь за бочкой"))
	service.HandleMessage(message("!quote"))

	if len(writer.messages) != 2 {
		t.Fatalf("expected add + random, got %v", writer.messages)
	}

	if !strings.Contains(writer.messages[0], "#1") {
		t.Fatalf("unexpected add reply: %q", writer.messages[0])
	}

	if !strings.Contains(writer.messages[1], "спрячьтесь за бочкой") {
		t.Fatalf("unexpected quote reply: %q", writer.messages[1])
	}
}

func TestQuoteByNumberAndDelete(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)

	_, _ = service.Add("первая", "Ivan", "Ivan")
	_, _ = service.Add("вторая", "Mary", "Mary")

	service.HandleMessage(message("!quote 2"))

	if !strings.Contains(writer.messages[0], "вторая") {
		t.Fatalf("unexpected quote: %q", writer.messages[0])
	}

	owner := message("!quote del 1")
	owner.Author = twitch.ChatUser{ID: "channel", Login: "kira_plays", DisplayName: "Kira"}
	owner.Broadcaster = twitch.ChatUser{ID: "channel", Login: "kira_plays", DisplayName: "Kira"}
	service.HandleMessage(owner)

	if len(service.List()) != 1 {
		t.Fatalf("expected 1 quote after delete, got %d", len(service.List()))
	}
}

func TestEmptyQuoteRejected(t *testing.T) {
	service := New(nil, nil)

	if _, err := service.Add("   ", "Ivan", "Ivan"); err == nil {
		t.Fatal("expected validation error")
	}
}
