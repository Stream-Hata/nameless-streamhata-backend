package games

import (
	"strings"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
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

func TestSlotPayoutMath(t *testing.T) {
	cases := map[[3]int]int{
		{4, 4, 4}: 25,
		{3, 3, 3}: 15,
		{0, 0, 0}: 10,
		{1, 1, 0}: 2,
		{1, 2, 3}: 0,
	}

	for reels, expected := range cases {
		if got := slotPayout([]int{reels[0], reels[1], reels[2]}); got != expected {
			t.Fatalf("reels %v: expected %d, got %d", reels, expected, got)
		}
	}
}

func TestSlotsSpinsAndPays(t *testing.T) {
	writer := &fakeWriter{}
	pointsService := points.New(writer, nil)
	pointsService.Add("1", "ivan", "Ivan", 100, "тест")

	service := New(pointsService, writer)
	service.now = func() time.Time { return time.Unix(0, 0) }
	service.randN = func(int) int { return 0 } // все реелы — символ 0 → x10

	reply := service.Slots(chatMessage("1", "ivan", "!slots 50"), 50)

	if !strings.Contains(reply, "выигрыш 500") {
		t.Fatalf("unexpected slots reply: %q", reply)
	}

	wallet, _ := pointsService.Wallet("1")

	if wallet.Balance != 100-50+500 {
		t.Fatalf("unexpected balance: %d", wallet.Balance)
	}
}

func TestSlotsInsufficientBalance(t *testing.T) {
	writer := &fakeWriter{}
	pointsService := points.New(writer, nil)
	service := New(pointsService, writer)

	reply := service.Slots(chatMessage("1", "ivan", "!slots 100"), 100)

	if !strings.Contains(reply, "не хватает баллов") {
		t.Fatalf("expected insufficient balance reply, got %q", reply)
	}
}

func TestSlotsCooldown(t *testing.T) {
	writer := &fakeWriter{}
	pointsService := points.New(writer, nil)
	pointsService.Add("1", "ivan", "Ivan", 1000, "тест")

	service := New(pointsService, writer)
	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }

	service.Slots(chatMessage("1", "ivan", "!slots 10"), 10)

	reply := service.Slots(chatMessage("1", "ivan", "!slots 10"), 10)

	if !strings.Contains(reply, "Кулдаун") {
		t.Fatalf("expected cooldown reply, got %q", reply)
	}
}

func TestTriviaAnswerFlow(t *testing.T) {
	writer := &fakeWriter{}
	pointsService := points.New(writer, nil)
	service := New(pointsService, writer)

	settings := service.GetSettings()
	settings.TriviaQuestions = []Question{{Text: "2+2?", Answer: "4"}}
	service.SetSettings(settings)

	question := service.Trivia(chatMessage("1", "ivan", "!trivia"))

	if !strings.Contains(question, "2+2?") {
		t.Fatalf("unexpected trivia question: %q", question)
	}

	service.HandleMessage(chatMessage("2", "mary", "четыре"))
	service.HandleMessage(chatMessage("2", "mary", "4!"))

	wallet, _ := pointsService.Wallet("2")

	if wallet.Balance != settings.TriviaPoints {
		t.Fatalf("expected %d points for winner, got %d", settings.TriviaPoints, wallet.Balance)
	}

	// Повторный ответ после победы не начисляет.
	service.HandleMessage(chatMessage("3", "bob", "4"))

	if wallet, ok := pointsService.Wallet("3"); ok && wallet.Balance > 0 {
		t.Fatalf("trivia must be closed after win, got %+v", wallet)
	}
}

func TestWheelPays(t *testing.T) {
	writer := &fakeWriter{}
	pointsService := points.New(writer, nil)
	pointsService.Add("1", "ivan", "Ivan", 100, "тест")

	service := New(pointsService, writer)
	service.randN = func(int) int { return 99 } // последний исход — x5

	reply := service.Wheel(chatMessage("1", "ivan", "!wheel 10"), 10)

	if !strings.Contains(reply, "выигрыш 50") {
		t.Fatalf("unexpected wheel reply: %q", reply)
	}
}
