// Package chatruntime — подписка на чат канала: OAuth-refresh токена,
// подключение IRC и EventSub, подача сообщений в шину чата.
package chatruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"

	"github.com/p7x-dev/nameless-streamhata-backend/alerts"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/eventsub"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/twitchirc"
	"github.com/p7x-dev/nameless-streamhata-backend/viewer"
)

const (
	tokenURL = "https://id.twitch.tv/oauth2/token"
	usersURL = "https://api.twitch.tv/helix/users"
)

type tokensResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type helixUsersResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Login       string `json:"login"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// Runtime держит подключение к чату канала и переподключает его.
type Runtime struct {
	clientID     string
	clientSecret string

	users   data_base.Database[data_base.User]
	tokens  data_base.Database[data_base.Token]
	chat    *chat.Service
	viewers *viewer.Runtime
	alerts  *alerts.Service

	irc            *twitchirc.Client
	activeToken    string
	broadcaster    twitch.ChatUser
	onConnected    func(twitch.ChatUser)
	sent           map[string]time.Time
	onNotification func(subscriptionType string, raw json.RawMessage)

	http *http.Client

	mu            sync.Mutex
	ctx           context.Context
	enabled       bool
	status        string
	clientsCancel context.CancelFunc
}

func New(
	clientID string,
	clientSecret string,
	users data_base.Database[data_base.User],
	tokens data_base.Database[data_base.Token],
	chatService *chat.Service,
	viewers *viewer.Runtime,
	alertsService *alerts.Service,
) *Runtime {
	return &Runtime{
		clientID:     clientID,
		clientSecret: clientSecret,
		users:        users,
		tokens:       tokens,
		chat:         chatService,
		viewers:      viewers,
		alerts:       alertsService,
		http:         netcfg.NewHTTPClient(20 * time.Second),
		enabled:      true,
		status:       "ожидает запуска",
		sent:         make(map[string]time.Time),
	}
}

// Reconnect перезапускает IRC/EventSub с актуальным токеном из БД.
func (r *Runtime) Reconnect(ctx context.Context) error {
	r.stopClients()

	return r.connect(ctx)
}

// SetOnConnected — колбэк после успешного подключения (канал известен).
func (r *Runtime) SetOnConnected(hook func(twitch.ChatUser)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.onConnected = hook
}

// SetNotificationRouter — маршрутизация событий EventSub другим подсистемам
// (channel points, опросы, предикты). Всё, что не обработано, идёт в alerts.
func (r *Runtime) SetNotificationRouter(router func(subscriptionType string, raw json.RawMessage)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.onNotification = router
}

// IsOwnText проверяет, что сообщение недавно отправлено самим приложением.
func (r *Runtime) IsOwnText(text string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	at, ok := r.sent[text]
	if !ok {
		return false
	}

	if time.Since(at) > 20*time.Second {
		delete(r.sent, text)

		return false
	}

	return true
}

// Say отправляет сообщение через Twitch Helix, с fallback на IRC.
func (r *Runtime) Say(text string) error {
	r.mu.Lock()
	r.sent[text] = time.Now()
	r.mu.Unlock()

	if err := r.sayAPI(text); err == nil {
		return nil
	}

	r.mu.Lock()
	irc := r.irc
	r.mu.Unlock()

	if irc == nil {
		return errors.New("chat: not connected")
	}

	return irc.Say(text)
}

func (r *Runtime) sayAPI(text string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{
		"broadcaster_id": broadcasterID,
		"sender_id":      broadcasterID,
		"message":        text,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://api.twitch.tv/helix/chat/messages",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("chat messages returned %d", response.StatusCode)
	}

	return nil
}

// DeleteMessage удаляет сообщение через Helix.
func (r *Runtime) DeleteMessage(ctx context.Context, messageID string) error {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return errors.New("chat: not connected")
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/moderation/chat?broadcaster_id=%s&moderator_id=%s&message_id=%s",
		broadcasterID, broadcasterID, messageID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("moderation/chat returned %d", response.StatusCode)
	}

	return nil
}

// CreateClip создаёт клип текущего стрима и возвращает ссылку на редактирование.
type ClipInfo struct {
	ID      string
	EditURL string
	URL     string
}

func (r *Runtime) CreateClipInfo(ctx context.Context) (ClipInfo, error) {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return ClipInfo{}, errors.New("chat: not connected")
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/clips?broadcaster_id=%s",
		broadcasterID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return ClipInfo{}, err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return ClipInfo{}, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return ClipInfo{}, fmt.Errorf("clips returned %d", response.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			EditURL string `json:"edit_url"`
		} `json:"data"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ClipInfo{}, err
	}

	if len(payload.Data) == 0 {
		return ClipInfo{}, errors.New("clips: empty response")
	}

	clip := payload.Data[0]

	return ClipInfo{ID: clip.ID, EditURL: clip.EditURL, URL: "https://clips.twitch.tv/" + clip.ID}, nil
}

