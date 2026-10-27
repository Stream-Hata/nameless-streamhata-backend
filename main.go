package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/actions"
	"github.com/p7x-dev/nameless-streamhata-backend/alerts"
	"github.com/p7x-dev/nameless-streamhata-backend/analytics"
	"github.com/p7x-dev/nameless-streamhata-backend/appdownload"
	"github.com/p7x-dev/nameless-streamhata-backend/catalog"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/chatjury"
	"github.com/p7x-dev/nameless-streamhata-backend/chatruntime"
	"github.com/p7x-dev/nameless-streamhata-backend/clips"
	"github.com/p7x-dev/nameless-streamhata-backend/commands"
	"github.com/p7x-dev/nameless-streamhata-backend/credentials"
	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/desktopauth"
	"github.com/p7x-dev/nameless-streamhata-backend/donations"
	"github.com/p7x-dev/nameless-streamhata-backend/events"
	"github.com/p7x-dev/nameless-streamhata-backend/games"
	"github.com/p7x-dev/nameless-streamhata-backend/integrations"
	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/lots"
	"github.com/p7x-dev/nameless-streamhata-backend/media"
	"github.com/p7x-dev/nameless-streamhata-backend/metrics"
	"github.com/p7x-dev/nameless-streamhata-backend/moderation"
	"github.com/p7x-dev/nameless-streamhata-backend/music"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"github.com/p7x-dev/nameless-streamhata-backend/notifications"
	"github.com/p7x-dev/nameless-streamhata-backend/notify"
	"github.com/p7x-dev/nameless-streamhata-backend/obs"
	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/polls"
	"github.com/p7x-dev/nameless-streamhata-backend/quotes"
	"github.com/p7x-dev/nameless-streamhata-backend/redemptions"
	"github.com/p7x-dev/nameless-streamhata-backend/roulette"
	"github.com/p7x-dev/nameless-streamhata-backend/server"
	"github.com/p7x-dev/nameless-streamhata-backend/seventv"
	"github.com/p7x-dev/nameless-streamhata-backend/steam"
	"github.com/p7x-dev/nameless-streamhata-backend/stream"
	"github.com/p7x-dev/nameless-streamhata-backend/suggestions"
	"github.com/p7x-dev/nameless-streamhata-backend/syncstate"
	"github.com/p7x-dev/nameless-streamhata-backend/timers"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/vaultsecrets"
	"github.com/p7x-dev/nameless-streamhata-backend/viewer"
	"github.com/p7x-dev/nameless-streamhata-backend/widgets"
)

// webhookProviders — сервисы, которые шлют донаты вебхуками на наш URL.
var webhookProviders = []string{"donationto", "donatestream", "donatepay", "generic"}

