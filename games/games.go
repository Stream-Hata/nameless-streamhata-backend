// Package games — чат-игры и баллы: слоты, колесо, викторина.
package games

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type Question struct {
	Text   string `json:"text"`
	Answer string `json:"answer"`
}

// Settings — настройки игр.
type WheelAppearance struct {
	Size             int    `json:"size"`
	Background       string `json:"background"`
	BorderColor      string `json:"borderColor"`
	BorderWidth      int    `json:"borderWidth"`
	GlowColor        string `json:"glowColor"`
	GlowSize         int    `json:"glowSize"`
	PointerColor     string `json:"pointerColor"`
	CenterColor      string `json:"centerColor"`
	CenterText       string `json:"centerText"`
	CenterTextColor  string `json:"centerTextColor"`
	LabelColor       string `json:"labelColor"`
	LabelSize        int    `json:"labelSize"`
	LabelStrokeColor string `json:"labelStrokeColor"`
	LabelStrokeWidth int    `json:"labelStrokeWidth"`
	ShowLabels       bool   `json:"showLabels"`
	ShowChances      bool   `json:"showChances"`
	FontFamily       string `json:"fontFamily"`
	FontURL          string `json:"fontUrl"`
}

func defaultWheelAppearance() WheelAppearance {
	return WheelAppearance{
		Size:             380,
		Background:       "#0F0817",
		BorderColor:      "rgba(255,255,255,0.18)",
		BorderWidth:      6,
		GlowColor:        "#FF2EA6",
		GlowSize:         24,
		PointerColor:     "#FF2EA6",
		CenterColor:      "#0F0817",
		CenterText:       "NS",
		CenterTextColor:  "#FF2EA6",
		LabelColor:       "#FFFFFF",
		LabelSize:        16,
		LabelStrokeColor: "#0F0817",
		LabelStrokeWidth: 2,
		ShowLabels:       true,
		ShowChances:      true,
	}
}

type Settings struct {
	Enabled bool `json:"enabled"`

	MinBet int `json:"minBet"`
	MaxBet int `json:"maxBet"`

	SlotsEnabled  bool `json:"slotsEnabled"`
	SlotsCooldown int  `json:"slotsCooldownSeconds"`

	WheelEnabled      bool            `json:"wheelEnabled"`
	WheelCooldown     int             `json:"wheelCooldownSeconds"`
	WheelMode         string          `json:"wheelMode"`
	WheelRoundSeconds int             `json:"wheelRoundSeconds"`
	WheelLabelMode    string          `json:"wheelLabelMode"`
	WheelSpinDelay    int             `json:"wheelSpinDelaySeconds"`
	WheelSpinCooldown int             `json:"wheelSpinCooldownSeconds"`
	WheelSegments     []WheelSegment  `json:"wheelSegments"`
	WheelSoundURL     string          `json:"wheelSoundUrl"`
	WheelCenterURL    string          `json:"wheelCenterUrl"`
	WheelBoosts       []WheelBoost    `json:"wheelBoosts"`
	WheelAppearance   WheelAppearance `json:"wheelAppearance"`

	TriviaEnabled   bool       `json:"triviaEnabled"`
	TriviaPoints    int        `json:"triviaPoints"`
	TriviaCooldown  int        `json:"triviaCooldownSeconds"`
	TriviaQuestions []Question `json:"triviaQuestions"`

	DuelEnabled  bool `json:"duelEnabled"`
	DuelCooldown int  `json:"duelCooldownSeconds"`
}

func defaultSettings() Settings {
	return Settings{
		Enabled:      true,
		MinBet:       10,
		MaxBet:       1000,
		SlotsEnabled: true,

		SlotsCooldown:     30,
		WheelEnabled:      true,
		WheelMode:         "spin",
		WheelRoundSeconds: 60,
		WheelLabelMode:    "lot",
		WheelSpinDelay:    3,
		WheelSpinCooldown: 10,
		WheelSegments:     defaultWheelSegments(),
		WheelBoosts:       defaultWheelBoosts(),
		WheelAppearance:   defaultWheelAppearance(),

		WheelCooldown: 30,
		TriviaEnabled: true,
		TriviaPoints:  100,

		TriviaCooldown: 120,
		DuelEnabled:    true,
		DuelCooldown:   60,
		TriviaQuestions: []Question{
			{Text: "Что означает аббревиатура FPS в контексте игр?", Answer: "кадры в секунду"},
			{Text: "Сколько битсов нужно, чтобы накинуть 1 доллар (примерно)?", Answer: "150"},
			{Text: "Как называется режим, где нельзя умирать ни разу?", Answer: "hardcore"},
		},
	}
}

