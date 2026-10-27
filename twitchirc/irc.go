// Package twitchirc — IRC-over-WebSocket клиент чата Twitch.
package twitchirc

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

const (
	ircURL        = "wss://irc-ws.chat.twitch.tv:443"
	reconnectWait = 3 * time.Second
	readTimeout   = 10 * time.Minute
)

var privmsgPattern = regexp.MustCompile(`^:([^!]+)![^ ]* PRIVMSG #[^ ]+ :(.*)$`)

// Options — параметры подключения к чату канала.
type Options struct {
	AccessToken string
	Broadcaster twitch.ChatUser
	OnMessage   func(twitch.TwitchChatMessage)
}

// Client держит одно соединение и переподключается при обрыве.
type Client struct {
	options Options

	mu   sync.Mutex
	conn *websocket.Conn
}

func New(options Options) *Client {
	return &Client{options: options}
}

// Connect блокируется до отмены контекста, переподключаясь при ошибках.
func (c *Client) Connect(ctx context.Context) {
	for ctx.Err() == nil {
		err := c.session(ctx)

		if ctx.Err() != nil {
			return
		}

		if err != nil {
			log.Printf("[Twitch IRC] %v", err)
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

	conn, _, err := dialer.DialContext(ctx, ircURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	defer func() {
		_ = conn.Close()
	}()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()

		if c.conn == conn {
			c.conn = nil
		}

		c.mu.Unlock()
	}()

	handshake := []string{
		"PASS oauth:" + c.options.AccessToken,
		"NICK " + c.options.Broadcaster.Login,
		"CAP REQ :twitch.tv/tags twitch.tv/commands",
		"JOIN #" + c.options.Broadcaster.Login,
	}

	for _, line := range handshake {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			return err
		}
	}

	log.Printf("[Twitch IRC] connected to #%s", c.options.Broadcaster.Login)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		for _, line := range strings.Split(string(payload), "\r\n") {
			if line == "" {
				continue
			}

			if err := c.handleLine(conn, line); err != nil {
				return err
			}
		}
	}
}

// Say отправляет PRIVMSG в канал.
func (c *Client) Say(text string) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("irc: not connected")
	}

	return conn.WriteMessage(
		websocket.TextMessage,
		[]byte(fmt.Sprintf("PRIVMSG #%s :%s", c.options.Broadcaster.Login, text)),
	)
}

func (c *Client) handleLine(conn *websocket.Conn, line string) error {
	if strings.HasPrefix(line, "PING") {
		return conn.WriteMessage(websocket.TextMessage, []byte("PONG :tmi.twitch.tv"))
	}

	if strings.Contains(line, " RECONNECT") {
		return fmt.Errorf("server requested reconnect")
	}

	message, ok := ParsePrivmsg(line, c.options.Broadcaster)

	if !ok {
		return nil
	}

	if c.options.OnMessage != nil {
		c.options.OnMessage(message)
	}

	return nil
}

// ParsePrivmsg разбирает IRC-строку PRIVMSG (с tags) в сообщение чата.
func ParsePrivmsg(line string, broadcaster twitch.ChatUser) (twitch.TwitchChatMessage, bool) {
	tags := map[string]string{}
	rest := line

	if strings.HasPrefix(rest, "@") {
		space := strings.Index(rest, " ")

		if space == -1 {
			return twitch.TwitchChatMessage{}, false
		}

		for _, item := range strings.Split(rest[1:space], ";") {
			separator := strings.Index(item, "=")

			if separator == -1 {
				tags[item] = ""

				continue
			}

			tags[item[:separator]] = item[separator+1:]
		}

		rest = rest[space+1:]
	}

	match := privmsgPattern.FindStringSubmatch(rest)

	if len(match) < 3 {
		return twitch.TwitchChatMessage{}, false
	}

	login := match[1]
	displayName := tags["display-name"]
	authorID := tags["user-id"]

	if displayName == "" {
		displayName = login
	}

	if authorID == "" {
		authorID = login
	}

	sentAt := time.Now().UTC()

	if timestamp := tags["tmi-sent-ts"]; timestamp != "" {
		if millis, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
			sentAt = time.UnixMilli(millis).UTC()
		}
	}

	messageID := tags["id"]
	if messageID == "" {
		messageID = fmt.Sprintf("irc-%s-%d", login, sentAt.UnixNano())
	}

	text := match[2]

	return twitch.TwitchChatMessage{
		MessageID:   messageID,
		Broadcaster: broadcaster,
		Author: twitch.ChatUser{
			ID:          authorID,
			Login:       login,
			DisplayName: displayName,
		},
		Text:   text,
		SentAt: sentAt.Format(time.RFC3339Nano),
		Color:  tags["color"],
		Emotes: ParseEmotes(tags["emotes"], text),
		Badges: ParseBadges(tags["badges"]),
	}, true
}

// ParseBadges разбирает IRC-тег badges="set/version,set/version".
func ParseBadges(raw string) []twitch.ChatBadge {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	badges := make([]twitch.ChatBadge, 0, len(parts))

	for _, part := range parts {
		setID, version, ok := strings.Cut(strings.TrimSpace(part), "/")
		if !ok || setID == "" || version == "" {
			continue
		}

		badges = append(badges, twitch.ChatBadge{SetID: setID, Version: version})
	}

	if len(badges) == 0 {
		return nil
	}

	return badges
}

// ParseEmotes разбирает IRC-тег emotes="id:start-end,..." с учётом Unicode
// (позиции Twitch — в кодовых точках, а не в байтах).
func ParseEmotes(raw, text string) []twitch.ChatEmote {
	if raw == "" || text == "" {
		return nil
	}

	runes := []rune(text)
	result := make([]twitch.ChatEmote, 0, 4)

	for _, group := range strings.Split(raw, "/") {
		parts := strings.SplitN(group, ":", 2)

		if len(parts) != 2 {
			continue
		}

		emoteID := parts[0]

		for _, span := range strings.Split(parts[1], ",") {
			bounds := strings.SplitN(span, "-", 2)

			if len(bounds) != 2 {
				continue
			}

			start, err1 := strconv.Atoi(bounds[0])
			end, err2 := strconv.Atoi(bounds[1])

			if err1 != nil || err2 != nil || start < 0 || end < start || end >= len(runes) {
				continue
			}

			result = append(result, twitch.ChatEmote{
				ID:       emoteID,
				Name:     string(runes[start : end+1]),
				Start:    start,
				End:      end,
				Provider: "twitch",
			})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Start < result[j].Start })

	return result
}
