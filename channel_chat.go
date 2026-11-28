package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/p7x-dev/nameless-streamhata-backend/alerts"
	"github.com/p7x-dev/nameless-streamhata-backend/analytics"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/chatjury"
	"github.com/p7x-dev/nameless-streamhata-backend/chatruntime"
	"github.com/p7x-dev/nameless-streamhata-backend/clips"
	"github.com/p7x-dev/nameless-streamhata-backend/commands"
	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/games"
	"github.com/p7x-dev/nameless-streamhata-backend/integrations"
	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/moderation"
	"github.com/p7x-dev/nameless-streamhata-backend/music"
	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/polls"
	"github.com/p7x-dev/nameless-streamhata-backend/quotes"
	"github.com/p7x-dev/nameless-streamhata-backend/redemptions"
	"github.com/p7x-dev/nameless-streamhata-backend/server"
	"github.com/p7x-dev/nameless-streamhata-backend/seventv"
	"github.com/p7x-dev/nameless-streamhata-backend/stream"
	"github.com/p7x-dev/nameless-streamhata-backend/syncstate"
	"github.com/p7x-dev/nameless-streamhata-backend/timers"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/viewer"
	"github.com/p7x-dev/nameless-streamhata-backend/welcome"
	"github.com/p7x-dev/nameless-streamhata-backend/widgets"
)

type connectedChat struct {
	mu          sync.Mutex
	runtime     *chatruntime.Runtime
	history     *chat.Service
	commands    *commands.Service
	timers      *timers.Service
	points      *points.Service
	games       *games.Service
	quotes      *quotes.Service
	triggers    *triggers.Service
	moderation  *moderation.Service
	redemptions *redemptions.Service
	polls       *polls.Service
	analytics   *analytics.Service
	stream      *stream.Service
	jury        *chatjury.Service
	viewers     *viewer.Runtime
	registry    *integrations.Registry
	alerts      *alerts.Service
	widgets     *widgets.Service
	welcome     *welcome.Service
	emotes      *seventv.Service
	syncState   *syncstate.Service
	music       *music.Service
	registered  bool
}

type chatActivation struct {
	*chatruntime.Runtime
	mu     sync.Mutex
	db     *sql.DB
	ctx    context.Context
	userID int
}

func (activation *chatActivation) SetEnabled(enabled bool) error {
	activation.mu.Lock()
	defer activation.mu.Unlock()
	previous := activation.Enabled()
	if err := activation.Runtime.SetEnabled(enabled); err != nil {
		return errors.Join(err, activation.Runtime.SetEnabled(previous))
	}
	if _, err := activation.db.ExecContext(activation.ctx,
		`UPDATE twitch_chat_activation SET enabled = $2 WHERE user_id = $1`, activation.userID, enabled); err != nil {
		return errors.Join(err, activation.Runtime.SetEnabled(previous))
	}
	return nil
}

func channelRoute[T any](chats *channelChats, service func(*connectedChat) T) func(http.ResponseWriter, *http.Request) T {
	return func(response http.ResponseWriter, request *http.Request) T {
		channel, _ := chats.resolve(response, request)
		if channel == nil {
			var unavailable T
			return unavailable
		}
		return service(channel)
	}
}

func (chats *channelChats) registerEngagementRoutes(srv server.Server) {
	triggers.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *triggers.Service { return c.triggers }))
	moderation.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *moderation.Service { return c.moderation }))
	points.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *points.Service { return c.points }))
	games.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *games.Service { return c.games }))
	quotes.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *quotes.Service { return c.quotes }))
	clips.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) clips.Creator { return c.runtime }))
	redemptions.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *redemptions.Service { return c.redemptions }))
	polls.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *polls.Service { return c.polls }))
	analytics.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *analytics.Service { return c.analytics }))
	stream.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *stream.Service { return c.stream }))
	chatjury.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *chatjury.Service { return c.jury }))
	viewer.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *viewer.Runtime { return c.viewers }))
	alerts.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *alerts.Service { return c.alerts }))
	widgets.RegisterRoutes(srv, channelReadRoute(chats, func(c *connectedChat) *widgets.Service { return c.widgets }))
	welcome.RegisterRoutes(srv, channelRoute(chats, func(c *connectedChat) *welcome.Service { return c.welcome }))
	seventv.RegisterRoutes(srv, channelReadRoute(chats, func(c *connectedChat) *seventv.Service { return c.emotes }))
	syncstate.RegisterRoutes(srv, channelReadRoute(chats, func(c *connectedChat) *syncstate.Service { return c.syncState }))
	music.RegisterRoutes(srv, channelReadRoute(chats, func(c *connectedChat) *music.Service { return c.music }))
	registerWidgetTokenRoute(srv, chats)
}

