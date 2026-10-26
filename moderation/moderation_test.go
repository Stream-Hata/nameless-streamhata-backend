package moderation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type fakeActions struct {
	deleted  []string
	timeouts []string
	bans     []string
}

func (f *fakeActions) DeleteMessage(_ context.Context, messageID string) error {
	f.deleted = append(f.deleted, messageID)

	return nil
}

func (f *fakeActions) BanUser(_ context.Context, userID, _ string) error {
	f.bans = append(f.bans, userID)

	return nil
}

func (f *fakeActions) TimeoutUser(_ context.Context, userID string, _ int, _ string) error {
	f.timeouts = append(f.timeouts, userID)

	return nil
}

type fakeWriter struct {
	messages []string
}

func (f *fakeWriter) Say(text string) error {
	f.messages = append(f.messages, text)

	return nil
}

func chatMessage(authorID, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID:   "m-" + text,
		Broadcaster: twitch.ChatUser{ID: "channel"},
		Author:      twitch.ChatUser{ID: authorID, Login: "user" + authorID, DisplayName: "User" + authorID},
		Text:        text,
	}
}

func TestBannedWordsDelete(t *testing.T) {
	actions := &fakeActions{}
	service := New(nil, actions)
	service.SetFilters(Filters{BannedWords: []string{"лох"}, Action: "delete", TimeoutSeconds: 60})

	service.HandleMessage(context.Background(), chatMessage("1", "ты лох"))

	if len(actions.deleted) != 1 {
		t.Fatalf("expected delete, got %+v", actions)
	}
}

func TestLinksBlocked(t *testing.T) {
	actions := &fakeActions{}
	service := New(nil, actions)
	service.SetFilters(Filters{BlockLinks: true})

	service.HandleMessage(context.Background(), chatMessage("1", "смотри https://youtu.be/abc123"))
	service.HandleMessage(context.Background(), chatMessage("1", "просто текст без ссылок"))

	if len(actions.deleted) != 1 {
		t.Fatalf("expected only link message to be deleted, got %+v", actions.deleted)
	}
}

func TestCapsDetection(t *testing.T) {
	service := New(nil, nil)
	service.SetFilters(Filters{CapsEnabled: true, CapsPercent: 70, CapsMinLength: 10})

	if reason := service.reason(chatMessage("1", "ЭТО ОЧЕНЬ ГРОМКОЕ СООБЩЕНИЕ")); reason != "капс" {
		t.Fatalf("expected caps reason, got %q", reason)
	}

	if reason := service.reason(chatMessage("1", "Это нормальное сообщение")); reason != "" {
		t.Fatalf("expected no reason, got %q", reason)
	}

	if reason := service.reason(chatMessage("1", "ОК")); reason != "" {
		t.Fatalf("short messages must be ignored, got %q", reason)
	}
}

func TestEmojiSpam(t *testing.T) {
	service := New(nil, nil)
	service.SetFilters(Filters{EmojiEnabled: true, EmojiMax: 3})

	if reason := service.reason(chatMessage("1", "😀😀😀😀😀 привет")); reason != "спам эмодзи" {
		t.Fatalf("expected emoji reason, got %q", reason)
	}
}

func TestRepeatDetection(t *testing.T) {
	service := New(nil, nil)
	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }
	service.SetFilters(Filters{RepeatEnabled: true, RepeatMax: 3, RepeatWindowSeconds: 30})

	for i := 0; i < 3; i++ {
		if reason := service.reason(chatMessage("1", "одно и то же")); reason != "" {
			t.Fatalf("iteration %d must pass, got %q", i, reason)
		}
	}

	if reason := service.reason(chatMessage("1", "одно и то же")); reason != "повтор сообщения" {
		t.Fatalf("expected repeat reason, got %q", reason)
	}

	now = now.Add(time.Minute)

	if reason := service.reason(chatMessage("1", "одно и то же")); reason != "" {
		t.Fatalf("window must expire, got %q", reason)
	}
}

func TestDryRunLogsWithoutActions(t *testing.T) {
	actions := &fakeActions{}
	writer := &fakeWriter{}
	service := New(writer, actions)
	service.SetFilters(Filters{BannedWords: []string{"спам"}, DryRun: true})

	service.HandleMessage(context.Background(), chatMessage("1", "спам спам"))

	if len(actions.deleted) != 0 {
		t.Fatal("dry run must not delete")
	}

	if len(service.Log()) != 1 {
		t.Fatalf("expected log entry, got %+v", service.Log())
	}

	if !strings.Contains(writer.messages[0], "сухой прогон") {
		t.Fatalf("expected dry-run announcement, got %v", writer.messages)
	}
}

func TestBroadcasterExempt(t *testing.T) {
	service := New(nil, nil)
	service.SetFilters(Filters{BannedWords: []string{"слово"}, ExemptModerators: true})

	message := chatMessage("channel", "слово")
	message.Broadcaster = twitch.ChatUser{ID: "channel"}

	if reason := service.reason(message); reason != "" {
		t.Fatalf("broadcaster must be exempt, got %q", reason)
	}
}
