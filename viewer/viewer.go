// Package viewer — рантайм зрителей: поллинг чатерсов, watch streak,
// «последний стрик» с очередью показа для оверлея.
package viewer

import (
	"context"
	"encoding/json"
	"github.com/p7x-dev/nameless-streamhata-backend/bots"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	chattersURL      = "https://api.twitch.tv/helix/chat/chatters"
	chattersInterval = 60 * time.Second
	streakQueueDelay = 20 * time.Second
)

// Viewer — зритель канала с накопленной статистикой.
type Viewer struct {
	UserID      string `json:"userId"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`

	WatchStreak          int `json:"watchStreak"`
	ChannelPointsAwarded int `json:"channelPointsAwarded"`

	FirstSeenAt string `json:"firstSeenAt"`
	LastSeenAt  string `json:"lastSeenAt"`
}

// WatchStreakEvent — событие стрика просмотра.
type WatchStreakEvent struct {
	UserID      string `json:"userId"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`

	StreakCount          int `json:"streakCount"`
	ChannelPointsAwarded int `json:"channelPointsAwarded"`
}

// ListResponse — снимок зрителей для панели.
type ListResponse struct {
	Total   int      `json:"total"`
	Viewers []Viewer `json:"viewers"`
}

type chattersResponse struct {
	Data []struct {
		UserID    string `json:"user_id"`
		UserLogin string `json:"user_login"`
		UserName  string `json:"user_name"`
	} `json:"data"`

	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
}

// ChatNotification — событие channel.chat.notification (watch_streak и др.).
type ChatNotification struct {
	ChatterUserID    string `json:"chatter_user_id"`
	ChatterUserLogin string `json:"chatter_user_login"`
	ChatterUserName  string `json:"chatter_user_name"`

	NoticeType string `json:"notice_type"`

	WatchStreak *struct {
		StreakCount          int `json:"streak_count"`
		ChannelPointsAwarded int `json:"channel_points_awarded"`
	} `json:"watch_streak"`
}

type pendingStreak struct {
	event       WatchStreakEvent
	availableAt time.Time
}

// Runtime держит состояние зрителей и канал показа стриков.
type Runtime struct {
	clientID string
	http     *http.Client

	mu      sync.Mutex
	viewers map[string]Viewer

	onTick   func([]Viewer)
	onStreak func(WatchStreakEvent)

	broadcasterID string
	accessToken   string
	running       bool
	cancel        context.CancelFunc

	streakDelay time.Duration

	pending      []pendingStreak
	latestStreak *WatchStreakEvent
	queueTimer   *time.Timer
}

// SetOnTick — колбэк после каждого опроса чатерсов (начисление баллов).
func (r *Runtime) SetOnTick(hook func([]Viewer)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.onTick = hook
}