func loadAutoTranslationSetting(db *sql.DB, channelID string, history *chat.Service) {
	if db == nil || history == nil || channelID == "" {
		return
	}
	var enabled bool
	if err := db.QueryRowContext(context.Background(), `SELECT auto_translation_enabled FROM chat_translation_settings WHERE channel_id=$1`, channelID).Scan(&enabled); err == nil {
		history.SetAutoTranslationEnabled(enabled)
	}
}

func channelReadRoute[T any](chats *channelChats, service func(*connectedChat) T) func(http.ResponseWriter, *http.Request) T {
	return func(response http.ResponseWriter, request *http.Request) T {
		channel, _ := chats.resolveWidgetRead(response, request)
		if channel == nil {
			var unavailable T
			return unavailable
		}
		return service(channel)
	}
}

func enableMusicTitleQueue(ctx context.Context, history *chat.Service, musicService *music.Service, runtime *chatruntime.Runtime) {
	history.SetTranslationBypass(func(message twitch.TwitchChatMessage) bool {
		addedBy := message.Author.DisplayName
		if addedBy == "" {
			addedBy = message.Author.Login
		}
		track, position, matched := musicService.QueueByTitle(message.Text, addedBy)
		login := strings.TrimSpace(message.Author.Login)
		if login == "" {
			login = strings.TrimSpace(message.Author.DisplayName)
		}
		if matched {
			acknowledgeMusicQueue(runtime, login, track, position)
			return true
		}

		query, isMusicRequest := parseMusicTitleRequest(message.Text)
		if !isMusicRequest {
			return false
		}
		go func() {
			track, position, err := musicService.SearchAndQueue(ctx, query, addedBy)
			if err != nil {
				log.Printf("[Music] title search failed: %v", err)
				if login != "" {
					if sendErr := runtime.Say(fmt.Sprintf("@%s 🎵 не удалось найти эту песню", login)); sendErr != nil {
						log.Printf("[Music] queue failure acknowledgement failed: %v", sendErr)
					}
				}
				return
			}
			acknowledgeMusicQueue(runtime, login, track, position)
		}()
		return true
	})
}

func parseMusicTitleRequest(message string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(message))
	if len(fields) < 2 {
		return "", false
	}
	prefix := strings.ToLower(fields[0])
	switch prefix {
	case "!music", "!музыка":
		query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(message), fields[0]))
		return query, len([]rune(query)) >= 3
	default:
		return "", false
	}
}

func acknowledgeMusicQueue(runtime *chatruntime.Runtime, login string, track music.Track, position int) {
	if login == "" {
		return
	}
	author := strings.TrimSpace(track.Author)
	if author == "" {
		author = "Неизвестный исполнитель"
	}
	title := strings.TrimSpace(track.Title)
	if err := runtime.Say(fmt.Sprintf("@%s 🎵 %s — %s добавлена в очередь под номером %d", login, author, title, position)); err != nil {
		log.Printf("[Music] queue acknowledgement failed: %v", err)
	}
}

type primaryChannelRoutes struct {
	server.Server
	sessions *twitch.Sessions
	runtime  *chatruntime.Runtime
}

func (routes primaryChannelRoutes) ServeEndpoint(path string, handler http.HandlerFunc) {
	routes.Server.ServeEndpoint(path, func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		user, _, err := routes.sessions.Account(request)
		if err != nil {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strconv.FormatInt(user.TwitchUserID, 10) != routes.runtime.CurrentChannelID() {
			http.Error(response, "integration belongs to another channel", http.StatusForbidden)
			return
		}
		handler(response, request)
	})
}

type channelChats struct {
	ctx        context.Context
	translator chat.LLMTranslator
	classifier channelLLM
	users      data_base.Database[data_base.User]
	tokens     data_base.Database[data_base.Token]
	sessions   *twitch.Sessions
	db         *sql.DB
	primary    *connectedChat
	ready      chan struct{}

	mu       sync.Mutex
	channels map[int64]*connectedChat
}