func main() {
	if err := credentials.Load(); err != nil {
		log.Printf("[credentials] load failed: %v", err)
	}

	if err := vaultsecrets.Load(); err != nil {
		log.Printf("[vault] load failed, using local credentials: %v", err)
	} else {
		log.Printf("[vault] credentials loaded")
	}

	if err := godotenv.Load(".env"); err != nil {
		log.Printf("[config] .env not found, using process environment")
	}

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	srv := server.NewServer(ctx)
	appdownload.RegisterRoutes(srv)
	go func() {
		if err := srv.Listen(); err != nil {
			log.Fatal(err)
		}
	}()

	if err := data_base.Migrate(); err != nil {
		log.Fatal(err)
	}

	useRepo, err := data_base.NewRepository[data_base.User](ctx)
	if err != nil {
		log.Fatal(err)
	}

	tokenRepo, err := data_base.NewRepository[data_base.Token](ctx)
	if err != nil {
		log.Fatal(err)
	}

	twitch.TwitchAuth(srv, useRepo, tokenRepo)

	// Шина чата: IRC/EventSub рантайм пишет, подписчики (музыка, команды,
	// жюри) — читают.
	chatService := chat.NewService(500)

	musicDB, err := data_base.NewConnection(ctx)
	if err != nil {
		log.Printf("[Music] running without persistence: %v", err)
		musicDB = nil
	}

	eventsService := events.New(musicDB)
	lots.RegisterRoutes(srv)
	events.RegisterRoutes(srv, eventsService)

	widgetsService := widgets.New(musicDB)
	widgets.RegisterRoutes(srv, widgetsService)

	catalogService := catalog.New(musicDB)
	catalogService.RegisterRoutes(srv)

	rouletteService := roulette.New(musicDB, catalogService)
	rouletteService.RegisterRoutes(srv)

	accessService := access.New(musicDB)
	access.RegisterRoutes(srv, accessService)

	suggestionsService := suggestions.New(musicDB, accessService)
	suggestionsService.RegisterRoutes(srv)

	desktopAuthService := desktopauth.New(musicDB)
	desktopAuthService.RegisterRoutes(srv)

	notificationsService := notifications.New(musicDB)
	notificationsService.RegisterRoutes(srv)

	steamService := steam.New()
	steam.RegisterRoutes(srv, steamService)
	steam.NewLinkService(musicDB, steamService, os.Getenv("PUBLIC_URL")).Register(srv)

	networkProvider := netcfg.Init(musicDB)
	netcfg.RegisterRoutes(srv, networkProvider)

	triggersService := triggers.New(musicDB)
	triggers.RegisterRoutes(srv, triggersService)

	musicService := music.NewService(musicDB)
	music.RegisterRoutes(srv, musicService)
	chat.RegisterRoutes(srv, chatService)

	// Чат-жюри: классификатор и вердикты — Groq (при наличии ключа).
	llmClient := llm.NewGroq(os.Getenv("GROQ_API_KEY"), os.Getenv("GROQ_MODEL"))

	juryService := chatjury.NewService(llmClient)
	juryScheduler := chatjury.NewScheduler(juryService, llmClient, chatService)
	juryScheduler.SetTriggers(triggersService)
	chatjury.RegisterRoutes(srv, juryService)

	donationsService := registerDonations(srv, ctx, musicDB)

	viewerRuntime := viewer.New(os.Getenv("TWITCH_CLIENT_KEY"))
	viewer.RegisterRoutes(srv, viewerRuntime)

	syncService := syncstate.New()
	syncstate.RegisterRoutes(srv, syncService)
	media.RegisterRoutes(srv)

	alertsService := alerts.New()
	alerts.RegisterRoutes(srv, alertsService)

	sevenTVService := seventv.New()
	seventv.RegisterRoutes(srv, sevenTVService)
	go sevenTVService.Run(ctx)

	notifyService := notify.New()
	notify.RegisterRoutes(srv, notifyService)
	musicService.SetTelegramSender(notifyService.SendTelegramText)

	obsClient := obs.New()
	obs.RegisterRoutes(srv, obsClient)
	obs.AutoConnect(obsClient)

	chatRuntime := chatruntime.New(
		os.Getenv("TWITCH_CLIENT_KEY"),
		os.Getenv("TWITCH_SECRET_KEY"),
		useRepo,
		tokenRepo,
		chatService,
		viewerRuntime,
		alertsService,
	)

	ttsService := donations.NewTTS(musicDB, chatRuntime)
	ttsService.SetStatsProvider(func() []string {
		stats := donationsService.Bus().Stats()
		result := make([]string, 0, len(stats.Top))

		for _, entry := range stats.Top {
			result = append(result, fmt.Sprintf("%s — %g %s (%d)", entry.Username, entry.Amount, entry.Currency, entry.Count))

			if len(result) >= 3 {
				break
			}
		}

		return result
	})
	donations.RegisterTTSRoutes(srv, donationsService, ttsService)

	commandsService := commands.New(chatRuntime, musicDB)
	commands.RegisterRoutes(srv, commandsService)

	timersService := timers.New(chatRuntime)
	timers.RegisterRoutes(srv, timersService)
	go timersService.Run(ctx.Done())

	moderationService := moderation.New(chatRuntime, chatRuntime)
	moderation.RegisterRoutes(srv, moderationService)

	clips.RegisterRoutes(srv, chatRuntime)

	// Фаза 2: баллы, игры, цитаты, channel points, опросы.
	pointsService := points.New(chatRuntime, musicDB)
	pointsService.SetTriggers(triggersService)
	points.RegisterRoutes(srv, pointsService)

	gamesService := games.New(pointsService, chatRuntime)
	gamesService.SetTriggers(triggersService)
	games.RegisterRoutes(srv, gamesService)

	quotesService := quotes.New(chatRuntime, musicDB)
	quotesService.SetTriggers(triggersService)
	quotes.RegisterRoutes(srv, quotesService)

	commandsService.SetActioner(commandActioner{points: pointsService, games: gamesService, chat: chatRuntime, music: musicService, quotes: quotesService})
	commandsService.SetVariableResolver(func(name string) string {
		switch name {
		case "lastmusic", "lasttrack", "np", "now":
			track := musicService.Current()
			if track == nil {
				if tracks := musicService.List(); len(tracks) > 0 {
					track = &tracks[0]
				}
			}
			if track != nil {
				return track.URL
			}
		}

		return ""
	})

	redemptionsService := redemptions.New(pointsService, chatRuntime, musicDB)
	redemptionsService.SetWheelEntry(gamesService.EnterWheelWithCost)
	redemptionsService.SetRewards(chatRuntime)
	redemptionsService.SetCommandRunner(commandsService)
	redemptions.RegisterRoutes(srv, redemptionsService)

	pollsService := polls.New(chatRuntime)
	polls.RegisterRoutes(srv, pollsService)

	analyticsService := analytics.New(chatRuntime)
	analytics.RegisterRoutes(srv, analyticsService)
	go analyticsService.Run(ctx)

	streamService := stream.New(chatRuntime)
	stream.RegisterRoutes(srv, streamService)

	metricsService := metrics.New(analyticsService, donationsService.Bus(), pointsService, musicService)
	metricsService.SetTelegramSender(notifyService.SendTelegramText)
	metrics.RegisterRoutes(srv, metricsService)

	registryActions := actions.New()
	actions.RegisterRoutes(srv, registryActions)

	// Хуки вовлечения и уведомлений.
	viewerRuntime.SetOnTick(func(viewers []viewer.Viewer) {
		pointsService.HandleViewers(viewers)
		analyticsService.HandleChatters(len(viewers))
	})
	viewerRuntime.SetOnStreak(pointsService.HandleStreak)

	alertsService.AddListener(pointsService.HandleAlert)
	alertsService.AddListener(func(event alerts.Event) {
		switch event.Type {
		case "stream_online":
			analyticsService.StreamOnline()
			notifyService.StreamEvent(true, event.Message)
		case "stream_offline":
			analyticsService.StreamOffline()
			notifyService.StreamEvent(false, "")

			if notifyService.GetSettings().Digest {
				go func() { _ = metricsService.SendDigestTelegram(context.Background()) }()
			}
		default:
			notifyService.AlertEvent(event.Type, event.Username, event.Message)
		}
	})

	// Регистрация действий для деков.
	registryActions.Register("clip", "Создать клип", func(ctx context.Context, _ map[string]string) (string, error) {
		return chatRuntime.CreateClip(ctx)
	})
	registryActions.Register("obs.scene", "OBS: сменить сцену (scene)", func(_ context.Context, params map[string]string) (string, error) {
		scene := params["scene"]

		if err := obsClient.SetScene(context.Background(), scene); err != nil {
			return "", err
		}

		return "сцена: " + scene, nil
	})
	registryActions.Register("obs.mute", "OBS: mute/unmute источника (input)", func(_ context.Context, params map[string]string) (string, error) {
		muted, err := obsClient.ToggleMute(context.Background(), params["input"])

		if err != nil {
			return "", err
		}

		if muted {
			return "замьючен: " + params["input"], nil
		}

		return "включён звук: " + params["input"], nil
	})
	registryActions.Register("obs.replay", "OBS: сохранить реплей", func(_ context.Context, _ map[string]string) (string, error) {
		if err := obsClient.OutputAction(context.Background(), "replay", "save"); err != nil {
			return "", err
		}

		return "реплей сохранён", nil
	})
	registryActions.Register("stream.title", "Twitch: сменить заголовок (title, gameQuery)", func(_ context.Context, params map[string]string) (string, error) {
		info, err := streamService.Update(context.Background(), params["title"], params["gameQuery"])

		if err != nil {
			return "", err
		}

		return "теперь: " + info.Title, nil
	})
	registryActions.Register("stream.shield", "Twitch: Shield Mode (active=true/false)", func(_ context.Context, params map[string]string) (string, error) {
		active := params["active"] == "true"

		if err := streamService.SetShield(context.Background(), active); err != nil {
			return "", err
		}

		return "shield mode обновлён", nil
	})

	chatRuntime.SetNotificationRouter(func(subscriptionType string, raw json.RawMessage) {
		switch subscriptionType {
		case "stream.online", "stream.offline":
			alertsService.HandleNotification(subscriptionType, raw)
		case "channel.channel_points_custom_reward_redemption.add",
			"channel.channel_points_custom_reward_redemption.update":
			redemptionsService.HandleNotification(subscriptionType, raw)
		case "channel.poll.begin", "channel.poll.progress", "channel.poll.end",
			"channel.prediction.begin", "channel.prediction.progress",
			"channel.prediction.lock", "channel.prediction.end":
			pollsService.HandleNotification(subscriptionType, raw)
		default:
			alertsService.HandleNotification(subscriptionType, raw)
		}
	})

	chatRuntime.SetOnConnected(func(user twitch.ChatUser) {
		timersService.SetChannel(user.Login)
		sevenTVService.SetChannel(user.ID)
	})

	if err := chatRuntime.Start(ctx); err != nil {
		log.Printf("[Chat] runtime start failed: %v", err)
	}

	// Конвейер чата: активность таймеров, команды, модерация, баллы, игры.
	chatService.Subscribe(func(message twitch.TwitchChatMessage) {
		if chatRuntime.IsOwnText(message.Text) {
			return
		}

		timersService.NoteChatActivity()

		if action, _, ok := triggersService.Match(message.Text); ok && action == "music.add" {
			go musicService.HandleChatMessage(ctx, message)

			return
		}

		if link, _, ok := music.ExtractLink(message.Text); ok {
			command := triggersService.CommandFor("music.add")
			if command == "" {
				command = "!music"
			}

			go func() {
				_ = chatRuntime.Say(fmt.Sprintf("Чтобы добавить музыку, отправь: %s %s", command, link))
			}()
		}

		switch strings.ToLower(strings.TrimSpace(message.Text)) {
		case "!np", "!now", "!nowplaying", "!сейчас":
			go func() {
				track := musicService.Current()
				if track == nil {
					_ = chatRuntime.Say("@" + message.Author.Login + ", Сейчас ничего не играет")

					return
				}

				_ = chatRuntime.Say("@" + message.Author.Login + ", Сейчас играет: " + track.URL)
			}()

			return
		}

		go commandsService.HandleMessage(message)
		analyticsService.HandleMessage()
		go moderationService.HandleMessage(ctx, message)

		if action, _, ok := triggersService.Match(message.Text); ok && action == "clips.clip" {
			go func() {
				editURL, err := chatRuntime.CreateClip(ctx)

				if err != nil || editURL == "" {
					return
				}

				_ = chatRuntime.Say("Клип: " + editURL)
			}()
		}
		go ttsService.HandleMessage(message)
		go pointsService.HandleMessage(message)
		go gamesService.HandleMessage(message)
		go quotesService.HandleMessage(message)
	})

	srv.ServeEndpoint("/rgg-sync/twitch/reconnect", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if err := chatRuntime.Reconnect(request.Context()); err != nil {
			http.Error(response, err.Error(), http.StatusBadGateway)

			return
		}

		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write([]byte(`{"ok":true}`))
	})

	srv.ServeEndpoint("/rgg-sync/chat/badges", func(response http.ResponseWriter, request *http.Request) {
		badges, err := chatRuntime.ChatBadges(request.Context())
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadGateway)

			return
		}

		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(response).Encode(map[string]any{"badges": badges})
	})

	registry := integrations.NewRegistry()
	registry.Register(chatRuntime)
	registry.Register(donationsService)
	registry.Register(musicService)
	registry.Register(alertsService)
	registry.Register(commandsService)
	registry.Register(timersService)
	registry.Register(moderationService)
	registry.Register(pointsService)
	registry.Register(gamesService)
	registry.Register(quotesService)
	registry.Register(redemptionsService)
	registry.Register(pollsService)
	registry.Register(obsClient)
	registry.Register(streamService)
	registry.Register(notifyService)
	registry.Register(analyticsService)
	registry.Register(registryActions)
	registry.Register(sevenTVService)
	registry.Register(triggersService)
	registry.Register(metricsService)
	integrations.RegisterRoutes(srv, registry)

	<-ctx.Done()

	log.Println("Shutting down server...")

	musicService.SetEnabled(false)
	donationsService.StopAll()

	if err := srv.Shutdown(); err != nil {
		log.Fatal(err)
	}
}

