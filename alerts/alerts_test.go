package alerts

import (
	"encoding/json"
	"testing"
)

func TestFollowNotification(t *testing.T) {
	service := New()

	service.HandleNotification("channel.follow", json.RawMessage(`{"user_name": "ivan"}`))

	events := service.Recent(5)

	if len(events) != 1 || events[0].Type != "follow" || events[0].Username != "ivan" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestCheerAndRaidNotifications(t *testing.T) {
	service := New()

	service.HandleNotification("channel.cheer", json.RawMessage(
		`{"user_name": "bob", "bits": 500, "message": "go"}`,
	))
	service.HandleNotification("channel.raid", json.RawMessage(
		`{"from_broadcaster_user_name": "friend", "viewers": 132}`,
	))

	events := service.Recent(5)

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %+v", events)
	}

	if events[0].Type != "raid" || events[0].Amount != 132 {
		t.Fatalf("unexpected raid event: %+v", events[0])
	}

	if events[1].Type != "cheer" || events[1].Amount != 500 {
		t.Fatalf("unexpected cheer event: %+v", events[1])
	}
}

func TestDisabledTypesFiltered(t *testing.T) {
	service := New()
	settings := service.GetSettings()
	settings.Types["follow"] = false
	service.SetSettings(settings)

	service.HandleNotification("channel.follow", json.RawMessage(`{"user_name": "ivan"}`))
	service.HandleNotification("channel.cheer", json.RawMessage(`{"user_name": "bob", "bits": 10}`))

	events := service.Recent(5)

	if len(events) != 1 || events[0].Type != "cheer" {
		t.Fatalf("disabled type must be filtered, got %+v", events)
	}
}

func TestDisabledServiceIgnoresEvents(t *testing.T) {
	service := New()
	_ = service.SetEnabled(false)

	service.HandleNotification("channel.follow", json.RawMessage(`{"user_name": "ivan"}`))

	if len(service.Recent(5)) != 0 {
		t.Fatal("disabled alerts service must ignore events")
	}
}

func TestTestAlertBypassesTypeFilter(t *testing.T) {
	service := New()
	settings := service.GetSettings()
	settings.Types["follow"] = false
	service.SetSettings(settings)

	event := service.Test("follow")

	if event.Username == "" || len(service.Recent(5)) != 1 {
		t.Fatalf("test alert must appear in feed, got %+v", event)
	}
}

func TestInvalidNotifications(t *testing.T) {
	service := New()

	service.HandleNotification("channel.follow", json.RawMessage(`{}`))
	service.HandleNotification("channel.unknown", json.RawMessage(`{"x": 1}`))
	service.HandleNotification("channel.cheer", json.RawMessage(`not-json`))

	if len(service.Recent(5)) != 0 {
		t.Fatal("invalid notifications must be ignored")
	}
}
