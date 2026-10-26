package games

import (
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type testWriter struct {
	messages []string
}

func (w *testWriter) Say(text string) error {
	w.messages = append(w.messages, text)

	return nil
}

func newWheelTestService() (*Service, *testWriter) {
	writer := &testWriter{}
	pointsService := points.New(writer, nil)
	service := New(pointsService, writer)
	service.now = func() time.Time { return time.Unix(1000, 0) }
	service.randN = func(n int) int {
		if n <= 1 {
			return 0
		}

		return n - 1
	}

	pointsService.Add("u1", "u1", "User One", 10000, "test")
	pointsService.Add("u2", "u2", "User Two", 10000, "test")

	return service, writer
}

func testMessage(id, login string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		Author: twitch.ChatUser{ID: id, Login: login, DisplayName: login},
		Text:   "!wheel TBoI 500",
	}
}

func TestParseWheelArgs(t *testing.T) {
	settings := Settings{MinBet: 10, MaxBet: 1000}

	title, stake := parseWheelArgs([]string{"TBoI", "500"}, settings)
	if title != "TBoI" || stake != 500 {
		t.Fatalf("unexpected args: %q %d", title, stake)
	}

	title, stake = parseWheelArgs([]string{"5000"}, settings)
	if title != "Колесо" || stake != 1000 {
		t.Fatalf("unexpected numeric args: %q %d", title, stake)
	}
}

func TestParseWheelArgsWithLuckAndMultiWordTitle(t *testing.T) {
	settings := Settings{MinBet: 10, MaxBet: 1000}

	title, stake := parseWheelArgs(
		[]string{"The", "Binding", "of", "Isaac", "удача", "100"},
		settings,
	)

	if title != "The Binding of Isaac" || stake != 100 {
		t.Fatalf("unexpected parse: %q %d", title, stake)
	}

	if !hasLuckText("The Binding of Isaac удача") {
		t.Fatal("expected luck flag")
	}
}

func TestEnterWheelMergesSameLotAndSplitsDifferentLot(t *testing.T) {
	service, _ := newWheelTestService()

	if err := service.EnterWheel("streamer", "streamer", "Streamer", "Lot A", false); err != nil {
		t.Fatal(err)
	}

	if err := service.EnterWheel("streamer", "streamer", "Streamer", "Lot A", false); err != nil {
		t.Fatal(err)
	}

	round := service.GetWheelRound()
	if len(round.Participants) != 1 {
		t.Fatalf("expected one merged participant, got %d", len(round.Participants))
	}

	if round.Participants[0].BaseBet != 20 {
		t.Fatalf("expected base bet 20, got %d", round.Participants[0].BaseBet)
	}

	if err := service.EnterWheel("streamer", "streamer", "Streamer", "Lot B", false); err != nil {
		t.Fatal(err)
	}

	round = service.GetWheelRound()
	if len(round.Participants) != 2 {
		t.Fatalf("expected second lot for different title, got %d", len(round.Participants))
	}

	if round.Participants[1].UserID == round.Participants[0].UserID {
		t.Fatal("expected unique lot keys")
	}
}

func TestEliminationWheelRound(t *testing.T) {
	service, writer := newWheelTestService()
	settings := service.GetSettings()
	settings.WheelMode = "elimination"
	settings.WheelCooldown = 0
	settings.WheelRoundSeconds = 60
	settings.MinBet = 10
	settings.MaxBet = 1000

	service.HandleWheelCommand(testMessage("u1", "user1"), []string{"TBoI", "500"}, settings)

	round := service.GetWheelRound()
	if round == nil {
		t.Fatal("round was not started")
	}

	if len(round.Participants) != 1 {
		t.Fatalf("expected 1 participant, got %d", len(round.Participants))
	}

	service.HandleWheelCommand(testMessage("u2", "user2"), []string{"TBoI", "500"}, settings)

	round = service.GetWheelRound()
	if len(round.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(round.Participants))
	}

	if round.Pot != 1000 {
		t.Fatalf("expected pot 1000, got %d", round.Pot)
	}

	service.SpinWheel(round.ID)
	round = service.GetWheelRound()

	if round.State != "spinning" {
		t.Fatalf("expected spinning, got %q", round.State)
	}

	if len(round.Order) != 1 {
		t.Fatalf("expected one eliminated participant, got %d", len(round.Order))
	}

	service.finishWheelElimination(round.ID, round.Order[0].UserID)
	round = service.GetWheelRound()

	if round.State != "finished" || round.Winner == nil {
		t.Fatalf("expected finished round with winner, got %#v", round)
	}

	if service.GetLastResult() == nil {
		t.Fatal("last result was not stored")
	}

	if len(writer.messages) == 0 {
		t.Fatal("expected chat announcements")
	}

	if service.wheelTimer != nil {
		service.wheelTimer.Stop()
	}
}