type channelLLM interface {
	llm.Classifier
	llm.VerdictGenerator
}

func newChannelChats(
	ctx context.Context,
	translator chat.LLMTranslator,
	classifier channelLLM,
	users data_base.Database[data_base.User],
	tokens data_base.Database[data_base.Token],
	sessions *twitch.Sessions,
	db *sql.DB,
	primary *connectedChat,
) *channelChats {
	return &channelChats{
		ctx: ctx, translator: translator, classifier: classifier, users: users, tokens: tokens, sessions: sessions, db: db,
		primary:  primary,
		ready:    make(chan struct{}),
		channels: make(map[int64]*connectedChat),
	}
}

func (chats *channelChats) resolve(response http.ResponseWriter, request *http.Request) (*connectedChat, string) {
	response.Header().Set("Cache-Control", "private, no-store")
	user, _, err := chats.sessions.Account(request)
	if err != nil {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return nil, ""
	}

	channelID := strconv.FormatInt(user.TwitchUserID, 10)
	channel, err := chats.connectUser(user)
	if err != nil {
		log.Printf("[Chat] channel %s unavailable: %v", channelID, err)
		http.Error(response, "Twitch chat unavailable", http.StatusBadGateway)
		return nil, ""
	}
	return channel, channelID
}

func (chats *channelChats) resolveWidgetRead(response http.ResponseWriter, request *http.Request) (*connectedChat, string) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(authorization) >= len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		token := strings.TrimSpace(authorization[len("Bearer "):])
		if token == "" || request.Method != http.MethodGet {
			http.Error(response, "widget token is read-only", http.StatusForbidden)
			return nil, ""
		}
		channelID, err := chats.channelForWidgetToken(request.Context(), token)
		if err != nil {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return nil, ""
		}
		user, err := chats.users.Find(data_base.Where{Column: "twitch_user_id", Value: channelID})
		if err != nil || user.Banned {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return nil, ""
		}
		channel, err := chats.connectUser(user)
		if err != nil {
			log.Printf("[Widget] channel %d unavailable: %v", channelID, err)
			http.Error(response, "Twitch chat unavailable", http.StatusBadGateway)
			return nil, ""
		}
		return channel, strconv.FormatInt(user.TwitchUserID, 10)
	}
	return chats.resolve(response, request)
}

func (chats *channelChats) channelForWidgetToken(ctx context.Context, token string) (int64, error) {
	hash := sha256.Sum256([]byte(token))
	var channelID int64
	err := chats.db.QueryRowContext(ctx,
		`SELECT channel_id FROM widget_access_tokens WHERE token_hash = $1`, hash[:],
	).Scan(&channelID)
	return channelID, err
}

func (chats *channelChats) issueWidgetToken(ctx context.Context, channelID int64) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	_, err := chats.db.ExecContext(ctx, `
		INSERT INTO widget_access_tokens (channel_id, token_hash)
		VALUES ($1, $2)
		ON CONFLICT (channel_id) DO UPDATE
		SET token_hash = EXCLUDED.token_hash, created_at = now()`, channelID, hash[:])
	if err != nil {
		return "", err
	}
	return token, nil
}