func (r *Runtime) CreateClip(ctx context.Context) (string, error) {
	clip, err := r.CreateClipInfo(ctx)
	if err != nil {
		return "", err
	}

	return clip.EditURL, nil
}

// FollowersCount возвращает число фолловеров.
func (r *Runtime) FollowersCount(ctx context.Context) (int, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/channels/followers?broadcaster_id=%s", broadcasterID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	var payload struct {
		Total int `json:"total"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return 0, err
	}
	return payload.Total, nil
}

// SubscribersCount возвращает число подписчиков.
func (r *Runtime) SubscribersCount(ctx context.Context) (int, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/subscriptions?broadcaster_id=%s", broadcasterID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	var payload struct {
		Total int `json:"total"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return 0, err
	}
	return payload.Total, nil
}

// ChattersCount возвращает число зрителей в чате.
func (r *Runtime) ChattersCount(ctx context.Context) (int, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return 0, err
	}
	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/chat/chatters?broadcaster_id=%s&moderator_id=%s", broadcasterID, broadcasterID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	var payload struct {
		Total int `json:"total"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return 0, err
	}
	return payload.Total, nil
}

// FollowerSince возвращает дату подписки пользователя на канал.
func (r *Runtime) FollowerSince(ctx context.Context, userID string) (string, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/channels/followers?broadcaster_id=%s&user_id=%s",
		broadcasterID, urlQueryEscape(userID),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			FollowedAt string `json:"followed_at"`
		} `json:"data"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return "", err
	}
	if len(payload.Data) == 0 {
		return "", errors.New("not following")
	}

	return payload.Data[0].FollowedAt, nil
}

