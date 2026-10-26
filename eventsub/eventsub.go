// Package eventsub — WebSocket-клиент Twitch EventSub для сообщений чата.
package eventsub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

const (
	eventsubURL   = "wss://eventsub.wss.twitch.tv/ws"
	reconnectWait = 3 * time.Second
	readTimeout   = 90 * time.Second
	subscribeURL  = "https://api.twitch.tv/helix/eventsub/subscriptions"
)

// Options — параметры клиента EventSub.
type Options struct {
	AccessToken string
	ClientID    string
	Broadcaster twitch.ChatUser
	OnMessage   func(twitch.TwitchChatMessage)

	// OnNotification получает события остальных подписок (watch_streak и т.п.).
	OnNotification func(subscriptionType string, event json.RawMessage)
}

// Client слушает channel.chat.message и переподключается при обрыве.
type Client struct {
	options Options
	http    *http.Client
}

func New(options Options) *Client {
	return &Client{
		options: options,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Connect(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.session(ctx)

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			log.Printf("[Twitch EventSub] %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectWait):
		}
	}
}

func (c *Client) session(ctx context.Context) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: netcfg.ProxyFunc()}

	conn, _, err := dialer.DialContext(ctx, eventsubURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	defer func() {
		_ = conn.Close()
	}()

	subscribed := false

	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var message envelope

		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}

		switch message.Metadata.MessageType {
		case "session_welcome":
			sessionID := message.Payload.Session.ID

			if sessionID == "" || subscribed {
				continue
			}

			for _, spec := range subscriptionSpecs {
				if err := c.subscribe(ctx, sessionID, spec); err != nil {
					log.Printf("[Twitch EventSub] %s subscribe failed: %v", spec.Type, err)
				}
			}

			subscribed = true

			log.Printf("[Twitch EventSub] subscribed for #%s", c.options.Broadcaster.Login)
		case "session_reconnect":
			// Простейший путь: закрываем соединение и переподключаемся к основному URL.
			return errors.New("session_reconnect received")
		case "notification":
			if c.options.OnMessage == nil {
				continue
			}

			if chatMessage, ok := mapNotification(message.Payload.Event, c.options.Broadcaster); ok {
				c.options.OnMessage(chatMessage)
			}
		}
	}
}

type subscriptionSpec struct {
	Type    string
	Version string
}

// subscriptionSpecs — что слушаем по умолчанию (чат, зрители, алерты).
var subscriptionSpecs = []subscriptionSpec{
	{Type: "channel.chat.message", Version: "1"},
	{Type: "channel.chat.notification", Version: "1"},
	{Type: "channel.follow", Version: "2"},
	{Type: "channel.subscribe", Version: "1"},
	{Type: "channel.subscription.gift", Version: "1"},
	{Type: "channel.subscription.message", Version: "1"},
	{Type: "channel.cheer", Version: "1"},
	{Type: "channel.raid", Version: "1"},
	{Type: "channel.hype_train.begin", Version: "2"},
	{Type: "channel.goal.begin", Version: "1"},
	{Type: "channel.goal.progress", Version: "1"},
	{Type: "channel.goal.end", Version: "1"},
	{Type: "channel.channel_points_custom_reward_redemption.add", Version: "1"},
	{Type: "channel.channel_points_custom_reward_redemption.update", Version: "1"},
	{Type: "channel.poll.begin", Version: "1"},
	{Type: "channel.poll.progress", Version: "1"},
	{Type: "channel.poll.end", Version: "1"},
	{Type: "channel.prediction.begin", Version: "1"},
	{Type: "channel.prediction.progress", Version: "1"},
	{Type: "channel.prediction.lock", Version: "1"},
	{Type: "channel.prediction.end", Version: "1"},
	{Type: "stream.online", Version: "1"},
	{Type: "stream.offline", Version: "1"},
}

