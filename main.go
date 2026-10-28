package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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

	actioner := commandActioner{points: pointsService, games: gamesService, chat: chatRuntime, music: musicService, quotes: quotesService, sync: syncService, commands: commandsService, widgets: widgetsService, alerts: alertsService, roulette: rouletteService}
	commandsService.SetActioner(actioner)
	diceFormula := func(value string, message twitch.TwitchChatMessage) string {
		raw := strings.ToLower(strings.TrimSpace(value))
		if fields := strings.Fields(message.Text); len(fields) > 1 {
			raw = strings.ToLower(strings.TrimSpace(fields[1]))
		}
		count, sides := 1, 6
		if _, err := fmt.Sscanf(raw, "%dd%d", &count, &sides); err != nil {
			if _, err := fmt.Sscanf(raw, "d%d", &sides); err != nil {
				return "1d6"
			}
		}
		if count < 1 || count > 20 || sides < 1 || sides > 1000 {
			return "1d6"
		}
		return raw
	}
	diceFormulaBound := func(value string, message twitch.TwitchChatMessage) string {
		raw := strings.ToLower(strings.TrimSpace(value))
		count, sides := 1, 6
		if _, err := fmt.Sscanf(raw, "%dd%d", &count, &sides); err == nil && count >= 1 && count <= 20 && sides >= 1 && sides <= 1000 {
			return raw
		}
		if _, err := fmt.Sscanf(raw, "d%d", &sides); err == nil && sides >= 1 && sides <= 1000 {
			return raw
		}

		return diceFormula(value, message)
	}
	rollDiceFormula := func(formula string) int {
		count, sides := 1, 6
		if _, err := fmt.Sscanf(formula, "%dd%d", &count, &sides); err != nil {
			if _, err := fmt.Sscanf(formula, "d%d", &sides); err != nil {
				count, sides = 1, 6
			}
		}
		total := 0
		for i := 0; i < count; i++ {
			total += rand.Intn(sides) + 1
		}
		return total
	}
	commandsService.SetVariableResolver(func(name, value string, message twitch.TwitchChatMessage) string {
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
		case "date":
			return time.Now().Format("02.01.2006")
		case "time":
			return time.Now().Format("15:04")
		case "datetime":
			return time.Now().Format("02.01.2006 15:04")
		case "timestamp":
			return strconv.FormatInt(time.Now().Unix(), 10)
		case "random":
			if minimum, maximum, ok := parseRange(value); ok {
				return strconv.Itoa(minimum + rand.Intn(maximum-minimum+1))
			}
			options := strings.Split(value, "|")
			clean := options[:0]
			for _, item := range options {
				if item = strings.TrimSpace(item); item != "" {
					clean = append(clean, item)
				}
			}
			if len(clean) > 0 {
				return clean[rand.Intn(len(clean))]
			}
			return strconv.Itoa(rand.Intn(100) + 1)
		case "dice.name", "dicename":
			return diceFormula(value, message)
		case "dice.result", "number":
			formula := diceFormulaBound(value, message)
			if state := syncService.GetDice(); state.Formula == formula && time.Since(state.RolledAt) < 5*time.Second {
				return strconv.Itoa(state.Result)
			}

			return strconv.Itoa(rollDiceFormula(formula))
		case "dice":
			return strconv.Itoa(rollDiceFormula(diceFormulaBound(value, message)))
		case "coinflip":
			if rand.Intn(2) == 0 {
				return "орёл"
			}
			return "решка"
		case "points.balance":
			return pointsService.BalanceText(message)
		case "points.top":
			return pointsService.TopText()
		case "points.shop":
			return pointsService.ShopText()
		case "widget.enabled":
			if widgetsService.Get()[strings.TrimSpace(value)] {
				return "включён"
			}
			return "выключен"
		case "twitch.followage", "twitch.accountage", "twitch.uptime", "twitch.viewers", "twitch.title", "twitch.game", "twitch.followers", "twitch.subscribers", "twitch.chatters":
			value := actioner.Twitch(context.Background(), name, message.Author.ID)
			value = strings.TrimPrefix(value, "Фолловер с ")
			value = strings.TrimPrefix(value, "Аккаунт создан ")

			return value
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
		pointsService.SetChannel(user.ID)
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

		if action, args, ok := triggersService.Match(message.Text); ok {
			if action == "say" {
				if text := strings.TrimSpace(strings.Join(args, " ")); text != "" {
					_ = chatRuntime.Say(text)
				}

				return
			}

			if text, err, handled := actioner.Perform(ctx, action, strings.Join(args, " "), message); handled {
				if err != nil {
					log.Printf("[Triggers] %s failed: %v", action, err)
				} else if text != "" {
					if strings.HasPrefix(text, "@") {
						_ = chatRuntime.Say(text)
					} else {
						_ = chatRuntime.Say("@" + message.Author.Login + ", " + text)
					}
				}

				return
			}
		}

		go commandsService.HandleMessage(message)
		analyticsService.HandleMessage()
		go moderationService.HandleMessage(ctx, message)

		if action, _, ok := triggersService.Match(message.Text); ok && (action == "clips.clip" || action == "clip") {
			go func() {
				clip, err := chatRuntime.CreateClipInfo(ctx)

				if err != nil || clip.URL == "" {
					log.Printf("[Clips] create failed: %v", err)
					_ = chatRuntime.Say("@" + message.Author.Login + ", не удалось создать клип")

					return
				}

				_ = chatRuntime.Say("@" + message.Author.Login + ", Клип: " + clip.URL)
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
	points   *points.Service
	games    *games.Service
	chat     *chatruntime.Runtime
	music    *music.Service
	quotes   *quotes.Service
	sync     *syncstate.Service
	commands *commands.Service
	widgets  *widgets.Service
	alerts   *alerts.Service
	roulette *roulette.Service
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

func parseRange(raw string) (int, int, bool) {
	value := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(raw), "..", "-"), ":", "-")
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}

	minimum, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	maximum, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || maximum < minimum {
		return 0, 0, false
	}

	return minimum, maximum, true
}

func (a commandActioner) resolveTarget(ctx context.Context, message twitch.TwitchChatMessage, value string) (string, string, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return message.Author.ID, "", nil
	}
	login := strings.TrimPrefix(fields[0], "@")
	if strings.EqualFold(login, "me") || strings.EqualFold(login, "self") {
		return message.Author.ID, strings.Join(fields[1:], " "), nil
	}
	id, err := a.chat.UserIDByLogin(ctx, login)
	if err != nil {
		return "", "", err
	}

	return id, strings.Join(fields[1:], " "), nil
}