// StreamStartedAt возвращает время начала текущего стрима.
func (r *Runtime) StreamStartedAt(ctx context.Context) (string, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/streams?user_id=%s", broadcasterID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			StartedAt string `json:"started_at"`
		} `json:"data"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return "", err
	}
	if len(payload.Data) == 0 {
		return "", errors.New("stream offline")
	}

	return payload.Data[0].StartedAt, nil
}

// AccountCreatedAt возвращает дату регистрации пользователя Twitch.
func (r *Runtime) AccountCreatedAt(ctx context.Context, userID string) (string, error) {
	token, _, err := r.credentials()
	if err != nil {
		return "", err
	}

	endpoint := "https://api.twitch.tv/helix/users?id=" + urlQueryEscape(userID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return "", err
	}
	if len(payload.Data) == 0 {
		return "", errors.New("user not found")
	}

	return payload.Data[0].CreatedAt, nil
}

// GetChannelInfo возвращает title и категорию канала.
func (r *Runtime) GetChannelInfo(ctx context.Context) (string, string, string, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return "", "", "", err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/channels?broadcaster_id=%s",
		broadcasterID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", "", err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			Title    string `json:"title"`
			GameName string `json:"game_name"`
			GameID   string `json:"game_id"`
		} `json:"data"`
	}

	if err := r.doJSON(request, &payload); err != nil {
		return "", "", "", err
	}

	if len(payload.Data) == 0 {
		return "", "", "", errors.New("channels: empty response")
	}

	return payload.Data[0].Title, payload.Data[0].GameName, payload.Data[0].GameID, nil
}

// UpdateChannel меняет title и/или категорию.
func (r *Runtime) UpdateChannel(ctx context.Context, title, gameID string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	body := map[string]string{"broadcaster_id": broadcasterID}

	if title != "" {
		body["title"] = title
	}

	if gameID != "" {
		body["game_id"] = gameID
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPatch,
		"https://api.twitch.tv/helix/channels",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("channels update returned %d", response.StatusCode)
	}

	return nil
}

// SearchGame ищет категорию по названию.
func (r *Runtime) SearchGame(ctx context.Context, query string) (string, string, error) {
	token, _, err := r.credentials()
	if err != nil {
		return "", "", err
	}

	endpoint := "https://api.twitch.tv/helix/search/categories?query=" + urlQueryEscape(query)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}

	if err := r.doJSON(request, &payload); err != nil {
		return "", "", err
	}

	if len(payload.Data) == 0 {
		return "", "", errors.New("категория не найдена")
	}

	return payload.Data[0].ID, payload.Data[0].Name, nil
}

// StartCommercial запускает рекламную паузу.
func (r *Runtime) StartCommercial(ctx context.Context, lengthSeconds int) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"broadcaster_id": broadcasterID,
		"length":         lengthSeconds,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.twitch.tv/helix/channels/commercial",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("commercial returned %d", response.StatusCode)
	}

	return nil
}

// GetShieldMode читает статус Shield Mode.
func (r *Runtime) GetShieldMode(ctx context.Context) (bool, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return false, err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/moderation/shield_mode?broadcaster_id=%s&moderator_id=%s",
		broadcasterID, broadcasterID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			IsActive bool `json:"is_active"`
		} `json:"data"`
	}

	if err := r.doJSON(request, &payload); err != nil {
		return false, err
	}

	if len(payload.Data) == 0 {
		return false, nil
	}

	return payload.Data[0].IsActive, nil
}

// UpdateShieldMode включает/выключает Shield Mode.
func (r *Runtime) UpdateShieldMode(ctx context.Context, active bool) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{
		"is_active": active,
	})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/moderation/shield_mode?broadcaster_id=%s&moderator_id=%s",
		broadcasterID, broadcasterID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("shield_mode returned %d", response.StatusCode)
	}

	return nil
}

// GetStreamViewers возвращает число зрителей эфира (0 — оффлайн).
func (r *Runtime) GetStreamViewers(ctx context.Context) (int, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return 0, err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/streams?user_id=%s",
		broadcasterID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			ViewerCount int `json:"viewer_count"`
		} `json:"data"`
	}

	if err := r.doJSON(request, &payload); err != nil {
		return 0, err
	}

	if len(payload.Data) == 0 {
		return 0, nil
	}

	return payload.Data[0].ViewerCount, nil
}

func (r *Runtime) credentials() (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activeToken == "" || r.broadcaster.ID == "" {
		return "", "", errors.New("chat: not connected")
	}

	return r.activeToken, r.broadcaster.ID, nil
}

func (r *Runtime) doJSON(request *http.Request, target any) error {
	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("helix returned %d", response.StatusCode)
	}

	return json.NewDecoder(response.Body).Decode(target)
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

// ChatBadges возвращает карту badge set/version -> URL картинки.
func (r *Runtime) ChatBadges(ctx context.Context) (map[string]string, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	endpoints := []string{
		"https://api.twitch.tv/helix/chat/badges/global",
		fmt.Sprintf("https://api.twitch.tv/helix/chat/badges?broadcaster_id=%s", broadcasterID),
	}

	for _, endpoint := range endpoints {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Client-Id", r.clientID)

		var payload struct {
			Data []struct {
				SetID    string `json:"set_id"`
				Versions []struct {
					ID    string `json:"id"`
					Image string `json:"image_url_1x"`
				} `json:"versions"`
			} `json:"data"`
		}
		if err := r.doJSON(request, &payload); err != nil {
			return nil, err
		}
		for _, set := range payload.Data {
			for _, version := range set.Versions {
				if set.SetID != "" && version.ID != "" && version.Image != "" {
					result[set.SetID+"/"+version.ID] = version.Image
				}
			}
		}
	}

	return result, nil
}

// ListChannelRewards возвращает управляемые награды канала.
func (r *Runtime) ListChannelRewards(ctx context.Context) ([]twitch.ChannelReward, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/channel_points/custom_rewards?broadcaster_id=%s&only_manageable_rewards=true",
		broadcasterID,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []twitch.ChannelReward `json:"data"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		return nil, err
	}

	return payload.Data, nil
}

