package chat

import (
	"context"
	"testing"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func message(id, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{MessageID: id, Text: text}
}

func TestReceiveNormalizesTranslit(t *testing.T) {
	service := NewService(10, translatorFunc(func(string) (string, error) {
		return "привет как дела", nil
	}))
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
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		t.Fatalf("command sent for translation: %q", text)
		return "", nil
	}))
	service.Receive(message("m1", "!yea"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "!yea" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestReceiveKeepsEnglish(t *testing.T) {
	service := NewService(10, nil)
	service.Receive(message("m1", "hello how are you"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "hello how are you" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

type translatorFunc func(string) (string, error)

func (translate translatorFunc) Translate(text string) (string, error) {
	return translate(text)
}

func TestReceiveTranslatesOriginalMixedMessage(t *testing.T) {
	const original = "privet pizdec fucking awesome"
	const translated = "привет пиздец охуенно"
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		if text != original {
			t.Fatalf("translator received %q, want %q", text, original)
		}
		return translated, nil
	}))

	service.Receive(message("m1", original))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != translated {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestAutoTranslationCanBeDisabledPerService(t *testing.T) {
	calls := 0
	service := NewService(10, translatorFunc(func(string) (string, error) {
		calls++
		return "перевод", nil
	}))
	service.SetAutoTranslationEnabled(false)
	service.Receive(message("disabled", "hello there"))
	if calls != 0 {
		t.Fatalf("translator called while auto-translation disabled: %d", calls)
	}
	service.SetAutoTranslationEnabled(true)
	service.Receive(message("enabled", "hello there"))
	if calls != 1 {
		t.Fatalf("translator calls = %d, want 1 after re-enabling", calls)
	}
}

func TestReceiveDeduplicates(t *testing.T) {
	service := NewService(10, nil)
	service.Receive(message("m1", "privet"))
	service.Receive(message("m1", "privet"))

	if recent := service.Recent(10); len(recent) != 1 {
		t.Fatalf("expected one message, got %d", len(recent))
	}
}

func TestReceiveCorrectsRussianCommas(t *testing.T) {
	const original = "я думаю что он придёт"
	const corrected = "я думаю, что он придёт"
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		if text != original {
			t.Fatalf("translator received %q, want %q", text, original)
		}
		return corrected, nil
	}))

	service.Receive(message("m1", original))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != corrected {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestReceiveKeepsOriginalWhenTranslatorEchoesInput(t *testing.T) {
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		return text, nil
	}))

	service.Receive(message("m1", "pft,bcm"))

	if recent := service.Recent(10); len(recent) != 1 || recent[0].Text != "pft,bcm" {
		t.Fatalf("unexpected buffer: %+v", recent)
	}
}

func TestRecentChannelFiltersBeforeApplyingLimit(t *testing.T) {
	service := NewService(10, nil)
	service.ReceiveOwn(twitch.TwitchChatMessage{
		MessageID: "m1", Text: "first", Broadcaster: twitch.ChatUser{ID: "100"},
	})
	service.ReceiveOwn(twitch.TwitchChatMessage{
		MessageID: "m2", Text: "second", Broadcaster: twitch.ChatUser{ID: "200"},
	})

	recent := service.RecentChannel(1, "100")
	if len(recent) != 1 || recent[0].MessageID != "m1" {
		t.Fatalf("unexpected channel history: %+v", recent)
	}
}

type writerFunc func(string) error

func (write writerFunc) Say(text string) error {
	return write(text)
}

type testReplyWriter struct {
	writerFunc
	reply func(string, string) error
}

func (writer testReplyWriter) Reply(parentID, text string) error {
	return writer.reply(parentID, text)
}

func TestReceiveRepliesToOriginalTwitchMessage(t *testing.T) {
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		if text != "t,extt ujdybot" {
			t.Fatalf("wrong-layout phrase was modified before translation: %q", text)
		}
		return "ебучее говнище", nil
	}))
	replied := false
	service.SetWriter(testReplyWriter{
		writerFunc: func(text string) error {
			t.Fatalf("expected Twitch reply, got standalone message %q", text)
			return nil
		},
		reply: func(parentID, text string) error {
			replied = true
			if parentID != "original-id" || text != "Перевод сообщения: ебучее говнище" {
				t.Fatalf("unexpected reply to %q: %q", parentID, text)
			}
			return nil
		},
	})
	incoming := message("original-id", "t,extt ujdybot")
	incoming.Author.Login = "grep_ban"
	service.Receive(incoming)
	if !replied {
		t.Fatal("translation was not sent")
	}
}

func TestRepeatedTextUsesOneTranslationAndRepliesToBothMessages(t *testing.T) {
	calls := 0
	service := NewService(10, newCachedTranslator(context.Background(), func(_ context.Context, text string) (string, error) {
		calls++
		return "говно", nil
	}))
	var replies []string
	service.SetWriter(testReplyWriter{
		writerFunc: func(text string) error {
			t.Fatalf("unexpected standalone message: %q", text)
			return nil
		},
		reply: func(parentID, text string) error {
			if text != "Перевод сообщения: говно" {
				t.Fatalf("unexpected translation: %q", text)
			}
			replies = append(replies, parentID)
			return nil
		},
	})

	service.Receive(message("first", "ujdyj"))
	service.Receive(message("first", "ujdyj"))
	service.Receive(message("second", "ujdyj"))

	if calls != 1 || len(replies) != 2 || replies[0] != "first" || replies[1] != "second" {
		t.Fatalf("got %d model calls and reply IDs %v", calls, replies)
	}
}

func TestReceiveLabelsTranslationSentToTwitch(t *testing.T) {
	service := NewService(10, translatorFunc(func(text string) (string, error) {
		if text == "pft,bcm" {
			return "заебись", nil
		}
		return text, nil
	}))
	var sent []string
	service.SetWriter(writerFunc(func(text string) error {
		sent = append(sent, text)
		return nil
	}))
	incoming := message("m1", "pft,bcm")
	incoming.Author.Login = "grep_ban"
	service.Receive(incoming)
	service.Receive(message("m2", "обычный текст"))

	if len(sent) != 1 || sent[0] != "Перевод сообщения от @grep_ban: заебись" {
		t.Fatalf("unexpected outgoing chat messages: %q", sent)
	}
	if recent := service.Recent(10); recent[0].Text != "заебись" || recent[0].Author.Login != "grep_ban" {
		t.Fatalf("unexpected history: %+v", recent)
	}
}
