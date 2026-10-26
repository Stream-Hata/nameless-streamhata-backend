package viewer

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWatchStreakQueuedAndDelivered(t *testing.T) {
	runtime := New("client")
	runtime.streakDelay = 30 * time.Millisecond

	runtime.UpdateWatchStreak(WatchStreakEvent{
		UserID: "1", Login: "ivan", DisplayName: "Ivan", StreakCount: 5, ChannelPointsAwarded: 100,
	})

	if runtime.GetLatestStreak() != nil {
		t.Fatal("streak must not be delivered immediately")
	}

	time.Sleep(80 * time.Millisecond)

	streak := runtime.GetLatestStreak()

	if streak == nil || streak.StreakCount != 5 || streak.Login != "ivan" {
		t.Fatalf("unexpected streak: %+v", streak)
	}
}

func TestStreakOnlyQueuedWhenIncreased(t *testing.T) {
	runtime := New("client")
	runtime.streakDelay = time.Second

	runtime.UpdateWatchStreak(WatchStreakEvent{UserID: "1", Login: "ivan", StreakCount: 5})
	runtime.UpdateWatchStreak(WatchStreakEvent{UserID: "1", Login: "ivan", StreakCount: 5})
	runtime.UpdateWatchStreak(WatchStreakEvent{UserID: "1", Login: "ivan", StreakCount: 4})

	runtime.mu.Lock()
	queueLength := len(runtime.pending)
	runtime.mu.Unlock()

	if queueLength != 1 {
		t.Fatalf("expected 1 queued streak, got %d", queueLength)
	}
}

func TestHandleChatNotification(t *testing.T) {
	runtime := New("client")

	raw := json.RawMessage(`{
		"chatter_user_id": "42",
		"chatter_user_login": "nameless",
		"chatter_user_name": "Namel3ss",
		"notice_type": "watch_streak",
		"watch_streak": {"streak_count": 7, "channel_points_awarded": 350}
	}`)

	runtime.HandleChatNotification(raw)

	viewer, ok := runtime.GetViewer("42")

	if !ok || viewer.WatchStreak != 7 || viewer.ChannelPointsAwarded != 350 {
		t.Fatalf("unexpected viewer: %+v (ok=%v)", viewer, ok)
	}
}

func TestHandleChatNotificationIgnoresOtherTypes(t *testing.T) {
	runtime := New("client")

	runtime.HandleChatNotification(json.RawMessage(
		`{"notice_type": "raid", "chatter_user_id": "1"}`,
	))

	if _, ok := runtime.GetViewer("1"); ok {
		t.Fatal("non-streak notification must be ignored")
	}
}

func TestSnapshotFiltersAndSorts(t *testing.T) {
	runtime := New("client")

	runtime.UpdateViewer("1", "ivan", "Ivan")
	runtime.UpdateViewer("2", "mary", "Mary")
	runtime.UpdateViewer("3", "igor", "Igor")

	runtime.mu.Lock()
	viewer := runtime.viewers["1"]
	viewer.WatchStreak = 3
	runtime.viewers["1"] = viewer

	viewer = runtime.viewers["2"]
	viewer.WatchStreak = 9
	runtime.viewers["2"] = viewer
	runtime.mu.Unlock()

	byLetter := runtime.Snapshot("i", "")

	if byLetter.Total != 2 {
		t.Fatalf("expected 2 viewers for letter i, got %d", byLetter.Total)
	}

	sorted := runtime.Snapshot("", "desc")

	if sorted.Viewers[0].Login != "mary" {
		t.Fatalf("expected mary first, got %+v", sorted.Viewers)
	}

	asc := runtime.Snapshot("", "asc")

	if asc.Viewers[0].Login != "igor" {
		t.Fatalf("expected igor first in asc, got %+v", asc.Viewers)
	}
}