// CreateChannelReward создаёт награду channel points.
func (r *Runtime) CreateChannelReward(ctx context.Context, title, prompt string, cost int, enabled bool) (twitch.ChannelReward, error) {
	return r.channelReward(ctx, http.MethodPost, "", map[string]any{
		"title": title, "prompt": prompt, "cost": cost, "is_enabled": enabled,
	})
}

// UpdateChannelReward обновляет награду channel points.
func (r *Runtime) UpdateChannelReward(ctx context.Context, id, title, prompt string, cost int, enabled bool) (twitch.ChannelReward, error) {
	return r.channelReward(ctx, http.MethodPatch, id, map[string]any{
		"title": title, "prompt": prompt, "cost": cost, "is_enabled": enabled,
	})
}

func (r *Runtime) channelReward(ctx context.Context, method, id string, body map[string]any) (twitch.ChannelReward, error) {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return twitch.ChannelReward{}, err
	}

	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/channel_points/custom_rewards?broadcaster_id=%s", broadcasterID)
	if id != "" {
		endpoint += "&id=" + urlQueryEscape(id)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return twitch.ChannelReward{}, err
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return twitch.ChannelReward{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	var payload struct {
		Data []twitch.ChannelReward `json:"data"`
	}
	if err := r.doJSON(request, &payload); err != nil {
		if strings.Contains(err.Error(), "403") {
			return twitch.ChannelReward{}, errors.New("Twitch 403: переподключите аккаунт — нужен scope channel:manage:redemptions")
		}

		return twitch.ChannelReward{}, err
	}
	if len(payload.Data) == 0 {
		return twitch.ChannelReward{}, errors.New("rewards: empty response")
	}

	return payload.Data[0], nil
}

// DeleteChannelReward удаляет награду channel points.
func (r *Runtime) DeleteChannelReward(ctx context.Context, id string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/channel_points/custom_rewards?broadcaster_id=%s&id=%s",
		broadcasterID, urlQueryEscape(id),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("rewards delete returned %d", response.StatusCode)
	}

	return nil
}

// FulfillRedemption помечает выкуп channel points выполненным.
func (r *Runtime) FulfillRedemption(ctx context.Context, rewardID, redemptionID string) error {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return errors.New("chat: not connected")
	}

	payload, err := json.Marshal(map[string]string{"status": "FULFILLED"})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf(
		"https://api.twitch.tv/helix/channel_points/custom_rewards/redemptions?broadcaster_id=%s&reward_id=%s&id=%s",
		broadcasterID, rewardID, redemptionID,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("redemptions update returned %d", response.StatusCode)
	}

	return nil
}