func (a commandActioner) Perform(ctx context.Context, action, value string, message twitch.TwitchChatMessage) (string, error, bool) {
	switch action {
	case "say":
		return value, nil, true
	case "points.add", "points.remove", "points.set":
		amount, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return "", fmt.Errorf("нужно число"), true
		}

		delta := amount
		if action == "points.remove" {
			delta = -amount
		}
		if action == "points.set" {
			wallet, _ := a.points.Wallet(message.Author.ID)
			delta = amount - wallet.Balance
		}
		a.points.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, delta, "command")

		return fmt.Sprintf("баланс: %d", amount), nil, true
	case "ban":
		targetID, reason, err := a.resolveTarget(ctx, message, value)
		if err != nil {
			return "", err, true
		}
		if reason == "" {
			reason = "command"
		}
		return "", a.chat.BanUser(ctx, targetID, reason), true
	case "unban":
		targetID, _, err := a.resolveTarget(ctx, message, value)
		if err != nil {
			return "", err, true
		}
		return "пользователь разбанен", a.chat.UnbanUser(ctx, targetID), true
	case "delete":
		messageID := strings.TrimSpace(value)
		if messageID == "" {
			messageID = message.MessageID
		}
		return "", a.chat.DeleteMessage(ctx, messageID), true
	case "announce":
		return "", a.chat.Announcement(ctx, strings.TrimSpace(value)), true
	case "shoutout":
		return "шаут отправлен", a.chat.Shoutout(ctx, strings.TrimSpace(value)), true
	case "raid":
		return "рейд запущен", a.chat.StartRaid(ctx, strings.TrimSpace(value)), true
	case "raid.cancel":
		return "рейд отменён", a.chat.CancelRaid(ctx), true
	case "marker":
		return "маркер создан", a.chat.CreateMarker(ctx, strings.TrimSpace(value)), true
	case "shield.on":
		return "shield mode включён", a.chat.UpdateShieldMode(ctx, true), true
	case "shield.off":
		return "shield mode выключен", a.chat.UpdateShieldMode(ctx, false), true
	case "title.set":
		return "заголовок обновлён", a.chat.UpdateChannel(ctx, strings.TrimSpace(value), ""), true
	case "game.set":
		gameID, _, err := a.chat.SearchGame(ctx, strings.TrimSpace(value))
		if err != nil {
			return "", err, true
		}
		return "категория обновлена", a.chat.UpdateChannel(ctx, "", gameID), true
	case "commercial":
		seconds, _ := strconv.Atoi(strings.TrimSpace(value))
		if seconds <= 0 {
			seconds = 60
		}
		return "реклама запущена", a.chat.StartCommercial(ctx, seconds), true
	case "poll.end":
		parts := strings.Fields(value)
		if len(parts) == 0 {
			return "", fmt.Errorf("нужен id опроса"), true
		}
		status := "TERMINATED"
		if len(parts) > 1 {
			status = strings.ToUpper(parts[1])
		}
		return "опрос завершён", a.chat.EndPoll(ctx, parts[0], status), true
	case "poll":
		parts := strings.Split(value, "|")
		if len(parts) < 3 {
			return "", fmt.Errorf("формат: вопрос | вариант 1 | вариант 2 | секунды"), true
		}
		duration := 60
		if parsed, err := strconv.Atoi(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			duration = parsed
			parts = parts[:len(parts)-1]
		}
		title := strings.TrimSpace(parts[0])
		choices := make([]string, 0, len(parts)-1)
		for _, item := range parts[1:] {
			if choice := strings.TrimSpace(item); choice != "" {
				choices = append(choices, choice)
			}
		}
		if title == "" || len(choices) < 2 {
			return "", fmt.Errorf("нужны вопрос и минимум два варианта"), true
		}
		_, err := a.chat.CreatePoll(ctx, title, choices, duration)
		return "голосование запущено", err, true
	case "dice":
		count, sides := 1, 6
		formula := "1d6"
		if raw := strings.ToLower(strings.TrimSpace(value)); raw != "" {
			formula = raw
			if _, err := fmt.Sscanf(raw, "%dd%d", &count, &sides); err != nil {
				if _, err := fmt.Sscanf(raw, "d%d", &sides); err != nil {
					count, sides = 1, 6
					formula = "1d6"
				}
			}
			if count < 1 || count > 20 || sides < 1 || sides > 1000 {
				count, sides = 1, 6
				formula = "1d6"
			}
		}
		rolls := make([]int, 0, count)
		total := 0
		for i := 0; i < count; i++ {
			roll := rand.Intn(sides) + 1
			rolls = append(rolls, roll)
			total += roll
		}
		a.sync.RollDice(formula, total, rolls, message.Author.DisplayName)
		a.sync.PublishGame("dice", formula, strconv.Itoa(total), message.Author.DisplayName)
		return "@" + message.Author.Login + " бросил кубик " + formula + " и ему выпало " + strconv.Itoa(total), nil, true
	case "coinflip":
		heads, tails := "орёл", "решка"
		if a.widgets != nil {
			config := a.widgets.GetConfig("coinflip")
			if value, ok := config["coinHeads"].(string); ok && strings.TrimSpace(value) != "" {
				heads = strings.TrimSpace(value)
			}
			if value, ok := config["coinTails"].(string); ok && strings.TrimSpace(value) != "" {
				tails = strings.TrimSpace(value)
			}
		}
		result, side := heads, "heads"
		choice := strings.ToLower(strings.TrimSpace(value))
		forceTails := strings.Contains(choice, "tail") || strings.Contains(choice, "реш") || strings.Contains(choice, "reverse") || strings.Contains(choice, "ревер")
		forceHeads := strings.Contains(choice, "head") || strings.Contains(choice, "орёл") || strings.Contains(choice, "орел")
		if forceTails || (!forceHeads && rand.Intn(2) == 0) {
			result, side = tails, "tails"
		}
		a.sync.PublishGameSide("coin", "монетка", result, side, message.Author.DisplayName)
		return "@" + message.Author.Login + " бросил монетку и ему выпало " + result, nil, true
	case "quote.add":
		quote, err := a.quotes.Add(strings.TrimSpace(value), message.Author.DisplayName, message.Author.Login)
		if err != nil {
			return "", err, true
		}
		return fmt.Sprintf("цитата #%d добавлена", quote.Number), nil, true
	case "points.redeem":
		return a.points.Redeem(message, strings.TrimSpace(value)), nil, true
	case "games.slots":
		stake, _ := strconv.Atoi(strings.TrimSpace(value))
		return a.games.Slots(message, stake), nil, true
	case "games.wheel":
		stake, _ := strconv.Atoi(strings.TrimSpace(value))
		return a.games.Wheel(message, stake), nil, true
	case "games.trivia":
		return a.games.Trivia(message), nil, true
	case "games.duel":
		return a.games.Duel(message, strings.Fields(value)), nil, true
	case "games.accept":
		return a.games.AcceptDuel(message), nil, true
	case "lastmusic":
		track := a.music.Current()
		if track == nil {
			return "сейчас ничего не играет", nil, true
		}
		return "сейчас играет: " + track.URL, nil, true
	case "timeout":
		seconds, _ := strconv.Atoi(strings.TrimSpace(value))
		if seconds <= 0 {
			seconds = 60
		}
		return "", a.chat.TimeoutUser(ctx, message.Author.ID, seconds, "command"), true
	case "music.current":
		track := a.music.Current()
		if track == nil {
			return "сейчас ничего не играет", nil, true
		}
		return "сейчас играет: " + track.URL, nil, true
	case "widget.enable", "widget.disable", "widget.toggle":
		kind := strings.TrimSpace(value)
		if a.widgets != nil && kind != "" {
			enabled := action == "widget.enable"
			if action == "widget.toggle" {
				enabled = !a.widgets.Get()[kind]
			}
			a.widgets.Set(kind, enabled)
		}

		return "виджет обновлён", nil, true
	case "widget.config":
		parts := strings.SplitN(value, "|", 2)
		if a.widgets != nil && len(parts) == 2 {
			var config map[string]any
			if err := json.Unmarshal([]byte(parts[1]), &config); err == nil {
				a.widgets.SetConfig(strings.TrimSpace(parts[0]), config)
			}
		}

		return "конфиг виджета обновлён", nil, true
	case "alert.test":
		if a.alerts != nil {
			a.alerts.Test(strings.TrimSpace(value))
		}

		return "алерт отправлен", nil, true
	case "roulette.spin":
		if a.roulette != nil {
			if round, err := a.roulette.Spin(strings.TrimSpace(value)); err == nil && round.Game.Title != "" {
				return "рулетка: " + round.Game.Title, nil, true
			}
		}

		return "рулетка запущена", nil, true
	case "reward.create":
		parts := strings.Split(value, "|")
		if len(parts) < 3 {
			return "", fmt.Errorf("формат: title|prompt|cost|enabled"), true
		}
		cost, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		enabled := true
		if len(parts) > 3 {
			enabled = strings.EqualFold(strings.TrimSpace(parts[3]), "true") || strings.TrimSpace(parts[3]) == "1"
		}
		_, err := a.chat.CreateChannelReward(ctx, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), cost, enabled)

		return "награда создана", err, true
	case "reward.update":
		parts := strings.Split(value, "|")
		if len(parts) < 4 {
			return "", fmt.Errorf("формат: id|title|prompt|cost|enabled"), true
		}
		cost, _ := strconv.Atoi(strings.TrimSpace(parts[3]))
		enabled := true
		if len(parts) > 4 {
			enabled = strings.EqualFold(strings.TrimSpace(parts[4]), "true") || strings.TrimSpace(parts[4]) == "1"
		}
		_, err := a.chat.UpdateChannelReward(ctx, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), cost, enabled)

		return "награда обновлена", err, true
	case "reward.delete":
		return "награда удалена", a.chat.DeleteChannelReward(ctx, strings.TrimSpace(value)), true
	case "reward.fulfill":
		parts := strings.Split(value, "|")
		if len(parts) < 2 {
			return "", fmt.Errorf("формат: reward_id|redemption_id"), true
		}
		return "награда выполнена", a.chat.FulfillRedemption(ctx, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])), true
	case "mod.add", "mod.remove", "vip.add", "vip.remove":
		targetID, _, err := a.resolveTarget(ctx, message, value)
		if err != nil {
			return "", err, true
		}
		switch action {
		case "mod.add":
			err = a.chat.AddModerator(ctx, targetID)
		case "mod.remove":
			err = a.chat.RemoveModerator(ctx, targetID)
		case "vip.add":
			err = a.chat.AddVIP(ctx, targetID)
		case "vip.remove":
			err = a.chat.RemoveVIP(ctx, targetID)
		}

		return "роль обновлена", err, true
	case "chat.settings":
		settings := map[string]any{}
		for _, item := range strings.Split(value, ",") {
			key, val, ok := strings.Cut(item, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(val) {
			case "true", "1":
				settings[strings.TrimSpace(key)] = true
			case "false", "0":
				settings[strings.TrimSpace(key)] = false
			default:
				if number, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					settings[strings.TrimSpace(key)] = number
				} else {
					settings[strings.TrimSpace(key)] = strings.TrimSpace(val)
				}
			}
		}
		return "настройки чата обновлены", a.chat.UpdateChatSettings(ctx, settings), true
	case "timer.start":
		seconds, _ := strconv.Atoi(strings.TrimSpace(value))
		if seconds > 0 {
			a.sync.SetTimer("play", seconds)
		} else {
			a.sync.SetTimer("play", 0)
		}
		return "таймер запущен", nil, true
	case "timer.pause":
		a.sync.SetTimer("pause", 0)
		return "таймер на паузе", nil, true
	case "timer.reset":
		a.sync.SetTimer("pause", 0)
		return "таймер сброшен", nil, true
	case "command.run":
		if a.commands != nil {
			a.commands.RunByName(value, message)
		}
		return "", nil, true
	case "variable.set", "variable.inc", "variable.dec":
		if a.commands != nil {
			a.commands.ApplyVariable(action, value)
		}
		return "переменная обновлена", nil, true
	case "timer.add":
		seconds, _ := strconv.Atoi(strings.TrimSpace(value))
		if seconds != 0 {
			a.sync.AddTimer(seconds)
		}
		return "таймер изменён", nil, true
	case "twitch.followage", "twitch.accountage", "twitch.uptime", "twitch.viewers", "twitch.title", "twitch.game", "twitch.followers", "twitch.subscribers", "twitch.chatters":
		return a.Twitch(ctx, action, message.Author.ID), nil, true
	case "points.balance", "points.top", "points.shop", "quotes.random":
		return a.Twitch(ctx, action, message.Author.ID), nil, true
	case "random":
		if minimum, maximum, ok := parseRange(value); ok {
			chosen := strconv.Itoa(minimum + rand.Intn(maximum-minimum+1))
			a.sync.PublishGame("random", value, chosen, message.Author.DisplayName)

			return "@" + message.Author.Login + " случайное число: " + chosen, nil, true
		}
		options := strings.Split(value, "|")
		clean := options[:0]
		for _, item := range options {
			if item = strings.TrimSpace(item); item != "" {
				clean = append(clean, item)
			}
		}
		if len(clean) == 0 {
			return "", fmt.Errorf("нужно хотя бы одно значение или диапазон 1-100"), true
		}
		chosen := clean[rand.Intn(len(clean))]
		a.sync.PublishGame("random", value, chosen, message.Author.DisplayName)
		return "@" + message.Author.Login + " случайный выбор: " + chosen, nil, true
	}

	return "", nil, false
}

func (a commandActioner) Clip(ctx context.Context) error {
	clip, err := a.chat.CreateClipInfo(ctx)
	if err != nil {
		return err
	}

	return a.chat.Say("Клип: " + clip.URL)
}