func normalizeWheelAppearance(value WheelAppearance) WheelAppearance {
	defaults := defaultWheelAppearance()

	if value.Size <= 0 {
		value.Size = defaults.Size
	}

	value.Size = clamp(value.Size, 220, 720)

	if value.BorderColor == "" {
		value.BorderColor = defaults.BorderColor
	}

	value.BorderWidth = clamp(value.BorderWidth, 0, 24)

	if value.GlowColor == "" {
		value.GlowColor = defaults.GlowColor
	}

	value.GlowSize = clamp(value.GlowSize, 0, 120)

	if value.PointerColor == "" {
		value.PointerColor = defaults.PointerColor
	}

	if value.CenterColor == "" {
		value.CenterColor = defaults.CenterColor
	}

	if value.CenterText == "" {
		value.CenterText = defaults.CenterText
	}

	if value.CenterTextColor == "" {
		value.CenterTextColor = defaults.CenterTextColor
	}

	if value.LabelColor == "" {
		value.LabelColor = defaults.LabelColor
	}

	if value.LabelSize <= 0 || value.LabelSize < 12 {
		value.LabelSize = defaults.LabelSize
	}

	value.LabelSize = clamp(value.LabelSize, 12, 32)

	if value.LabelStrokeColor == "" {
		value.LabelStrokeColor = defaults.LabelStrokeColor
	}

	value.LabelStrokeWidth = clamp(value.LabelStrokeWidth, 0, 12)

	if value.Size > 0 && !value.ShowLabels && value.LabelSize == defaults.LabelSize && value.CenterText == defaults.CenterText {
		value.ShowLabels = false
	}

	if value.Background == "" {
		value.Background = defaults.Background
	}

	return value
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}

	if value > maxValue {
		return maxValue
	}

	return value
}

type LastResult struct {
	Kind           string             `json:"kind"`
	Mode           string             `json:"mode"`
	Title          string             `json:"title"`
	User           string             `json:"user"`
	Bet            int                `json:"bet"`
	Win            int                `json:"win"`
	Label          string             `json:"label"`
	Multiplier     float64            `json:"multiplier"`
	LuckMultiplier float64            `json:"luckMultiplier,omitempty"`
	At             time.Time          `json:"at"`
	Participants   []WheelParticipant `json:"participants,omitempty"`
	Order          []WheelParticipant `json:"order,omitempty"`
}

type activeTrivia struct {
	answer   string
	award    int
	deadline time.Time
}

type Duel struct {
	ChallengerID   string `json:"challengerId"`
	ChallengerName string `json:"challengerName"`
	OpponentLogin  string `json:"opponentLogin"`
	Bet            int    `json:"bet"`
	deadline       time.Time
}

// Service — обработка игровых команд чата.
type Service struct {
	mu         sync.Mutex
	settings   Settings
	cooldowns  map[string]time.Time
	trivia     *activeTrivia
	duelState  *Duel
	lastResult *LastResult
	wheelRound *WheelRound
	wheelTimer *time.Timer

	points   *points.Service
	writer   chat.Writer
	now      func() time.Time
	randN    func(int) int
	triggers *triggers.Service
}

func New(pointsService *points.Service, writer chat.Writer) *Service {
	return &Service{
		settings:  defaultSettings(),
		cooldowns: make(map[string]time.Time),
		points:    pointsService,
		writer:    writer,
		now:       time.Now,
		randN:     rand.IntN,
		triggers:  triggers.New(nil),
	}
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "games"
}

func (s *Service) Name() string {
	return "Чат-игры"
}

