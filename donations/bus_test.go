package donations

import (
	"testing"
	"time"
)

func TestBusPublishDeduplicates(t *testing.T) {
	bus := NewBus(10)

	event := Event{ID: "42", Provider: "kofi", Username: "ivan", Amount: 100, Currency: "RUB"}

	bus.Publish(event)
	bus.Publish(event)

	recent := bus.Recent(10)

	if len(recent) != 1 {
		t.Fatalf("expected 1 event after dedupe, got %d", len(recent))
	}
}

func TestBusRingBufferKeepsLimit(t *testing.T) {
	bus := NewBus(3)

	for i := 1; i <= 5; i++ {
		bus.Publish(Event{
			ID:       string(rune('a' + i)),
			Provider: "generic",
			Username: "user",
			Amount:   float64(i),
			Currency: "RUB",
		})
	}

	recent := bus.Recent(10)

	if len(recent) != 3 {
		t.Fatalf("expected 3 events in ring buffer, got %d", len(recent))
	}

	if recent[0].Amount != 5 {
		t.Fatalf("expected newest event first, got amount %v", recent[0].Amount)
	}
}

func TestBusStatsTotalsAndTop(t *testing.T) {
	bus := NewBus(10)

	now := time.Now().UTC()

	bus.Publish(Event{ID: "1", Provider: "kofi", Username: "ivan", Amount: 100, Currency: "RUB", CreatedAt: now})
	bus.Publish(Event{ID: "2", Provider: "kofi", Username: "ivan", Amount: 50, Currency: "RUB", CreatedAt: now})
	bus.Publish(Event{ID: "3", Provider: "kofi", Username: "mary", Amount: 200, Currency: "RUB", CreatedAt: now})
	bus.Publish(Event{ID: "4", Provider: "kofi", Username: "bob", Amount: 10, Currency: "USD", CreatedAt: now})

	stats := bus.Stats()

	if stats.TotalCount != 4 {
		t.Fatalf("expected total 4, got %d", stats.TotalCount)
	}

	if stats.Totals["RUB"] != 350 {
		t.Fatalf("expected RUB total 350, got %v", stats.Totals["RUB"])
	}

	if stats.Totals["USD"] != 10 {
		t.Fatalf("expected USD total 10, got %v", stats.Totals["USD"])
	}

	if len(stats.Top) == 0 || stats.Top[0].Username != "mary" {
		t.Fatalf("expected mary as top donator, got %+v", stats.Top)
	}
}

func TestBusSubscribeReceivesEvents(t *testing.T) {
	bus := NewBus(10)

	channel, unsubscribe := bus.Subscribe()
	defer unsubscribe()

	bus.Publish(Event{ID: "1", Provider: "generic", Username: "ivan", Amount: 1})

	select {
	case event := <-channel:
		if event.ID != "1" {
			t.Fatalf("unexpected event %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered to subscriber")
	}
}
