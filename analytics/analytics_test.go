package analytics

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	viewers []int
	index   int
}

func (f *fakeSource) GetStreamViewers(context.Context) (int, error) {
	if f.index >= len(f.viewers) {
		return f.viewers[len(f.viewers)-1], nil
	}

	value := f.viewers[f.index]
	f.index++

	return value, nil
}

func TestSessionLifecycle(t *testing.T) {
	source := &fakeSource{viewers: []int{100, 250, 150}}
	service := New(source)
	now := time.Unix(0, 0)
	service.now = func() time.Time { return now }

	service.StreamOnline()
	service.HandleMessage()
	service.HandleMessage()
	service.HandleChatters(42)

	ctx := context.Background()
	service.Tick(ctx)
	service.Tick(ctx)
	service.Tick(ctx)

	current := service.Current()

	if current == nil || current.PeakViewers != 250 {
		t.Fatalf("unexpected peak: %+v", current)
	}

	if current.AvgViewers != (100+250+150)/3 {
		t.Fatalf("unexpected avg: %d", current.AvgViewers)
	}

	if current.Messages != 2 || current.Chatters != 42 {
		t.Fatalf("unexpected counters: %+v", current)
	}

	service.StreamOffline()

	if service.Current() != nil {
		t.Fatal("session must be closed")
	}

	history := service.History()

	if len(history) != 1 || history[0].EndedAt == nil {
		t.Fatalf("unexpected history: %+v", history)
	}
}

func TestOfflineWithoutSessionIsSafe(t *testing.T) {
	service := New(nil)

	service.StreamOffline()
	service.HandleMessage()

	if service.Current() != nil {
		t.Fatal("no session must stay nil")
	}
}

func TestCSVExport(t *testing.T) {
	source := &fakeSource{viewers: []int{100}}
	service := New(source)
	service.StreamOnline()
	service.Tick(context.Background())
	service.StreamOffline()

	var builder strings.Builder

	if err := service.WriteCSV(&builder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := builder.String()

	if !strings.Contains(output, "session_id;started_at") {
		t.Fatalf("expected header, got %q", output)
	}

	if !strings.Contains(output, "100") {
		t.Fatalf("expected peak in export, got %q", output)
	}
}