func (s *Service) Kind() string {
	return "games"
}

func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings.Enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings.Enabled = enabled

	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	active := make([]string, 0, 3)

	if s.settings.SlotsEnabled {
		active = append(active, "слоты")
	}

	if s.settings.WheelEnabled {
		active = append(active, "колесо")
	}

	if s.settings.TriviaEnabled {
		active = append(active, "викторина")
	}

	return "игры: " + strings.Join(active, ", ")
}

func (s *Service) GetLastResult() *LastResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastResult
}

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings
}

func (s *Service) SetSettings(settings Settings) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	if settings.MinBet <= 0 {
		settings.MinBet = 10
	}

	if settings.MaxBet < settings.MinBet {
		settings.MaxBet = settings.MinBet * 10
	}

	if settings.SlotsCooldown <= 0 {
		settings.SlotsCooldown = 30
	}

	if settings.WheelCooldown <= 0 {
		settings.WheelCooldown = 30
	}

	if settings.WheelMode != "elimination" {
		settings.WheelMode = "spin"
	}

	if settings.WheelRoundSeconds <= 0 {
		settings.WheelRoundSeconds = 60
	}

	if settings.WheelLabelMode != "viewer" {
		settings.WheelLabelMode = "lot"
	}

	if settings.WheelSpinDelay < 0 {
		settings.WheelSpinDelay = 0
	}

	if settings.WheelSpinDelay > 30 {
		settings.WheelSpinDelay = 30
	}

	if settings.WheelSpinCooldown < 0 {
		settings.WheelSpinCooldown = 0
	}

	if settings.WheelSpinCooldown > 60 {
		settings.WheelSpinCooldown = 60
	}

	if len(settings.WheelSegments) < 2 {
		settings.WheelSegments = defaultWheelSegments()
	}

	if len(settings.WheelBoosts) == 0 {
		settings.WheelBoosts = defaultWheelBoosts()
	}

	settings.WheelAppearance = normalizeWheelAppearance(settings.WheelAppearance)

	if settings.TriviaPoints <= 0 {
		settings.TriviaPoints = 100
	}

	if settings.TriviaCooldown <= 0 {
		settings.TriviaCooldown = 120
	}

	if settings.TriviaQuestions == nil {
		settings.TriviaQuestions = s.settings.TriviaQuestions
	}

	if settings.DuelCooldown <= 0 {
		settings.DuelCooldown = 60
	}

	s.settings = settings

	return s.settings
}

/* ── Обработка чата ───────────────────────────────────────────────── */

// SetTriggers подключает настраиваемые стримером команды.
func (s *Service) SetTriggers(service *triggers.Service) {
	s.triggers = service
}

func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	settings := s.GetSettings()

	if !settings.Enabled {
		return
	}

	text := strings.TrimSpace(message.Text)

	if !strings.HasPrefix(text, "!") {
		s.tryTriviaAnswer(message, text)

		return
	}

	action, args, ok := s.triggers.Match(text)

	if !ok {
		return
	}

	switch action {
	case "games.slots":
		if settings.SlotsEnabled {
			s.reply(s.Slots(message, bet(args, settings)))
		}
	case "games.wheel":
		if settings.WheelEnabled {
			s.HandleWheelCommand(message, args, settings)
		}
	case "games.trivia":
		if settings.TriviaEnabled {
			s.reply(s.Trivia(message))
		}
	case "games.duel":
		if settings.DuelEnabled {
			s.reply(s.Duel(message, args))
		}
	case "games.accept":
		if settings.DuelEnabled {
			s.reply(s.AcceptDuel(message))
		}
	}
}

