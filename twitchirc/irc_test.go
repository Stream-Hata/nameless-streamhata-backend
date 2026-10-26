package twitchirc

import (
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

var broadcaster = twitch.ChatUser{ID: "1", Login: "kira_plays", DisplayName: "Kira"}

func TestParsePrivmsgWithTags(t *testing.T) {
	line := "@badge-info=;badges=;client-nonce=abc;id=bb1c7125;user-id=998;display-name=Namel3ss;" +
		"tmi-sent-ts=1700000000123 :nameless!nameless@nameless.tmi.twitch.tv PRIVMSG #kira_plays :спрячьтесь за бочкой"

	message, ok := ParsePrivmsg(line, broadcaster)

	if !ok {
		t.Fatal("expected message to be parsed")
	}

	if message.MessageID != "bb1c7125" {
		t.Fatalf("unexpected id: %q", message.MessageID)
	}

	if message.Author.ID != "998" || message.Author.Login != "nameless" {
		t.Fatalf("unexpected author: %+v", message.Author)
	}

	if message.Author.DisplayName != "Namel3ss" {
		t.Fatalf("unexpected display name: %q", message.Author.DisplayName)
	}

	if message.Text != "спрячьтесь за бочкой" {
		t.Fatalf("unexpected text: %q", message.Text)
	}

	if message.Broadcaster.ID != "1" {
		t.Fatalf("unexpected broadcaster: %+v", message.Broadcaster)
	}
}

func TestParsePrivmsgWithoutTags(t *testing.T) {
	line := ":ivan!ivan@ivan.tmi.twitch.tv PRIVMSG #kira_plays :hello"

	message, ok := ParsePrivmsg(line, broadcaster)

	if !ok {
		t.Fatal("expected message to be parsed")
	}

	if message.Author.Login != "ivan" || message.Author.DisplayName != "ivan" {
		t.Fatalf("unexpected author: %+v", message.Author)
	}

	if message.MessageID == "" {
		t.Fatal("expected fallback message id")
	}
}

func TestParsePrivmsgIgnoresOtherCommands(t *testing.T) {
	line := ":tmi.twitch.tv 366 kira_plays #kira_plays :End of /NAMES list"

	if _, ok := ParsePrivmsg(line, broadcaster); ok {
		t.Fatal("expected non-PRIVMSG line to be ignored")
	}
}

func TestParsePrivmsgMultilineText(t *testing.T) {
	line := "@id=x1;user-id=5;display-name=Bob :bob!bob@bob.tmi.twitch.tv PRIVMSG #kira_plays :a : b : c"

	message, ok := ParsePrivmsg(line, broadcaster)

	if !ok || message.Text != "a : b : c" {
		t.Fatalf("expected text with colons, got %+v", message)
	}
}
