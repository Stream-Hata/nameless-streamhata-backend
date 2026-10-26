package points

import (
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/alerts"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/viewer"
)

type fakeWriter struct {
	messages []string
}

func (f *fakeWriter) Say(text string) error {
	f.messages = append(f.messages, text)

	return nil
}

func chatMessage(authorID, author, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID:   "m-" + text,
		Broadcaster: twitch.ChatUser{ID: "channel", Login: "kira_plays", DisplayName: "Kira"},
		Author:      twitch.ChatUser{ID: authorID, Login: author, DisplayName: author},
		Text:        text,
	}
}

func TestAwardOnMessageWithCooldown(t *testing.T) {
	service := New(nil, nil)
	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }
	service.SetSettings(Settings{Enabled: true, PointsPerMessage: 5, MessageCooldownSeconds: 60})

	service.HandleMessage(chatMessage("1", "ivan", "привет"))
	service.HandleMessage(chatMessage("1", "ivan", "ещё раз"))
	service.HandleMessage(chatMessage("2", "mary", "хай"))

	wallet, _ := service.Wallet("1")

	if wallet.Balance != 5 {
		t.Fatalf("cooldown must limit earnings, got %d", wallet.Balance)
	}

	mary, _ := service.Wallet("2")

	if mary.Balance != 5 {
		t.Fatalf("different user must earn, got %d", mary.Balance)
	}

	now = now.Add(2 * time.Minute)
	service.HandleMessage(chatMessage("1", "ivan", "снова"))

	wallet, _ = service.Wallet("1")

	if wallet.Balance != 10 {
		t.Fatalf("after cooldown must earn again, got %d", wallet.Balance)
	}
}

func TestRedeemSpendsBalance(t *testing.T) {
	service := New(nil, nil)
	service.Add("1", "ivan", "Ivan", 100, "тест")

	_, err := service.UpsertReward(Reward{Name: "Сменить игру", Cost: 50, Response: "Игра изменена для $(user)!"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result := service.Redeem(chatMessage("1", "ivan", "!redeem сменить игру"), "сменить игру")

	if result != "Игра изменена для ivan!" {
		t.Fatalf("unexpected redeem result: %q", result)
	}

	wallet, _ := service.Wallet("1")

	if wallet.Balance != 50 || wallet.TotalSpent != 50 {
		t.Fatalf("unexpected wallet: %+v", wallet)
	}
}

func TestRedeemInsufficientFunds(t *testing.T) {
	service := New(nil, nil)
	service.Add("1", "ivan", "Ivan", 10, "тест")

	_, _ = service.UpsertReward(Reward{Name: "Дорогая", Cost: 500})
	service.UpsertReward(Reward{Name: "Дорогая", Cost: 500})

	result := service.Redeem(chatMessage("1", "ivan", "!redeem дорогая"), "дорогая")

	if result == "" || result[:3] != "iva" {
		t.Fatalf("expected insufficient funds reply, got %q", result)
	}

	wallet, _ := service.Wallet("1")

	if wallet.Balance != 10 {
		t.Fatalf("balance must not change, got %d", wallet.Balance)
	}
}

func TestBuiltinCommandsReply(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)
	service.Add("1", "ivan", "Ivan", 42, "тест")

	service.HandleMessage(chatMessage("1", "ivan", "!points"))
	service.HandleMessage(chatMessage("1", "ivan", "!top"))

	if len(writer.messages) != 2 {
		t.Fatalf("expected two replies, got %v", writer.messages)
	}

	if writer.messages[0] != "ivan, у тебя 42 баллов (заработано 42)." {
		t.Fatalf("unexpected balance reply: %q", writer.messages[0])
	}
}

func TestAlertBonuses(t *testing.T) {
	service := New(nil, nil)

	service.HandleAlert(alerts.Event{Type: "subscribe", Username: "ivan"})
	service.HandleAlert(alerts.Event{Type: "cheer", Username: "bob", Amount: 100})
	service.HandleAlert(alerts.Event{Type: "gift", Username: "mary", Amount: 5})

	ivan, _ := service.Wallet("login:ivan")
	bob, _ := service.Wallet("login:bob")
	mary, _ := service.Wallet("login:mary")

	if ivan.Balance != 500 || bob.Balance != 100 || mary.Balance != 1250 {
		t.Fatalf("unexpected bonuses: ivan=%d bob=%d mary=%d", ivan.Balance, bob.Balance, mary.Balance)
	}
}

func TestViewersAndStreakAwards(t *testing.T) {
	service := New(nil, nil)

	service.HandleViewers([]viewer.Viewer{
		{UserID: "1", Login: "ivan", DisplayName: "Ivan"},
		{UserID: "2", Login: "mary", DisplayName: "Mary"},
	})

	service.HandleStreak(viewer.WatchStreakEvent{UserID: "1", Login: "ivan", DisplayName: "Ivan", StreakCount: 7})

	ivan, _ := service.Wallet("1")

	if ivan.Balance != 1+35 {
		t.Fatalf("expected 36 points for ivan, got %d", ivan.Balance)
	}

	if wallet, _ := service.Wallet("2"); wallet.Balance != 1 {
		t.Fatalf("expected 1 point for mary, got %d", wallet.Balance)
	}
}

func TestTopSortedByBalance(t *testing.T) {
	service := New(nil, nil)

	service.Add("1", "ivan", "Ivan", 10, "тест")
	service.Add("2", "mary", "Mary", 90, "тест")
	service.Add("3", "bob", "Bob", 50, "тест")

	top := service.Top(2)

	if len(top) != 2 || top[0].DisplayName != "Mary" || top[1].DisplayName != "Bob" {
		t.Fatalf("unexpected top: %+v", top)
	}
}