// Duel бросает вызов зрителю на ставку.
func (s *Service) Duel(message twitch.TwitchChatMessage, args []string) string {
	if len(args) < 2 {
		return "Использование: !duel @ник <ставка>"
	}

	opponent := strings.TrimPrefix(args[0], "@")
	settings := s.GetSettings()
	stake := bet(args[1:], settings)

	if strings.EqualFold(opponent, message.Author.Login) {
		return "Нельзя вызвать самого себя."
	}

	if reply := s.cooldownReply(message.Author.ID, "duel", settings.DuelCooldown); reply != "" {
		return reply
	}

	wallet, ok := s.points.Wallet(message.Author.ID)

	if !ok || wallet.Balance < stake {
		return fmt.Sprintf("%s, не хватает баллов на ставку %d.", display(message), stake)
	}

	s.mu.Lock()
	s.duelState = &Duel{
		ChallengerID:   message.Author.ID,
		ChallengerName: display(message),
		OpponentLogin:  strings.ToLower(opponent),
		Bet:            stake,
		deadline:       s.now().Add(2 * time.Minute),
	}
	s.mu.Unlock()

	return fmt.Sprintf(
		"%s вызывает %s на дуэль! Ставка %d. %s, напиши !accept в течение 2 минут.",
		display(message), opponent, stake, opponent,
	)
}

// AcceptDuel принимает вызов и решает исход.
func (s *Service) AcceptDuel(message twitch.TwitchChatMessage) string {
	s.mu.Lock()

	duel := s.duelState

	if duel == nil || s.now().After(duel.deadline) {
		s.duelState = nil
		s.mu.Unlock()

		return "Активной дуэли нет."
	}

	if !strings.EqualFold(message.Author.Login, duel.OpponentLogin) {
		s.mu.Unlock()

		return "Принять дуэль может только вызванный зритель."
	}

	s.duelState = nil
	s.mu.Unlock()

	challengerWallet, ok1 := s.points.Wallet(duel.ChallengerID)
	opponentWallet, ok2 := s.points.Wallet(message.Author.ID)

	if !ok1 || !ok2 || challengerWallet.Balance < duel.Bet || opponentWallet.Balance < duel.Bet {
		return "У кого-то из участников не хватает баллов — дуэль отменена."
	}

	s.points.Add(duel.ChallengerID, challengerWallet.Login, challengerWallet.DisplayName, -duel.Bet, "дуэль: ставка")
	s.points.Add(message.Author.ID, message.Author.Login, display(message), -duel.Bet, "дуэль: ставка")

	winnerName := duel.ChallengerName
	winnerID := duel.ChallengerID
	winnerLogin := challengerWallet.Login
	loserName := display(message)

	if s.randN(2) == 0 {
		winnerID = message.Author.ID
		winnerLogin = message.Author.Login
		winnerName = display(message)
		loserName = duel.ChallengerName
	}

	s.points.Add(winnerID, winnerLogin, winnerName, duel.Bet*2, "дуэль: победа")

	return fmt.Sprintf("Дуэль: %s победил %s и забирает %d баллов!", winnerName, loserName, duel.Bet*2)
}

func bet(args []string, settings Settings) int {
	if len(args) < 1 {
		return settings.MinBet
	}

	value := 0

	_, _ = fmt.Sscanf(args[0], "%d", &value)

	if value < settings.MinBet {
		return settings.MinBet
	}

	if value > settings.MaxBet {
		return settings.MaxBet
	}

	return value
}

/* ── Слоты ────────────────────────────────────────────────────────── */

var slotSymbols = []string{"🍒", "🍋", "🍊", "💎", "7️⃣"}

// Slots крутит барабаны, списывает ставку и начисляет выигрыш.
func (s *Service) Slots(message twitch.TwitchChatMessage, stake int) string {
	if reply := s.spend(message, stake, "слоты", s.settings.SlotsCooldown); reply != "" {
		return reply
	}

	weighted := func() int {
		// Чем "дороже" символ, тем реже выпадает: 0-4 с весами 40/30/20/7/3.
		roll := s.randN(100)

		switch {
		case roll < 40:
			return 0
		case roll < 70:
			return 1
		case roll < 90:
			return 2
		case roll < 97:
			return 3
		default:
			return 4
		}
	}

	reels := []int{weighted(), weighted(), weighted()}
	multiplier := slotPayout(reels)

	win := stake * multiplier

	if win > 0 {
		s.points.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, win, "слоты: выигрыш")
	}

	visual := fmt.Sprintf("%s %s %s", slotSymbols[reels[0]], slotSymbols[reels[1]], slotSymbols[reels[2]])

	if win == 0 {
		return fmt.Sprintf("%s: %s — не повезло, -%d баллов.", display(message), visual, stake)
	}

	return fmt.Sprintf("%s: %s — выигрыш %d баллов! (x%d)", display(message), visual, win, multiplier)
}