// CreatePoll создаёт нативный опрос Twitch и возвращает его id.
func (r *Runtime) CreatePoll(ctx context.Context, title string, choices []string, durationSeconds int) (string, error) {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return "", errors.New("chat: not connected")
	}

	items := make([]map[string]string, 0, len(choices))

	for _, choice := range choices {
		items = append(items, map[string]string{"title": choice})
	}

	payload, err := json.Marshal(map[string]any{
		"broadcaster_id": broadcasterID,
		"title":          title,
		"choices":        items,
		"duration":       durationSeconds,
	})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.twitch.tv/helix/polls",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return "", err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("polls create returned %d", response.StatusCode)
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return "", err
	}

	if len(parsed.Data) == 0 {
		return "", errors.New("polls: empty response")
	}

	return parsed.Data[0].ID, nil
}

// EndPoll завершает опрос (TERMINATED/ARCHIVED).
func (r *Runtime) EndPoll(ctx context.Context, pollID, status string) error {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return errors.New("chat: not connected")
	}

	payload, err := json.Marshal(map[string]string{
		"broadcaster_id": broadcasterID,
		"id":             pollID,
		"status":         status,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPatch,
		"https://api.twitch.tv/helix/polls",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("polls end returned %d", response.StatusCode)
	}

	return nil
}

// TimeoutUser выдаёт таймаут (или бан при seconds == 0) через Helix.
func (r *Runtime) TimeoutUser(ctx context.Context, userID string, seconds int, reason string) error {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return errors.New("chat: not connected")
	}

	payload, err := json.Marshal(map[string]any{
		"broadcaster_id": broadcasterID,
		"moderator_id":   broadcasterID,
		"data": map[string]any{
			"user_id":  userID,
			"duration": seconds,
			"reason":   reason,
		},
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.twitch.tv/helix/moderation/bans",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("moderation/bans returned %d", response.StatusCode)
	}

	return nil
}

// BanUser permanently bans a user in the channel.
func (r *Runtime) BanUser(ctx context.Context, userID, reason string) error {
	r.mu.Lock()
	token := r.activeToken
	broadcasterID := r.broadcaster.ID
	r.mu.Unlock()

	if token == "" || broadcasterID == "" {
		return errors.New("chat: not connected")
	}

	payload, err := json.Marshal(map[string]any{
		"broadcaster_id": broadcasterID,
		"moderator_id":   broadcasterID,
		"data": map[string]any{
			"user_id": userID,
			"reason":  reason,
		},
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.twitch.tv/helix/moderation/bans",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("moderation/bans returned %d", response.StatusCode)
	}

	return nil
}

// UserIDByLogin resolves a Twitch login to a user ID.
func (r *Runtime) UserIDByLogin(ctx context.Context, login string) (string, error) {
	token, _, err := r.credentials()
	if err != nil {
		return "", err
	}

	endpoint := "https://api.twitch.tv/helix/users?login=" + urlQueryEscape(strings.TrimPrefix(strings.TrimSpace(login), "@"))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := r.doJSON(request, &payload); err != nil {
		return "", err
	}
	if len(payload.Data) == 0 {
		return "", errors.New("пользователь не найден")
	}

	return payload.Data[0].ID, nil
}

// Announcement sends a chat announcement.
func (r *Runtime) Announcement(ctx context.Context, text string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{
		"broadcaster_id": broadcasterID,
		"moderator_id":   broadcasterID,
		"message":        text,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.twitch.tv/helix/chat/announcements", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("announcements returned %d", response.StatusCode)
	}

	return nil
}

// Shoutout sends a Twitch shoutout to a channel.
func (r *Runtime) Shoutout(ctx context.Context, login string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	targetID, err := r.UserIDByLogin(ctx, login)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{
		"from_broadcaster_id": broadcasterID,
		"to_broadcaster_id":   targetID,
		"moderator_id":        broadcasterID,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.twitch.tv/helix/chat/shoutouts", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("shoutouts returned %d", response.StatusCode)
	}

	return nil
}

// StartRaid starts a raid to a channel login.
func (r *Runtime) StartRaid(ctx context.Context, login string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	targetID, err := r.UserIDByLogin(ctx, login)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/raids?from_broadcaster_id=%s&to_broadcaster_id=%s", broadcasterID, targetID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("raids returned %d", response.StatusCode)
	}

	return nil
}

