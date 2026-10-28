package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type fakeWriter struct {
	messages []string
}

func (f *fakeWriter) Say(text string) error {
	f.messages = append(f.messages, text)

	return nil
}

func message(authorID, author, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID:   "m-" + text,
		Broadcaster: twitch.ChatUser{ID: "channel", Login: "kira_plays", DisplayName: "Kira"},
		Author:      twitch.ChatUser{ID: authorID, Login: strings.ToLower(author), DisplayName: author},
		Text:        text,
	}
}

func TestRenderVariables(t *testing.T) {
	text := render("Привет, $(user) на $(channel)! Уже $(count) раз, $(date) $(time)", message("1", "Иван", "!hi"), 7)

	if !strings.Contains(text, "Иван") || !strings.Contains(text, "Kira") || !strings.Contains(text, "7") {
		t.Fatalf("unexpected render: %q", text)
	}

	if strings.Contains(text, "$(") {
		t.Fatalf("variables must be replaced: %q", text)
	}
}

func TestCommandExecutionWithCooldown(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)
	service.now = func() time.Time { return time.Unix(0, 0) }

	_, err := service.Upsert(Command{
		Name:            "hi",
		Response:        "Привет, $(user)!",
		CooldownSeconds: 30,
		UserLevel:       "everyone",
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	service.HandleMessage(message("1", "Иван", "!hi"))
	service.HandleMessage(message("1", "Иван", "!hi"))

	if len(writer.messages) != 1 {
		t.Fatalf("cooldown must suppress second call, got %v", writer.messages)
	}

	if writer.messages[0] != "@иван, Привет, Иван!" {
		t.Fatalf("unexpected message: %q", writer.messages[0])
	}

	service.HandleMessage(message("2", "Мария", "!hi"))

	if len(writer.messages) != 2 {
		t.Fatalf("different user must bypass cooldown, got %v", writer.messages)
	}
}

func TestAliasesAndCounters(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)

	_, err := service.Upsert(Command{
		Name:      "count",
		Aliases:   []string{"counter"},
		Response:  "Счётчик: $(count)",
		UserLevel: "everyone",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	service.HandleMessage(message("1", "Иван", "!counter"))
	service.HandleMessage(message("2", "Мария", "!count"))

	if len(writer.messages) != 2 {
		t.Fatalf("expected alias and name to work, got %v", writer.messages)
	}

	if writer.messages[0] != "@иван, Счётчик: 1" || writer.messages[1] != "@мария, Счётчик: 2" {
		t.Fatalf("unexpected counters: %v", writer.messages)
	}
}

func TestDisabledCommandIgnored(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)

	_, _ = service.Upsert(Command{Name: "off", Response: "nope", Enabled: false})

	service.HandleMessage(message("1", "Иван", "!off"))

	if len(writer.messages) != 0 {
		t.Fatal("disabled command must be ignored")
	}
}

func TestValidation(t *testing.T) {
	service := New(nil, nil)

	if _, err := service.Upsert(Command{Name: "", Response: "x"}); err == nil {
		t.Fatal("expected name validation")
	}

	if _, err := service.Upsert(Command{Name: "x", Response: ""}); err == nil {
		t.Fatal("expected response validation")
	}
}

func TestTestEndpointRenders(t *testing.T) {
	service := New(nil, nil)

	saved, err := service.Upsert(Command{Name: "hi", Response: "Йо, $(user)!"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text, ok := service.Test(saved.ID)

	if !ok || text != "Йо, Тестер!" {
		t.Fatalf("unexpected test render: %q (ok=%v)", text, ok)
	}
}

func TestExpandBracedAndSelfReferencingVariable(t *testing.T) {
	service := New(nil, nil)
	service.custom["fdate"] = Variable{Name: "fdate", Action: "twitch.followage", Value: "${fdate}"}
	service.variables = func(name, value string, message twitch.TwitchChatMessage) string {
		if name == "twitch.followage" {
			return "12.03.2024"
		}

		return ""
	}

	text := service.expand("ты зафолловился ${fdate}", message("1", "Иван", "!follow"))
	if text != "ты зафолловился 12.03.2024" {
		t.Fatalf("unexpected expansion: %q", text)
	}
}

func TestExpandSelfReferenceDoesNotLoop(t *testing.T) {
	service := New(nil, nil)
	service.custom["loop"] = Variable{Name: "loop", Value: "${loop}"}

	text := service.expand("${loop}", message("1", "Иван", "!loop"))
	if !strings.Contains(text, "loop") {
		t.Fatalf("self reference must not disappear: %q", text)
	}
}

func TestReplyExpandsVariables(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)
	service.custom["fdate"] = Variable{Name: "fdate", Value: "12.03.2024"}
	service.variables = func(name, value string, message twitch.TwitchChatMessage) string {
		return ""
	}

	_, err := service.Upsert(Command{
		Name:      "follow",
		Response:  "fallback",
		Reply:     "ты зафолловился ${fdate}",
		UserLevel: "everyone",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	service.HandleMessage(message("1", "Иван", "!follow"))

	if len(writer.messages) != 1 || !strings.Contains(writer.messages[0], "12.03.2024") {
		t.Fatalf("reply variable was not expanded: %v", writer.messages)
	}
}

func TestSingleChatMessageCollapsesLines(t *testing.T) {
	text := "Правила чата:\n1. Уважай всех.\n2. Не спамь."
	got := singleChatMessage(text, 500)
	if got != "Правила чата: 1. Уважай всех. 2. Не спамь." {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestSingleChatMessageTruncates(t *testing.T) {
	got := singleChatMessage(strings.Repeat("а", 600), 500)
	if len([]rune(got)) != 500 {
		t.Fatalf("expected 500 runes, got %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}
}

func TestLineBreakReplySendsEachLine(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer, nil)
	service.sendReply(Command{Reply: "Первая\nВторая", LineBreaks: true}, message("1", "Иван", "!rules"), "")

	if len(writer.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(writer.messages), writer.messages)
	}
	if !strings.Contains(writer.messages[0], "Первая") || !strings.Contains(writer.messages[1], "Вторая") {
		t.Fatalf("unexpected messages: %#v", writer.messages)
	}
}