// slotPayout — чистая математика выплат (тестируется отдельно).
func slotPayout(reels []int) int {
	if len(reels) != 3 {
		return 0
	}

	if reels[0] == reels[1] && reels[1] == reels[2] {
		if reels[0] == 4 {
			return 25
		}

		if reels[0] == 3 {
			return 15
		}

		return 10
	}

	pairs := 0

	if reels[0] == reels[1] || reels[1] == reels[2] || reels[0] == reels[2] {
		pairs = 1
	}

	if pairs == 1 {
		return 2
	}

	return 0
}

/* ── Колесо ───────────────────────────────────────────────────────── */

var wheelOutcomes = []struct {
	Label      string
	Multiplier float64
	Weight     int
}{
	{"ноль", 0, 35},
	{"полторы ставки", 1.5, 30},
	{"двойная", 2, 20},
	{"тройная", 3, 10},
	{"пятерка", 5, 5},
}

func (s *Service) Wheel(message twitch.TwitchChatMessage, stake int) string {
	if reply := s.spend(message, stake, "колесо", s.settings.WheelCooldown); reply != "" {
		return reply
	}

	settings := s.GetSettings()
	segments := settings.WheelSegments

	if len(segments) < 2 {
		segments = defaultWheelSegments()
	}

	totalWeight := 0
	for _, segment := range segments {
		if segment.Weight > 0 {
			totalWeight += segment.Weight
		}
	}

	if totalWeight <= 0 {
		totalWeight = len(segments)
	}

	roll := s.randN(totalWeight)
	accumulated := 0
	selected := segments[0]

	for _, segment := range segments {
		weight := segment.Weight
		if weight <= 0 {
			weight = 1
		}

		accumulated += weight

		if roll < accumulated {
			selected = segment

			break
		}
	}

	win := int(math.Round(float64(stake) * selected.Multiplier))

	if win > 0 {
		s.points.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, win, "колесо: выигрыш")
	}

	s.mu.Lock()
	s.lastResult = &LastResult{
		Kind:       "wheel",
		Mode:       "spin",
		Title:      "Колесо",
		User:       message.Author.DisplayName,
		Bet:        stake,
		Win:        win,
		Label:      selected.Label,
		Multiplier: selected.Multiplier,
		At:         s.now(),
	}
	s.mu.Unlock()

	if win == 0 {
		return fmt.Sprintf("%s: колесо остановилось на «%s» — -%d баллов.", display(message), selected.Label, stake)
	}

	return fmt.Sprintf("%s: колесо — «%s», выигрыш %d баллов!", display(message), selected.Label, win)
}

/* ── Викторина ────────────────────────────────────────────────────── */

// Trivia задаёт вопрос; первый правильный ответ забирает награду.
func (s *Service) Trivia(message twitch.TwitchChatMessage) string {
	settings := s.GetSettings()

	if reply := s.cooldownReply(message.Author.ID, "trivia", settings.TriviaCooldown); reply != "" {
		return reply
	}

	if len(settings.TriviaQuestions) == 0 {
		return "Вопросы для викторины не настроены."
	}

	question := settings.TriviaQuestions[s.randN(len(settings.TriviaQuestions))]

	s.mu.Lock()

	s.trivia = &activeTrivia{
		answer:   normalize(question.Answer),
		award:    settings.TriviaPoints,
		deadline: s.now().Add(time.Minute),
	}

	s.mu.Unlock()

	return fmt.Sprintf("Викторина: %s (ответ в чат, %d баллов, 60 секунд)", question.Text, settings.TriviaPoints)
}