func (c *Client) conditionFor(subscriptionType string) map[string]string {
	broadcasterID := c.options.Broadcaster.ID

	switch subscriptionType {
	case "channel.chat.message", "channel.chat.notification":
		return map[string]string{
			"broadcaster_user_id": broadcasterID,
			"user_id":             broadcasterID,
		}
	case "channel.follow":
		return map[string]string{
			"broadcaster_user_id": broadcasterID,
			"moderator_user_id":   broadcasterID,
		}
	case "channel.raid":
		return map[string]string{
			"to_broadcaster_user_id": broadcasterID,
		}
	default:
		return map[string]string{
			"broadcaster_user_id": broadcasterID,
		}
	}
}

func (c *Client) subscribe(ctx context.Context, sessionID string, spec subscriptionSpec) error {
	body, err := json.Marshal(map[string]any{
		"type":      spec.Type,
		"version":   spec.Version,
		"condition": c.conditionFor(spec.Type),
		"transport": map[string]string{
			"method":     "websocket",
			"session_id": sessionID,
		},
	})
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, subscribeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+c.options.AccessToken)
	request.Header.Set("Client-Id", c.options.ClientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusOK {
		return fmt.Errorf("subscribe failed: %d", response.StatusCode)
	}

	return nil
}

type envelope struct {
	Metadata struct {
		MessageType string `json:"message_type"`
	} `json:"metadata"`

	Payload struct {
		Subscription struct {
			Type string `json:"type"`
		} `json:"subscription"`

		Session struct {
			ID           string `json:"id"`
			ReconnectURL string `json:"reconnect_url"`
		} `json:"session"`

		Event json.RawMessage `json:"event"`
	} `json:"payload"`
}

type chatEvent struct {
	BroadcasterUserID    string `json:"broadcaster_user_id"`
	BroadcasterUserLogin string `json:"broadcaster_user_login"`
	BroadcasterUserName  string `json:"broadcaster_user_name"`

	ChatterUserID    string `json:"chatter_user_id"`
	ChatterUserLogin string `json:"chatter_user_login"`
	ChatterUserName  string `json:"chatter_user_name"`

	MessageID string `json:"message_id"`

	Message struct {
		Text      string `json:"text"`
		Fragments []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Emote *struct {
				ID string `json:"id"`
			} `json:"emote"`
		} `json:"fragments"`
	} `json:"message"`

	SentAt string `json:"sent_at"`
}

// mapNotification превращает event канала в сообщение чата.
func mapNotification(raw json.RawMessage, broadcaster twitch.ChatUser) (twitch.TwitchChatMessage, bool) {
	var event chatEvent

	if err := json.Unmarshal(raw, &event); err != nil {
		return twitch.TwitchChatMessage{}, false
	}

	if event.ChatterUserLogin == "" || event.MessageID == "" {
		return twitch.TwitchChatMessage{}, false
	}

	sentAt := event.SentAt

	if sentAt == "" {
		sentAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	return twitch.TwitchChatMessage{
		MessageID:   event.MessageID,
		Broadcaster: broadcaster,
		Author: twitch.ChatUser{
			ID:          event.ChatterUserID,
			Login:       event.ChatterUserLogin,
			DisplayName: event.ChatterUserName,
		},
		Text:   event.Message.Text,
		SentAt: sentAt,
		Emotes: mapFragments(event.Message.Fragments),
	}, true
}

// mapFragments считает позиции эмотов по фрагментам EventSub (Unicode-индексы).
func mapFragments(fragments []struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emote *struct {
		ID string `json:"id"`
	} `json:"emote"`
}) []twitch.ChatEmote {
	if len(fragments) == 0 {
		return nil
	}

	offset := 0
	result := make([]twitch.ChatEmote, 0, 2)

	for _, fragment := range fragments {
		length := len([]rune(fragment.Text))

		if fragment.Type == "emote" && fragment.Emote != nil && length > 0 {
			result = append(result, twitch.ChatEmote{
				ID:       fragment.Emote.ID,
				Name:     fragment.Text,
				Start:    offset,
				End:      offset + length - 1,
				Provider: "twitch",
			})
		}

		offset += length
	}

	return result
}