// CancelRaid cancels the current raid.
func (r *Runtime) CancelRaid(ctx context.Context) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/raids?broadcaster_id=%s", broadcasterID)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("raids cancel returned %d", response.StatusCode)
	}

	return nil
}

// CreateMarker creates a stream marker.
func (r *Runtime) CreateMarker(ctx context.Context, description string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]string{
		"user_id":     broadcasterID,
		"description": description,
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.twitch.tv/helix/streams/markers", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("markers returned %d", response.StatusCode)
	}

	return nil
}

// UnbanUser removes a ban.
func (r *Runtime) UnbanUser(ctx context.Context, userID string) error {
	token, broadcasterID, err := r.credentials()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://api.twitch.tv/helix/moderation/bans?broadcaster_id=%s&moderator_id=%s&user_id=%s", broadcasterID, broadcasterID, urlQueryEscape(userID))
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("unban returned %d", response.StatusCode)
	}

	return nil
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (r *Runtime) ID() string {
	return "chat"
}

func (r *Runtime) Name() string {
	return "Чат Twitch"
}

func (r *Runtime) Kind() string {
	return "chat"
}

func (r *Runtime) Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.enabled
}

func (r *Runtime) SetEnabled(enabled bool) error {
	r.mu.Lock()
	r.enabled = enabled
	ctx := r.ctx
	r.mu.Unlock()

	if !enabled {
		r.stopClients()
		r.setStatus("выключено")

		return nil
	}

	if ctx == nil {
		return nil
	}

	return r.connect(ctx)
}

func (r *Runtime) Status() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.status
}

/* ── Запуск ───────────────────────────────────────────────────────── */

// Start запоминает контекст и подключается к чату (если есть токен в БД).
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	r.ctx = ctx
	enabled := r.enabled
	r.mu.Unlock()

	if !enabled {
		r.setStatus("выключено")

		return nil
	}

	return r.connect(ctx)
}

func (r *Runtime) setStatus(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.status = status
}

// connect получает актуальный токен, определяет канал и запускает клиентов.
func (r *Runtime) connect(ctx context.Context) error {
	token, err := r.tokens.First()

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r.setStatus("нет токена — авторизуйте Twitch")

			return nil
		}

		r.setStatus("ошибка БД: " + err.Error())

		return err
	}

	accessToken, expiresIn := r.resolveAccessToken(ctx, token)

	broadcaster, err := r.fetchBroadcaster(ctx, accessToken)
	if err != nil {
		r.setStatus("ошибка Twitch API: " + err.Error())

		return err
	}

	r.chat.SetChannel(broadcaster.ID)
	r.launchClients(ctx, accessToken, broadcaster)
	r.setStatus("подключён: #" + broadcaster.Login)

	schedule := time.Duration(expiresIn-60) * time.Second
	if schedule < 30*time.Second {
		schedule = 30 * time.Second
	}

	time.AfterFunc(schedule, func() {
		if ctx.Err() != nil || !r.Enabled() {
			return
		}

		if err := r.connect(ctx); err != nil {
			log.Printf("[Chat] reconnect failed: %v", err)
		}
	})

	return nil
}