func (s *Service) tryTriviaAnswer(message twitch.TwitchChatMessage, text string) {
	s.mu.Lock()

	trivia := s.trivia

	if trivia == nil || s.now().After(trivia.deadline) {
		s.trivia = nil
		s.mu.Unlock()

		return
	}

	if normalize(text) != trivia.answer {
		s.mu.Unlock()

		return
	}

	award := trivia.award
	s.trivia = nil
	s.mu.Unlock()

	s.points.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, award, "викторина")

	s.reply(fmt.Sprintf("%s угадал! +%d баллов.", display(message), award))
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(value, "?!.")))
}

/* ── Общее ────────────────────────────────────────────────────────── */

// spend списывает ставку и проверяет кулдаун; возвращает текст отказа.
func (s *Service) spend(message twitch.TwitchChatMessage, stake int, game string, cooldown int) string {
	if reply := s.cooldownReply(message.Author.ID, game, cooldown); reply != "" {
		return reply
	}

	wallet, ok := s.points.Wallet(message.Author.ID)

	if !ok || wallet.Balance < stake {
		return fmt.Sprintf("%s, не хватает баллов на ставку %d. Пиши в чат, чтобы заработать!", display(message), stake)
	}

	s.points.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, -stake, game+": ставка")

	return ""
}

func (s *Service) cooldownReply(userID, game string, cooldown int) string {
	key := game + ":" + userID
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if until, ok := s.cooldowns[key]; ok && now.Before(until) {
		return fmt.Sprintf("Кулдаун: ещё %d сек.", int(until.Sub(now).Seconds())+1)
	}

	s.cooldowns[key] = now.Add(time.Duration(cooldown) * time.Second)

	return ""
}

func (s *Service) reply(text string) {
	if s.writer == nil || text == "" {
		return
	}

	_ = s.writer.Say(text)
}

func display(message twitch.TwitchChatMessage) string {
	if message.Author.DisplayName != "" {
		return message.Author.DisplayName
	}

	return message.Author.Login
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/games", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch || request.Method == http.MethodPost {
			var settings Settings

			if err := json.NewDecoder(request.Body).Decode(&settings); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			writeJSON(response, http.StatusOK, service.SetSettings(settings))

			return
		}

		writeJSON(response, http.StatusOK, service.GetSettings())
	})

	srv.ServeEndpoint("/rgg-sync/games/last", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, service.GetLastResult())
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, service.GetWheelState())

			return
		}

		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/start", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Title   string `json:"title"`
			Bet     int    `json:"bet"`
			Seconds int    `json:"seconds"`
			Mode    string `json:"mode"`
			Test    bool   `json:"test"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		settings := service.GetSettings()
		if body.Seconds > 0 {
			settings.WheelRoundSeconds = body.Seconds
		}

		if body.Mode == "elimination" || body.Mode == "spin" {
			settings.WheelMode = body.Mode
		}

		title := strings.TrimSpace(body.Title)
		if title == "" {
			title = "Колесо"
		}

		round := service.startWheelRound(title, clampBet(body.Bet, settings), settings)
		if round != nil {
			round.Test = body.Test
		}
		writeJSON(response, http.StatusOK, round)
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/entry", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Name   string `json:"name"`
			Viewer string `json:"viewer"`
			Bet    int    `json:"bet"`
			Luck   bool   `json:"luck"`
			Key    string `json:"key"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		if err := service.AddWheelLot(body.Name, body.Viewer, body.Bet, body.Luck, body.Key); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, service.GetWheelState())
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/boost", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			UserID string `json:"userId"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		multiplier, bet, chance, err := service.BoostParticipant(body.UserID)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"multiplier": multiplier,
			"bet":        bet,
			"chance":     chance,
		})
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/entry/{userId}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.RemoveWheelParticipant(request.PathValue("userId")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "participant not found"})

			return
		}

		writeJSON(response, http.StatusOK, service.GetWheelState())
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/refund", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if _, err := service.RefundWheel(); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, service.GetWheelState())
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/reset", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		service.ResetWheel()
		writeJSON(response, http.StatusOK, service.GetWheelState())
	})

	srv.ServeEndpoint("/rgg-sync/games/wheel/spin", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		round := service.GetWheelRound()
		if round == nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "нет активного колеса"})

			return
		}

		service.SpinWheel(round.ID)
		writeJSON(response, http.StatusOK, service.GetWheelState())
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