func registerWidgetTokenRoute(srv server.Server, chats *channelChats) {
	srv.ServeEndpoint("/rgg-sync/widget-token", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		user, _, err := chats.sessions.Account(request)
		if err != nil {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch request.Method {
		case http.MethodPost:
			token, err := chats.issueWidgetToken(request.Context(), user.TwitchUserID)
			if err != nil {
				log.Printf("[Widget] token issue failed for channel %d: %v", user.TwitchUserID, err)
				http.Error(response, "widget token unavailable", http.StatusInternalServerError)
				return
			}
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(response).Encode(map[string]string{"token": token})
		case http.MethodDelete:
			if _, err := chats.db.ExecContext(request.Context(), `DELETE FROM widget_access_tokens WHERE channel_id = $1`, user.TwitchUserID); err != nil {
				http.Error(response, "widget token unavailable", http.StatusInternalServerError)
				return
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (chats *channelChats) connectUser(user *data_base.User) (*connectedChat, error) {
	select {
	case <-chats.ready:
	case <-chats.ctx.Done():
		return nil, chats.ctx.Err()
	}
	channelID := strconv.FormatInt(user.TwitchUserID, 10)
	if chats.primary.runtime.CurrentChannelID() == channelID {
		return chats.primary, nil
	}
	channel := chats.forUser(user)
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if !channel.registered {
		if err := channel.timers.UseStorage(chats.db, channelID); err != nil {
			return nil, err
		}
		var enabled bool
		if err := chats.db.QueryRowContext(chats.ctx, `
			INSERT INTO twitch_chat_activation (user_id) VALUES ($1)
			ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
			RETURNING enabled`, user.ID).Scan(&enabled); err != nil {
			return nil, err
		}
		if !enabled {
			if err := channel.runtime.SetEnabled(false); err != nil {
				return nil, err
			}
		}
		if err := channel.runtime.Start(chats.ctx); err != nil {
			return nil, err
		}
		channel.registered = true
	}
	if channel.runtime.Enabled() && channel.runtime.CurrentChannelID() != channelID {
		return nil, sql.ErrNoRows
	}
	return channel, nil
}

func (chats *channelChats) restore() {
	rows, err := chats.db.QueryContext(chats.ctx, `SELECT user_id FROM twitch_chat_activation WHERE enabled`)
	if err != nil {
		log.Printf("[Chat] load active channels failed: %v", err)
		return
	}
	var users []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			log.Printf("[Chat] read active channel failed: %v", err)
			continue
		}
		users = append(users, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[Chat] read active channels failed: %v", err)
	}
	_ = rows.Close()
	for _, id := range users {
		if chats.ctx.Err() != nil {
			return
		}
		user, err := chats.users.Find(data_base.Where{Column: "id", Value: id})
		if err != nil || user.Banned || user.TwitchUserID <= 0 {
			continue
		}
		if _, err := chats.connectUser(user); err != nil {
			log.Printf("[Chat] restore Twitch user %d failed: %v", user.TwitchUserID, err)
		}
	}
}

func (chats *channelChats) forUser(user *data_base.User) *connectedChat {
	chats.mu.Lock()
	defer chats.mu.Unlock()
	if channel := chats.channels[user.TwitchUserID]; channel != nil {
		return channel
	}
	history := chat.NewService(500, chats.translator)
	channelID := strconv.FormatInt(user.TwitchUserID, 10)
	loadAutoTranslationSetting(chats.db, channelID, history)
	channelViewers := viewer.New(os.Getenv("TWITCH_CLIENT_KEY"))
	runtime := chatruntime.NewForAccount(
		os.Getenv("TWITCH_CLIENT_KEY"), os.Getenv("TWITCH_SECRET_KEY"),
		*user, chats.users, chats.tokens, history, channelViewers, chats.db,
	)
	history.SetWriter(runtime)
	channelCommands := commands.NewForChannel(runtime, chats.db, channelID)
	channelTimers := timers.New(runtime)
	channelTriggers := triggers.NewForChannel(chats.db, channelID)
	channelPoints := points.NewForChannel(runtime, chats.db, channelID)
	channelPoints.SetTriggers(channelTriggers)
	channelGames := games.New(channelPoints, runtime)
	channelGames.SetTriggers(channelTriggers)
	channelQuotes := quotes.NewForChannel(runtime, chats.db, channelID)
	channelQuotes.SetTriggers(channelTriggers)
	channelModeration := moderation.New(runtime, runtime)
	channelAlerts := alerts.New()
	channelWidgets := widgets.NewForChannel(chats.db, channelID)
	channelWelcome := welcome.NewForChannel(chats.db, runtime, channelID)
	channelEmotes := seventv.New()
	channelSync := syncstate.New()
	channelMusic := music.NewForChannel(chats.db, channelID)
	enableMusicTitleQueue(chats.ctx, history, channelMusic, runtime)
	channelPolls := polls.New(runtime)
	channelAnalytics := analytics.New(runtime)
	channelStream := stream.New(runtime)
	channelJury := chatjury.NewService(chats.classifier)
	chatjury.NewScheduler(channelJury, chats.classifier, history).SetTriggers(channelTriggers)
	channelRedemptions := redemptions.NewForChannel(channelPoints, runtime, chats.db, channelID)
	channelRedemptions.SetWheelEntry(channelGames.EnterWheelWithCost)
	channelRedemptions.SetRewards(runtime)
	channelRedemptions.SetCommandRunner(channelCommands)
	runtime.SetNotificationRouter(func(kind string, event json.RawMessage) {
		switch kind {
		case "channel.channel_points_custom_reward_redemption.add", "channel.channel_points_custom_reward_redemption.update":
			channelRedemptions.HandleNotification(kind, event)
		case "channel.poll.begin", "channel.poll.progress", "channel.poll.end",
			"channel.prediction.begin", "channel.prediction.progress", "channel.prediction.lock", "channel.prediction.end":
			channelPolls.HandleNotification(kind, event)
		default:
			channelAlerts.HandleNotification(kind, event)
		}
	})
	actioner := commandActioner{
		chat: runtime, points: channelPoints, games: channelGames, quotes: channelQuotes,
		commands: channelCommands, sync: channelSync, widgets: channelWidgets, alerts: channelAlerts,
		music: channelMusic,
	}
	channelCommands.SetActioner(actioner)
	channelCommands.SetVariableResolver(actioner.variableResolver())
	runtime.SetOnConnected(func(channel twitch.ChatUser) {
		channelTimers.SetChannel(channel.Login)
		channelEmotes.SetChannel(channel.ID)
	})
	history.Subscribe(func(message twitch.TwitchChatMessage) {
		if message.Author.ID == message.Broadcaster.ID && runtime.IsOwnText(message.Text) {
			return
		}
		channelTimers.NoteChatActivity()
		channelAnalytics.HandleMessage()
		go channelWelcome.HandleMessage(message)
		go channelModeration.HandleMessage(chats.ctx, message)
		go func() {
			switch strings.ToLower(strings.TrimSpace(message.Text)) {
			case "!np", "!now", "!nowplaying", "!сейчас":
				text, _, _ := actioner.Perform(chats.ctx, "music.current", "", message)
				_ = runtime.Say("@" + message.Author.Login + ", " + text)
				return
			}
			if action, args, ok := channelTriggers.Match(message.Text); ok {
				switch {
				case action == "music.add":
					channelMusic.HandleChatMessage(chats.ctx, message)
					return
				case action == "clips.clip" || action == "clip":
					if err := actioner.Clip(chats.ctx); err != nil {
						log.Printf("[Clips] create failed: %v", err)
					}
					return
				case commands.CanRunAtLevel(message, "moderator"):
					if text, err, handled := actioner.Perform(chats.ctx, action, strings.Join(args, " "), message); handled {
						if err != nil {
							log.Printf("[Triggers] %s failed: %v", action, err)
						} else if text != "" {
							_ = runtime.Say(text)
						}
						return
					}
				}
			}
			channelCommands.HandleMessage(message)
			channelGames.HandleMessage(message)
			channelQuotes.HandleMessage(message)
		}()
		go channelPoints.HandleMessage(message)
	})
	go channelTimers.Run(chats.ctx.Done())
	go channelAnalytics.Run(chats.ctx)
	go channelEmotes.Run(chats.ctx)
	registry := integrations.NewRegistry()
	for _, component := range []integrations.Component{
		&chatActivation{Runtime: runtime, db: chats.db, ctx: chats.ctx, userID: user.ID},
		channelCommands, channelTimers, channelPoints, channelGames, channelQuotes,
		channelTriggers, channelModeration, channelRedemptions, channelPolls, channelAnalytics, channelStream,
		channelAlerts, channelMusic,
	} {
		registry.Register(component)
	}
	channel := &connectedChat{
		runtime: runtime, history: history, commands: channelCommands, timers: channelTimers,
		points: channelPoints, games: channelGames, quotes: channelQuotes, triggers: channelTriggers,
		moderation: channelModeration, redemptions: channelRedemptions, polls: channelPolls,
		analytics: channelAnalytics, stream: channelStream, jury: channelJury, viewers: channelViewers, registry: registry,
		alerts: channelAlerts, widgets: channelWidgets, welcome: channelWelcome, emotes: channelEmotes, syncState: channelSync,
		music: channelMusic,
	}
	chats.channels[user.TwitchUserID] = channel
	return channel
}