// SetOnStreak — колбэк на полученный watch streak.
func (r *Runtime) SetOnStreak(hook func(WatchStreakEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.onStreak = hook
}

func New(clientID string) *Runtime {
	return &Runtime{
		clientID:    clientID,
		http:        netcfg.NewHTTPClient(20 * time.Second),
		viewers:     make(map[string]Viewer),
		streakDelay: streakQueueDelay,
	}
}

// Start запускает поллинг чатерсов (если уже запущен — перезапускает).
func (r *Runtime) Start(ctx context.Context, broadcasterID, accessToken string) {
	r.Stop()

	if broadcasterID == "" || accessToken == "" {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)

	r.mu.Lock()
	r.broadcasterID = broadcasterID
	r.accessToken = accessToken
	r.running = true
	r.cancel = cancel
	r.mu.Unlock()

	go r.pollChatters(runCtx)
}

// Stop останавливает поллинг и чистит очередь стриков.
func (r *Runtime) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.running = false

	if r.queueTimer != nil {
		r.queueTimer.Stop()
		r.queueTimer = nil
	}

	r.pending = nil
	r.latestStreak = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// UpdateViewer фиксирует активность зрителя в чате/чатерсах.
func (r *Runtime) UpdateViewer(userID, login, displayName string) Viewer {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	r.mu.Lock()
	defer r.mu.Unlock()

	current, exists := r.viewers[userID]

	viewer := Viewer{
		UserID:               userID,
		Login:                login,
		DisplayName:          displayName,
		WatchStreak:          current.WatchStreak,
		ChannelPointsAwarded: current.ChannelPointsAwarded,
		LastSeenAt:           now,
		FirstSeenAt:          now,
	}

	if exists {
		viewer.FirstSeenAt = current.FirstSeenAt
	}

	r.viewers[userID] = viewer

	return viewer
}

// UpdateWatchStreak применяет стрик и ставит его в очередь показа.
func (r *Runtime) UpdateWatchStreak(event WatchStreakEvent) Viewer {
	r.mu.Lock()

	current, exists := r.viewers[event.UserID]

	previousStreak := 0

	if exists {
		previousStreak = current.WatchStreak
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	viewer := Viewer{
		UserID:               event.UserID,
		Login:                event.Login,
		DisplayName:          event.DisplayName,
		WatchStreak:          event.StreakCount,
		ChannelPointsAwarded: event.ChannelPointsAwarded,
		LastSeenAt:           now,
		FirstSeenAt:          now,
	}

	if exists {
		viewer.FirstSeenAt = current.FirstSeenAt
	}

	r.viewers[event.UserID] = viewer

	if event.StreakCount > previousStreak {
		r.pending = append(r.pending, pendingStreak{
			event:       event,
			availableAt: time.Now().Add(r.streakDelay),
		})

		r.scheduleNextLocked()
	}

	hook := r.onStreak
	r.mu.Unlock()

	if hook != nil {
		hook(event)
	}

	return viewer
}

// HandleChatNotification разбирает channel.chat.notification (watch_streak).
func (r *Runtime) HandleChatNotification(raw json.RawMessage) {
	var notification ChatNotification

	if err := json.Unmarshal(raw, &notification); err != nil {
		return
	}

	if notification.NoticeType != "watch_streak" || notification.WatchStreak == nil {
		return
	}

	if notification.ChatterUserID == "" {
		return
	}

	r.UpdateWatchStreak(WatchStreakEvent{
		UserID:               notification.ChatterUserID,
		Login:                notification.ChatterUserLogin,
		DisplayName:          notification.ChatterUserName,
		StreakCount:          notification.WatchStreak.StreakCount,
		ChannelPointsAwarded: notification.WatchStreak.ChannelPointsAwarded,
	})
}

func (r *Runtime) GetViewer(userID string) (Viewer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	viewer, ok := r.viewers[userID]

	return viewer, ok
}

// Snapshot отдаёт список зрителей с фильтром по первой букве и сортировкой по стрику.
func (r *Runtime) Snapshot(letter, streak string) ListResponse {
	r.mu.Lock()

	viewers := make([]Viewer, 0, len(r.viewers))

	for _, viewer := range r.viewers {
		if bots.Is(viewer.Login) {
			continue
		}

		viewers = append(viewers, viewer)
	}

	r.mu.Unlock()

	if normalized := strings.ToLower(strings.TrimSpace(letter)); normalized != "" {
		filtered := viewers[:0]

		for _, viewer := range viewers {
			if strings.HasPrefix(strings.ToLower(viewer.DisplayName), normalized) {
				filtered = append(filtered, viewer)
			}
		}

		viewers = filtered
	}

	switch streak {
	case "asc":
		sort.Slice(viewers, func(i, j int) bool { return viewers[i].WatchStreak < viewers[j].WatchStreak })
	case "desc":
		sort.Slice(viewers, func(i, j int) bool { return viewers[i].WatchStreak > viewers[j].WatchStreak })
	}

	return ListResponse{Total: len(viewers), Viewers: viewers}
}

func (r *Runtime) GetLatestStreak() *WatchStreakEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.latestStreak
}

func (r *Runtime) scheduleNextLocked() {
	if r.queueTimer != nil || len(r.pending) == 0 {
		return
	}

	delay := time.Until(r.pending[0].availableAt)

	if delay < 0 {
		delay = 0
	}

	r.queueTimer = time.AfterFunc(delay, r.popStreak)
}

func (r *Runtime) popStreak() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.queueTimer = nil

	if len(r.pending) == 0 {
		return
	}

	item := r.pending[0]
	r.pending = r.pending[1:]
	r.latestStreak = &item.event

	r.scheduleNextLocked()
}

func (r *Runtime) pollChatters(ctx context.Context) {
	r.refreshChatters(ctx)

	ticker := time.NewTicker(chattersInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refreshChatters(ctx)
		}
	}
}

func (r *Runtime) refreshChatters(ctx context.Context) {
	r.mu.Lock()
	broadcasterID := r.broadcasterID
	accessToken := r.accessToken
	r.mu.Unlock()

	if broadcasterID == "" || accessToken == "" {
		return
	}

	cursor := ""

	for {
		params := url.Values{}
		params.Set("broadcaster_id", broadcasterID)
		params.Set("moderator_id", broadcasterID)
		params.Set("first", "100")

		if cursor != "" {
			params.Set("after", cursor)
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, chattersURL+"?"+params.Encode(), nil)
		if err != nil {
			return
		}

		request.Header.Set("Authorization", "Bearer "+accessToken)
		request.Header.Set("Client-Id", r.clientID)

		response, err := r.http.Do(request)
		if err != nil {
			log.Printf("[Viewer] chatters request failed: %v", err)

			return
		}

		var data chattersResponse

		decodeErr := json.NewDecoder(response.Body).Decode(&data)
		_ = response.Body.Close()

		if response.StatusCode != http.StatusOK || decodeErr != nil {
			log.Printf("[Viewer] chatters returned %d", response.StatusCode)

			return
		}

		for _, chatter := range data.Data {
			r.UpdateViewer(chatter.UserID, chatter.UserLogin, chatter.UserName)
		}

		cursor = data.Pagination.Cursor

		if cursor == "" {
			break
		}
	}

	r.mu.Lock()
	hook := r.onTick
	snapshot := make([]Viewer, 0, len(r.viewers))

	for _, viewer := range r.viewers {
		snapshot = append(snapshot, viewer)
	}

	r.mu.Unlock()

	if hook != nil {
		hook(snapshot)
	}
}
