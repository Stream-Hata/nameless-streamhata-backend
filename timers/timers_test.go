package timers

import (
	"strings"
	"testing"
	"time"
)

type fakeWriter struct {
	messages []string
}

func (f *fakeWriter) Say(text string) error {
	f.messages = append(f.messages, text)

	return nil
}

func TestTimersFireWithInterval(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer)
	service.SetChannel("kira_plays")

	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }

	_, err := service.Upsert(Timer{
		Name:            "Реклама",
		Messages:        []string{"Подписывайтесь на $(channel)!", "Ещё раз про $(channel)"},
		IntervalSeconds: 60,
		Enabled:         true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	service.Tick(now)

	if len(writer.messages) != 0 {
		t.Fatal("timer must not fire before the first interval")
	}

	service.Tick(now.Add(61 * time.Second))

	if len(writer.messages) != 1 {
		t.Fatalf("expected first fire, got %v", writer.messages)
	}

	if !strings.Contains(writer.messages[0], "kira_plays") {
		t.Fatalf("channel variable must be replaced: %q", writer.messages[0])
	}

	service.Tick(now.Add(91 * time.Second))

	if len(writer.messages) != 1 {
		t.Fatal("interval must suppress early fire")
	}

	service.Tick(now.Add(122 * time.Second))

	if len(writer.messages) != 2 {
		t.Fatalf("expected second fire after interval, got %v", writer.messages)
	}

	if writer.messages[1] != "Ещё раз про kira_plays" {
		t.Fatalf("expected round-robin second message, got %q", writer.messages[1])
	}
}

func TestActiveOnlyRespectsChatActivity(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer)

	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }

	_, _ = service.Upsert(Timer{
		Messages:        []string{"active-only"},
		IntervalSeconds: 60,
		Enabled:         true,
		ActiveOnly:      true,
	})

	service.Tick(now.Add(61 * time.Second))

	if len(writer.messages) != 0 {
		t.Fatal("timer without chat activity must not fire")
	}

	service.NoteChatActivity()
	service.Tick(now.Add(62 * time.Second))

	if len(writer.messages) != 1 {
		t.Fatalf("expected fire with activity, got %v", writer.messages)
	}
}

func TestDisabledTimerIgnored(t *testing.T) {
	writer := &fakeWriter{}
	service := New(writer)

	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }

	_, _ = service.Upsert(Timer{Messages: []string{"x"}, IntervalSeconds: 60, Enabled: false})
	service.Tick(now.Add(time.Hour))

	if len(writer.messages) != 0 {
		t.Fatal("disabled timer must not fire")
	}
}

func TestUpsertValidation(t *testing.T) {
	service := New(nil)

	if _, err := service.Upsert(Timer{Messages: []string{}}); err == nil {
		t.Fatal("expected messages validation")
	}

	timer, err := service.Upsert(Timer{Messages: []string{"x"}, IntervalSeconds: 5})

	if err != nil || timer.IntervalSeconds != minIntervalSec {
		t.Fatalf("expected interval clamp, got %+v (%v)", timer, err)
	}
}
