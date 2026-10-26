package eventsub

import (
	"encoding/json"
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func TestMapNotification(t *testing.T) {
	raw := json.RawMessage(`{
		"broadcaster_user_id": "1",
		"broadcaster_user_login": "kira_plays",
		"broadcaster_user_name": "Kira",
		"chatter_user_id": "42",
		"chatter_user_login": "nameless",
		"chatter_user_name": "Namel3ss",
		"message_id": "msg-1",
		"message": {"text": "спрячьтесь за бочкой"},
		"sent_at": "2026-05-12T18:00:01.123Z"
	}`)

	message, ok := mapNotification(raw, twitch.ChatUser{ID: "1", Login: "kira_plays"})

	if !ok {
		t.Fatal("expected notification to be mapped")
	}

	if message.MessageID != "msg-1" || message.Text != "спрячьтесь за бочкой" {
		t.Fatalf("unexpected message: %+v", message)
	}

	if message.Author.Login != "nameless" || message.Author.DisplayName != "Namel3ss" {
		t.Fatalf("unexpected author: %+v", message.Author)
	}

	if message.SentAt != "2026-05-12T18:00:01.123Z" {
		t.Fatalf("unexpected sent_at: %q", message.SentAt)
	}
}

func TestMapNotificationRejectsEmpty(t *testing.T) {
	if _, ok := mapNotification(json.RawMessage(`{}`), twitch.ChatUser{}); ok {
		t.Fatal("expected empty event to be rejected")
	}

	if _, ok := mapNotification(json.RawMessage(`not-json`), twitch.ChatUser{}); ok {
		t.Fatal("expected invalid json to be rejected")
	}
}
