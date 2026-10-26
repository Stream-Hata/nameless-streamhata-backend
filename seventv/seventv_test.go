package seventv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshMergesSets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/global":
			_ = json.NewEncoder(response).Encode(globalResponse{Emotes: []emote{
				{ID: "g1", Name: "GlobalCat"},
				{ID: "g2", Name: "Shared"},
			}})
		case "/channel":
			_ = json.NewEncoder(response).Encode(channelResponse{
				EmoteSet: struct {
					Emotes []emote `json:"emotes"`
				}{Emotes: []emote{
					{ID: "c1", Name: "Shared"},
					{ID: "c2", Name: "ChanDog"},
				}},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	service := New()
	service.channelID = "123"

	// Переопределяем URL'ы через локальные копии refresh-логики.
	emotes := map[string]string{}
	if err := service.loadGlobal(context.Background(), server.URL+"/global", emotes); err != nil {
		t.Fatalf("global: %v", err)
	}
	if err := service.loadChannel(context.Background(), server.URL+"/channel", emotes); err != nil {
		t.Fatalf("channel: %v", err)
	}

	if emotes["GlobalCat"] != "https://cdn.7tv.app/emote/g1/2x.webp" {
		t.Fatalf("unexpected global emote: %q", emotes["GlobalCat"])
	}

	if emotes["Shared"] != "https://cdn.7tv.app/emote/c1/2x.webp" {
		t.Fatalf("channel set must override global: %q", emotes["Shared"])
	}

	if emotes["ChanDog"] == "" {
		t.Fatal("expected channel emote")
	}
}

func TestDisableClearsEmotes(t *testing.T) {
	service := New()
	service.emotes = map[string]string{"x": "y"}

	_ = service.SetEnabled(false)

	if len(service.Snapshot().Emotes) != 0 {
		t.Fatal("disable must clear emote cache")
	}
}
