package eventsub

import (
	"encoding/json"
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func TestMapFragments(t *testing.T) {
	raw := json.RawMessage(`{
		"broadcaster_user_id": "1",
		"chatter_user_id": "42",
		"chatter_user_login": "n",
		"chatter_user_name": "N",
		"message_id": "m",
		"message": {
			"text": "привет Kappa!",
			"fragments": [
				{"type": "text", "text": "привет "},
				{"type": "emote", "text": "Kappa", "emote": {"id": "25"}},
				{"type": "text", "text": "!"}
			]
		},
		"sent_at": "2026-01-01T00:00:00Z"
	}`)

	message, ok := mapNotification(raw, twitch.ChatUser{ID: "1"})

	if !ok {
		t.Fatal("expected mapped message")
	}

	if len(message.Emotes) != 1 {
		t.Fatalf("expected 1 emote, got %+v", message.Emotes)
	}

	emote := message.Emotes[0]

	if emote.ID != "25" || emote.Name != "Kappa" || emote.Start != 7 || emote.End != 11 {
		t.Fatalf("unexpected emote: %+v", emote)
	}
}