// registerDonations собирает провайдеров донатов и вешает HTTP-ручки.
func registerDonations(srv server.Server, ctx context.Context, db *sql.DB) *donations.Service {
	bus := donations.NewBus(200)
	service := donations.NewService(bus)

	daRedirect := strings.TrimSpace(os.Getenv("DONATIONALERTS_REDIRECT_URI"))
	if daRedirect == "" || strings.Contains(daRedirect, "127.0.0.1") || strings.Contains(daRedirect, "localhost") {
		daRedirect = "https://streamhata.rdpctd.dev/rgg-sync/donations/donationalerts/callback"
	}

	daToken := strings.TrimSpace(os.Getenv("DONATIONALERTS_ACCESS_TOKEN"))
	if daToken == "" {
		daToken = strings.TrimSpace(os.Getenv("DONATIONALERTS_API_KEY"))
	}

	storedAccess, storedRefresh := donations.LoadDonationAlertsTokens(db)
	if daToken == "" {
		daToken = storedAccess
	}

	refreshToken := strings.TrimSpace(os.Getenv("DONATIONALERTS_REFRESH_TOKEN"))
	if refreshToken == "" {
		refreshToken = storedRefresh
	}

	service.Register(donations.NewDonationAlerts(donations.DonationAlertsConfig{
		ClientID:     os.Getenv("DONATIONALERTS_CLIENT_ID"),
		ClientSecret: os.Getenv("DONATIONALERTS_CLIENT_SECRET"),
		RedirectURI:  daRedirect,
		AccessToken:  daToken,
		RefreshToken: refreshToken,
		OnTokens: func(access, refresh string) {
			donations.SaveDonationAlertsTokens(db, access, refresh)
		},
		OnGoal: service.PublishGoal,
	}))

	service.Register(donations.NewWebhookProvider("kofi", os.Getenv("KOFI_VERIFICATION_TOKEN")))

	for _, name := range webhookProviders {
		secret := os.Getenv("DONATIONS_WEBHOOK_SECRET_" + strings.ToUpper(name))
		provider := donations.NewWebhookProvider(name, secret)

		if name == "donatestream" {
			provider.SetConfirm(os.Getenv("DONATIONS_DONATESTREAM_CONFIRM"))
		}

		service.Register(provider)
	}

	donations.RegisterRoutes(srv, service)
	service.StartAll(ctx)

	return service
}

