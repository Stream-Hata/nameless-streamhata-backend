// Package syncstate — состояние оверлея (таймер, хотбар) для /rgg-sync.
package syncstate

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Time struct {
	Hours        int  `json:"hours"`
	Minutes      int  `json:"minutes"`
	Seconds      int  `json:"seconds"`
	Milliseconds *int `json:"milliseconds,omitempty"`
}

type TimerState struct {
	Mode string `json:"mode"`
	Time Time   `json:"time"`
}

type PlatformInfo struct {
	Name                string `json:"name"`
	CompletedGamesCount int    `json:"completedGamesCount"`
	Icon                string `json:"icon"`
	Code                string `json:"code"`
}

type HotbarState struct {
	Platforms []PlatformInfo `json:"platforms"`
}

// AppState — форма состояния, которую ждёт фронт (AppBackendState).
type AppState struct {
	Mode        string      `json:"mode"`
	TimerState  TimerState  `json:"timerState"`
	HotbarState HotbarState `json:"hotbarState"`
}

// Patch — частичное обновление (поля-указатели = «не менять»).
type Patch struct {
	Mode       *string `json:"mode"`
	TimerState *struct {
		Mode *string `json:"mode"`
		Time *struct {
			Hours   *int `json:"hours"`
			Minutes *int `json:"minutes"`
			Seconds *int `json:"seconds"`
		} `json:"time"`
	} `json:"timerState"`
	HotbarState *struct {
		Platforms *[]PlatformInfo `json:"platforms"`
	} `json:"hotbarState"`
}

type Service struct {
	mu              sync.Mutex
	state           AppState
	lastTimerUpdate time.Time
}

func New() *Service {
	return &Service{
		state: AppState{
			Mode: "solo",
			TimerState: TimerState{
				Mode: "pause",
			},
			HotbarState: HotbarState{
				Platforms: []PlatformInfo{},
			},
		},
		lastTimerUpdate: time.Now(),
	}
}

func (s *Service) advanceTimerLocked(now time.Time) {
	if s.state.TimerState.Mode != "play" {
		s.lastTimerUpdate = now

		return
	}

	elapsed := int(now.Sub(s.lastTimerUpdate) / time.Second)

	if elapsed <= 0 {
		return
	}

	total := s.state.TimerState.Time.Hours*3600 +
		s.state.TimerState.Time.Minutes*60 +
		s.state.TimerState.Time.Seconds +
		elapsed

	s.state.TimerState.Time = Time{
		Hours:   total / 3600,
		Minutes: (total % 3600) / 60,
		Seconds: total % 60,
	}
	s.lastTimerUpdate = s.lastTimerUpdate.Add(time.Duration(elapsed) * time.Second)
}

func (s *Service) Get() AppState {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.advanceTimerLocked(time.Now())

	return s.state
}

// Apply мержит патч в текущее состояние и возвращает результат.
func (s *Service) Apply(patch Patch) AppState {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	s.advanceTimerLocked(now)

	if patch.Mode != nil {
		s.state.Mode = *patch.Mode
	}

	if patch.TimerState != nil {
		if patch.TimerState.Mode != nil {
			s.state.TimerState.Mode = *patch.TimerState.Mode
		}

		if patch.TimerState.Time != nil {
			if patch.TimerState.Time.Hours != nil {
				s.state.TimerState.Time.Hours = *patch.TimerState.Time.Hours
			}

			if patch.TimerState.Time.Minutes != nil {
				s.state.TimerState.Time.Minutes = *patch.TimerState.Time.Minutes
			}

			if patch.TimerState.Time.Seconds != nil {
				s.state.TimerState.Time.Seconds = *patch.TimerState.Time.Seconds
			}
		}
	}

	s.lastTimerUpdate = now

	if patch.HotbarState != nil && patch.HotbarState.Platforms != nil {
		platforms := *patch.HotbarState.Platforms

		if platforms == nil {
			platforms = []PlatformInfo{}
		}

		s.state.HotbarState.Platforms = platforms
	}

	return s.state
}

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	handler := func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, service.Get())
		case http.MethodPost:
			var patch Patch

			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			writeJSON(response, http.StatusOK, service.Apply(patch))
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	}

	srv.ServeEndpoint("/rgg-sync", handler)
	srv.ServeEndpoint("/rgg-sync/", handler)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
