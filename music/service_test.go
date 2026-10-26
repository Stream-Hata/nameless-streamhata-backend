package music

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

func chatMessage(login, text string) twitch.TwitchChatMessage {
	return twitch.TwitchChatMessage{
		MessageID: "m1",
		Author: twitch.ChatUser{
			Login:       login,
			DisplayName: strings.ToUpper(login[:1]) + login[1:],
		},
		Text: text,
	}
}

func TestExtractVideoID(t *testing.T) {
	cases := map[string]string{
		"зацените https://www.youtube.com/watch?v=dQw4w9WgXcQ ребята": "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                                     "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/AbCdEf12345":                       "AbCdEf12345",
		"https://music.youtube.com/watch?v=AbCdEf12345&list=RDAbCdEf12345": "AbCdEf12345",
		"https://www.youtube.com/watch?list=PL123&v=AbCdEf12345":           "AbCdEf12345",
	}

	for text, expected := range cases {
		id, ok := ExtractVideoID(text)

		if !ok || id != expected {
			t.Fatalf("for %q expected %q, got %q (ok=%v)", text, expected, id, ok)
		}
	}

	if _, ok := ExtractVideoID("https://example.com/video/123"); ok {
		t.Fatal("expected no match for non-youtube link")
	}
}

func TestAddTrackUpsertsAndStats(t *testing.T) {
	service := NewService(nil)

	service.fetcher = func(_ context.Context, videoID string) (*VideoMeta, error) {
		return &VideoMeta{
			VideoID:         videoID,
			Title:           "Track " + videoID,
			Author:          "Artist",
			DurationSeconds: 200,
		}, nil
	}

	first, err := service.Add(context.Background(), "aaa", "", "ivan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if first.TimesAdded != 1 || first.Title != "Track aaa" {
		t.Fatalf("unexpected first track: %+v", first)
	}

	time.Sleep(time.Millisecond)

	second, err := service.Add(context.Background(), "aaa", "", "mary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if second.TimesAdded != 2 {
		t.Fatalf("expected timesAdded 2, got %d", second.TimesAdded)
	}

	if second.AddedBy != "mary" {
		t.Fatalf("expected last added by mary, got %q", second.AddedBy)
	}

	if _, err := service.Add(context.Background(), "bbb", "", "bob"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats := service.Stats()

	if stats.TotalTracks != 2 {
		t.Fatalf("expected 2 tracks, got %d", stats.TotalTracks)
	}

	if stats.TotalDuration != 400 {
		t.Fatalf("expected 400 seconds total, got %d", stats.TotalDuration)
	}

	if stats.UniqueAuthors != 1 {
		t.Fatalf("expected 1 unique author, got %d", stats.UniqueAuthors)
	}
}

func TestHandleChatMessageFindsLink(t *testing.T) {
	service := NewService(nil)

	service.fetcher = func(_ context.Context, videoID string) (*VideoMeta, error) {
		return &VideoMeta{VideoID: videoID, Title: "Song", Author: "Band", DurationSeconds: 120}, nil
	}

	service.HandleChatMessage(context.Background(), chatMessage("ivan", "врубай https://youtu.be/zzz111"))

	tracks := service.List()

	if len(tracks) != 1 || tracks[0].VideoID != "zzz111" {
		t.Fatalf("expected parsed track, got %+v", tracks)
	}

	if tracks[0].AddedBy != "Ivan" {
		t.Fatalf("expected AddedBy Ivan, got %q", tracks[0].AddedBy)
	}
}

func TestDisabledServiceIgnoresChat(t *testing.T) {
	service := NewService(nil)

	service.fetcher = func(_ context.Context, videoID string) (*VideoMeta, error) {
		return &VideoMeta{VideoID: videoID, Title: "Song", Author: "Band"}, nil
	}

	if err := service.SetEnabled(false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	service.HandleChatMessage(context.Background(), chatMessage("ivan", "https://youtu.be/zzz111"))

	if len(service.List()) != 0 {
		t.Fatal("disabled service must ignore chat links")
	}
}

func TestWriteCSV(t *testing.T) {
	service := NewService(nil)

	service.fetcher = func(_ context.Context, videoID string) (*VideoMeta, error) {
		return &VideoMeta{VideoID: videoID, Title: "Song; with separator", Author: "Band", DurationSeconds: 61}, nil
	}

	if _, err := service.Add(context.Background(), "ccc", "", "ivan"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var builder strings.Builder

	if err := service.WriteCSV(&builder); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := builder.String()

	if !strings.Contains(output, "title;author") {
		t.Fatalf("expected csv header, got %q", output)
	}

	if !strings.Contains(output, `"Song; with separator"`) {
		t.Fatalf("expected quoted title, got %q", output)
	}
}