type commandActioner struct {
	points *points.Service
	games  *games.Service
	chat   *chatruntime.Runtime
	music  *music.Service
	quotes *quotes.Service
}

func (a commandActioner) AwardPoints(userID, login, displayName string, amount int, reason string) error {
	a.points.Add(userID, login, displayName, amount, reason)

	return nil
}

func (a commandActioner) TimeoutUser(ctx context.Context, userID string, seconds int, reason string) error {
	return a.chat.TimeoutUser(ctx, userID, seconds, reason)
}

func (a commandActioner) WheelEntry(userID, login, displayName, title string, bet int) error {
	return a.games.EnterWheelWithCost(userID, login, displayName, title, false, bet)
}

func (a commandActioner) AddMusic(ctx context.Context, url, addedBy string) error {
	if videoID, ok := music.ExtractVideoID(url); ok {
		_, err := a.music.Add(ctx, videoID, "https://www.youtube.com/watch?v="+videoID, addedBy)

		return err
	}

	if link, kind, ok := music.ExtractLink(url); ok {
		_, err := a.music.AddMedia(link, kind, addedBy)

		return err
	}

	return nil
}

func (a commandActioner) Twitch(ctx context.Context, action, userID string) string {
	switch action {
	case "twitch.followage":
		value, err := a.chat.FollowerSince(ctx, userID)
		if err == nil {
			return "Фолловер с " + value
		}
	case "twitch.accountage":
		value, err := a.chat.AccountCreatedAt(ctx, userID)
		if err == nil {
			return "Аккаунт создан " + value
		}
	case "twitch.uptime":
		value, err := a.chat.StreamStartedAt(ctx)
		if err == nil {
			return "Стрим идёт с " + value
		}
	case "twitch.viewers":
		value, err := a.chat.GetStreamViewers(ctx)
		if err == nil {
			return fmt.Sprintf("Зрителей: %d", value)
		}
	case "twitch.followers":
		value, err := a.chat.FollowersCount(ctx)
		if err == nil {
			return fmt.Sprintf("Фолловеров: %d", value)
		}
	case "twitch.subscribers":
		value, err := a.chat.SubscribersCount(ctx)
		if err == nil {
			return fmt.Sprintf("Подписчиков: %d", value)
		}
	case "twitch.chatters":
		value, err := a.chat.ChattersCount(ctx)
		if err == nil {
			return fmt.Sprintf("В чате: %d", value)
		}
	case "points.balance":
		return a.points.BalanceText(twitch.TwitchChatMessage{Author: twitch.ChatUser{ID: userID, Login: userID, DisplayName: userID}})
	case "points.top":
		return a.points.TopText()
	case "points.shop":
		return a.points.ShopText()
	case "quotes.random":
		return a.quotes.RandomText()
	case "twitch.title", "twitch.game":
		title, game, _, err := a.chat.GetChannelInfo(ctx)
		if err == nil {
			if action == "twitch.title" {
				return "Заголовок: " + title
			}
			return "Категория: " + game
		}
	}

	return ""
}

func (a commandActioner) Clip(ctx context.Context) error {
	url, err := a.chat.CreateClip(ctx)
	if err != nil {
		return err
	}

	return a.chat.Say("Клип: " + url)
}