// resolveAccessToken обновляет refresh-токен; при сбое использует сохранённый access.
func (r *Runtime) resolveAccessToken(ctx context.Context, token *data_base.Token) (string, int) {
	expiresIn := 3600

	if token.RefreshToken == "" {
		return token.AccessToken, expiresIn
	}

	form := url.Values{}
	form.Set("client_id", r.clientID)
	form.Set("client_secret", r.clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.RefreshToken)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return token.AccessToken, expiresIn
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := r.http.Do(request)
	if err != nil {
		log.Printf("[Chat] token refresh failed: %v", err)

		return token.AccessToken, expiresIn
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		log.Printf("[Chat] token refresh returned %d", response.StatusCode)

		return token.AccessToken, expiresIn
	}

	var data tokensResponse

	if err := json.NewDecoder(response.Body).Decode(&data); err != nil || data.AccessToken == "" {
		return token.AccessToken, expiresIn
	}

	if data.RefreshToken == "" {
		data.RefreshToken = token.RefreshToken
	}

	if data.ExpiresIn > 0 {
		expiresIn = data.ExpiresIn
	}

	token.AccessToken = data.AccessToken
	token.RefreshToken = data.RefreshToken

	if _, err := r.tokens.Update(*token); err != nil {
		log.Printf("[Chat] token persist failed: %v", err)
	}

	return data.AccessToken, expiresIn
}

func (r *Runtime) fetchBroadcaster(ctx context.Context, accessToken string) (twitch.ChatUser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, usersURL, nil)
	if err != nil {
		return twitch.ChatUser{}, err
	}

	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Client-Id", r.clientID)

	response, err := r.http.Do(request)
	if err != nil {
		return twitch.ChatUser{}, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return twitch.ChatUser{}, fmt.Errorf("helix/users returned %d", response.StatusCode)
	}

	var users helixUsersResponse

	if err := json.NewDecoder(response.Body).Decode(&users); err != nil {
		return twitch.ChatUser{}, err
	}

	if len(users.Data) == 0 {
		return twitch.ChatUser{}, errors.New("twitch user not found")
	}

	user := users.Data[0]

	return twitch.ChatUser{
		ID:          user.ID,
		Login:       user.Login,
		DisplayName: user.DisplayName,
	}, nil
}

// launchClients перезапускает IRC и EventSub с актуальным токеном.
func (r *Runtime) launchClients(ctx context.Context, accessToken string, broadcaster twitch.ChatUser) {
	r.stopClients()

	runCtx, cancel := context.WithCancel(ctx)

	if r.viewers != nil {
		r.viewers.Start(runCtx, broadcaster.ID, accessToken)
	}

	r.mu.Lock()
	r.clientsCancel = cancel
	r.mu.Unlock()

	irc := twitchirc.New(twitchirc.Options{
		AccessToken: accessToken,
		Broadcaster: broadcaster,
		OnMessage:   r.chat.Receive,
	})

	r.mu.Lock()
	r.irc = irc
	r.activeToken = accessToken
	r.broadcaster = broadcaster
	hook := r.onConnected
	r.mu.Unlock()

	if hook != nil {
		hook(broadcaster)
	}

	eventSub := eventsub.New(eventsub.Options{
		AccessToken: accessToken,
		ClientID:    r.clientID,
		Broadcaster: broadcaster,
		OnMessage:   r.chat.Receive,
		OnNotification: func(subscriptionType string, event json.RawMessage) {
			if subscriptionType == "channel.chat.notification" {
				if r.viewers != nil {
					r.viewers.HandleChatNotification(event)
				}

				return
			}

			r.mu.Lock()
			router := r.onNotification
			r.mu.Unlock()

			if router != nil {
				router(subscriptionType, event)

				return
			}

			if r.alerts != nil {
				r.alerts.HandleNotification(subscriptionType, event)
			}
		},
	})

	go irc.Connect(runCtx)
	go eventSub.Connect(runCtx)
}

func (r *Runtime) stopClients() {
	r.mu.Lock()
	cancel := r.clientsCancel
	r.clientsCancel = nil
	r.mu.Unlock()

	if r.viewers != nil {
		r.viewers.Stop()
	}

	r.mu.Lock()
	r.irc = nil
	r.activeToken = ""
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}
