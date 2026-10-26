package syncstate

import (
	"reflect"
	"testing"
)

func TestApplyPartialPatch(t *testing.T) {
	service := New()

	mode := "land"
	timerMode := "play"
	minutes := 3
	seconds := 30

	state := service.Apply(Patch{
		Mode: &mode,
		TimerState: &struct {
			Mode *string `json:"mode"`
			Time *struct {
				Hours   *int `json:"hours"`
				Minutes *int `json:"minutes"`
				Seconds *int `json:"seconds"`
			} `json:"time"`
		}{
			Mode: &timerMode,
			Time: &struct {
				Hours   *int `json:"hours"`
				Minutes *int `json:"minutes"`
				Seconds *int `json:"seconds"`
			}{Minutes: &minutes, Seconds: &seconds},
		},
	})

	if state.Mode != "land" || state.TimerState.Mode != "play" {
		t.Fatalf("unexpected state: %+v", state)
	}

	if state.TimerState.Time.Minutes != 3 || state.TimerState.Time.Seconds != 30 {
		t.Fatalf("unexpected time: %+v", state.TimerState.Time)
	}
}

func TestApplyPlatforms(t *testing.T) {
	service := New()

	platforms := []PlatformInfo{
		{Name: "PlayStation", Code: "ps5", Icon: "ps5.svg", CompletedGamesCount: 12},
		{Name: "Xbox", Code: "xbox", Icon: "xbox.svg"},
	}

	state := service.Apply(Patch{
		HotbarState: &struct {
			Platforms *[]PlatformInfo `json:"platforms"`
		}{Platforms: &platforms},
	})

	if len(state.HotbarState.Platforms) != 2 {
		t.Fatalf("expected 2 platforms, got %+v", state.HotbarState.Platforms)
	}

	empty := []PlatformInfo{}
	state = service.Apply(Patch{
		HotbarState: &struct {
			Platforms *[]PlatformInfo `json:"platforms"`
		}{Platforms: &empty},
	})

	if state.HotbarState.Platforms == nil || len(state.HotbarState.Platforms) != 0 {
		t.Fatalf("expected empty non-nil platforms, got %+v", state.HotbarState.Platforms)
	}
}

func TestGetDoesNotChangeState(t *testing.T) {
	service := New()

	if !reflect.DeepEqual(service.Get(), service.Get()) {
		t.Fatal("Get must be stable")
	}
}
