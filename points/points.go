// Package points — баллы лояльности: начисления, кошельки, транзакции,
// магазин наград и встроенные чат-команды (!points, !top, !shop, !redeem).
package points

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/alerts"
	"github.com/p7x-dev/nameless-streamhata-backend/bots"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/triggers"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/viewer"
)

// Wallet — кошелёк зрителя.
type Wallet struct {
	UserID      string    `json:"userId"`
	Login       string    `json:"login"`
	DisplayName string    `json:"displayName"`
	Balance     int       `json:"balance"`
	TotalEarned int       `json:"totalEarned"`
	TotalSpent  int       `json:"totalSpent"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Transaction — запись в истории начислений/списаний.
type Transaction struct {
	ID           string    `json:"id"`
	UserID       string    `json:"userId"`
	Login        string    `json:"login"`
	Delta        int       `json:"delta"`
	Reason       string    `json:"reason"`
	BalanceAfter int       `json:"balanceAfter"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Reward — награда магазина.
type Reward struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Cost     int    `json:"cost"`
	Response string `json:"response"`
	Enabled  bool   `json:"enabled"`
}

// Settings — правила начисления.
type Settings struct {
	Enabled                bool `json:"enabled"`
	PointsPerMessage       int  `json:"pointsPerMessage"`
	MessageCooldownSeconds int  `json:"messageCooldownSeconds"`
	PointsPerMinute        int  `json:"pointsPerMinute"`
	StreakBonusPerLevel    int  `json:"streakBonusPerLevel"`
	SubscribeBonus         int  `json:"subscribeBonus"`
	GiftBonus              int  `json:"giftBonus"`
	CheerBonusPerBit       int  `json:"cheerBonusPerBit"`
}

func defaultSettings() Settings {
	return Settings{
		Enabled:                true,
		PointsPerMessage:       1,
		MessageCooldownSeconds: 60,
		PointsPerMinute:        1,
		StreakBonusPerLevel:    5,
		SubscribeBonus:         500,
		GiftBonus:              250,
		CheerBonusPerBit:       1,
	}
}

// Service — кошельки, транзакции, магазин и обработка чат-команд.
type Service struct {
	mu         sync.Mutex
	wallets    map[string]*Wallet
	transacion []Transaction
	history    map[string][]Transaction
	rewards    map[string]*Reward
	settings   Settings

	cooldowns map[string]time.Time

	writer   chat.Writer
	db       *sql.DB
	now      func() time.Time
	triggers *triggers.Service
}

func New(writer chat.Writer, db *sql.DB) *Service {
	service := &Service{
		wallets:   make(map[string]*Wallet),
		history:   make(map[string][]Transaction),
		rewards:   make(map[string]*Reward),
		settings:  defaultSettings(),
		cooldowns: make(map[string]time.Time),
		writer:    writer,
		db:        db,
		now:       time.Now,
		triggers:  triggers.New(nil),
	}

	service.load()
	service.cleanupTestData()

	return service
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "points"
}

func (s *Service) Name() string {
	return "Баллы лояльности"
}

func (s *Service) Kind() string {
	return "points"
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

	return fmt.Sprintf("кошельков: %d, наград: %d", len(s.wallets), len(s.rewards))
}

/* ── Настройки и магазин ──────────────────────────────────────────── */

func (s *Service) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.settings
}

func (s *Service) SetSettings(settings Settings) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()

	if settings.PointsPerMessage < 0 {
		settings.PointsPerMessage = 0
	}

	if settings.MessageCooldownSeconds <= 0 {
		settings.MessageCooldownSeconds = 60
	}

	if settings.StreakBonusPerLevel < 0 {
		settings.StreakBonusPerLevel = 0
	}

	s.settings = settings

	return s.settings
}

func (s *Service) Rewards() []Reward {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]Reward, 0, len(s.rewards))

	for _, reward := range s.rewards {
		list = append(list, *reward)
	}

	sort.Slice(list, func(i, j int) bool { return list[i].Cost < list[j].Cost })

	return list
}

func (s *Service) UpsertReward(reward Reward) (Reward, error) {
	reward.Name = strings.TrimSpace(reward.Name)

	if reward.Name == "" {
		return Reward{}, fmt.Errorf("у награды должно быть название")
	}

	if reward.Cost <= 0 {
		return Reward{}, fmt.Errorf("стоимость должна быть больше нуля")
	}

	if reward.Response == "" {
		reward.Response = "Награда «" + reward.Name + "» активирована, $(user)!"
	}

	if reward.ID == "" {
		reward.ID = fmt.Sprintf("reward-%d", s.now().UnixNano())
		reward.Enabled = true
	}

	s.mu.Lock()
	s.rewards[reward.ID] = &reward
	s.mu.Unlock()

	s.persistReward(reward)

	return reward, nil
}

func (s *Service) DeleteReward(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.rewards[id]; !ok {
		return false
	}

	delete(s.rewards, id)

	if s.db != nil {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM reward WHERE id = $1`, id)
	}

	return true
}

/* ── Кошельки ─────────────────────────────────────────────────────── */

// Add изменяет баланс и пишет транзакцию.
func (s *Service) Add(userID, login, displayName string, delta int, reason string) *Transaction {
	if userID == "" || delta == 0 {
		return nil
	}

	s.mu.Lock()

	wallet, exists := s.wallets[userID]

	if !exists {
		wallet = &Wallet{
			UserID:      userID,
			Login:       login,
			DisplayName: displayName,
			CreatedAt:   s.now().UTC(),
		}

		s.wallets[userID] = wallet
	}

	if login != "" {
		wallet.Login = login
	}

	if displayName != "" {
		wallet.DisplayName = displayName
	}

	wallet.Balance += delta
	wallet.UpdatedAt = s.now().UTC()

	if delta > 0 {
		wallet.TotalEarned += delta
	} else {
		wallet.TotalSpent -= delta
	}

	transaction := Transaction{
		ID:           fmt.Sprintf("tx-%d", s.now().UnixNano()),
		UserID:       userID,
		Login:        wallet.Login,
		Delta:        delta,
		Reason:       reason,
		BalanceAfter: wallet.Balance,
		CreatedAt:    s.now().UTC(),
	}

	s.transacion = append(s.transacion, transaction)

	if len(s.transacion) > 1000 {
		s.transacion = s.transacion[len(s.transacion)-1000:]
	}

	history := s.history[userID]
	history = append(history, transaction)

	if len(history) > 100 {
		history = history[len(history)-100:]
	}

	s.history[userID] = history

	walletCopy := *wallet
	s.mu.Unlock()

	s.persistWallet(walletCopy)
	s.persistTransaction(transaction)

	return &transaction
}

func (s *Service) Wallet(userID string) (Wallet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wallet, ok := s.wallets[userID]

	if !ok {
		return Wallet{}, false
	}

	return *wallet, true
}

var testNamePattern = regexp.MustCompile(`(^|[^a-zа-я0-9])(test|тест|demo|sample|fake|проверка|пример)([^a-zа-я0-9]|$)`)

func isTestAccount(userID, login, displayName string) bool {
	if strings.HasPrefix(strings.ToLower(userID), "manual-") {
		return true
	}

	return testNamePattern.MatchString(strings.ToLower(login + " " + displayName))
}

func (s *Service) Top(limit int) []Wallet {
	s.mu.Lock()

	list := make([]Wallet, 0, len(s.wallets))

	for _, wallet := range s.wallets {
		if isTestAccount(wallet.UserID, wallet.Login, wallet.DisplayName) || bots.Is(wallet.Login) {
			continue
		}

		list = append(list, *wallet)
	}

	s.mu.Unlock()

	sort.Slice(list, func(i, j int) bool { return list[i].Balance > list[j].Balance })

	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}

	return list[:limit]
}

func (s *Service) History(userID string, limit int) []Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.history[userID]

	if limit <= 0 || limit > len(history) {
		limit = len(history)
	}

	result := make([]Transaction, 0, limit)

	for i := len(history) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, history[i])
	}

	return result
}

func (s *Service) RecentTransactions(limit int) []Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 || limit > len(s.transacion) {
		limit = len(s.transacion)
	}

	result := make([]Transaction, 0, limit)

	for i := len(s.transacion) - 1; i >= 0 && len(result) < limit; i-- {
		if bots.Is(s.transacion[i].Login) {
			continue
		}

		result = append(result, s.transacion[i])
	}

	return result
}

/* ── Начисления ───────────────────────────────────────────────────── */

// SetTriggers подключает настраиваемые стримером команды.
func (s *Service) SetTriggers(service *triggers.Service) {
	s.triggers = service
}

// HandleMessage начисляет баллы за сообщение и обрабатывает встроенные команды.
func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	if bots.Is(message.Author.Login) {
		return
	}

	settings := s.GetSettings()
	text := strings.TrimSpace(message.Text)

	if strings.HasPrefix(text, "!") {
		action, args, ok := s.triggers.Match(text)

		if !ok {
			return
		}

		s.handleAction(message, action, args)

		return
	}

	if !settings.Enabled || settings.PointsPerMessage <= 0 {
		return
	}

	key := message.Author.ID
	now := s.now()

	s.mu.Lock()
	until, cooling := s.cooldowns[key]

	if cooling && now.Before(until) {
		s.mu.Unlock()

		return
	}

	s.cooldowns[key] = now.Add(time.Duration(settings.MessageCooldownSeconds) * time.Second)
	s.mu.Unlock()

	s.Add(
		message.Author.ID,
		message.Author.Login,
		message.Author.DisplayName,
		settings.PointsPerMessage,
		"сообщение",
	)
}

// HandleViewers начисляет баллы присутствующим в чатерсах (раз в тик рантайма).
func (s *Service) HandleViewers(viewers []viewer.Viewer) {
	settings := s.GetSettings()

	if !settings.Enabled || settings.PointsPerMinute <= 0 {
		return
	}

	for _, item := range viewers {
		s.Add(item.UserID, item.Login, item.DisplayName, settings.PointsPerMinute, "просмотр")
	}
}

// HandleStreak выдаёт бонус за watch streak.
func (s *Service) HandleStreak(event viewer.WatchStreakEvent) {
	settings := s.GetSettings()

	if !settings.Enabled || settings.StreakBonusPerLevel <= 0 {
		return
	}

	bonus := event.StreakCount * settings.StreakBonusPerLevel

	s.Add(event.UserID, event.Login, event.DisplayName, bonus, fmt.Sprintf("стрик %d", event.StreakCount))
}

// HandleAlert начисляет бонусы за подписки, подарки и битсы.
func (s *Service) HandleAlert(event alerts.Event) {
	settings := s.GetSettings()

	if !settings.Enabled {
		return
	}

	bonus := 0
	reason := ""

	switch event.Type {
	case "subscribe", "resub":
		bonus = settings.SubscribeBonus
		reason = "подписка"
	case "gift":
		bonus = settings.GiftBonus * max(event.Amount, 1)
		reason = "подарочные подписки"
	case "cheer":
		bonus = settings.CheerBonusPerBit * event.Amount
		reason = "битсы"
	default:
		return
	}

	if bonus <= 0 {
		return
	}

	// Идентификатор зрителя у алертов не приходит — используем логин как ключ.
	userID := "login:" + strings.ToLower(event.Username)
	s.Add(userID, strings.ToLower(event.Username), event.Username, bonus, reason)
}

/* ── Встроенные команды ───────────────────────────────────────────── */

func (s *Service) handleAction(message twitch.TwitchChatMessage, action string, args []string) {
	switch action {
	case "points.balance":
		s.reply(s.balanceText(message))
	case "points.top":
		s.reply(s.topText())
	case "points.shop":
		s.reply(s.shopText())
	case "points.redeem":
		if len(args) == 0 {
			s.reply("Использование: !redeem <название награды>")

			return
		}

		s.reply(s.Redeem(message, strings.Join(args, " ")))
	}
}

func (s *Service) BalanceText(message twitch.TwitchChatMessage) string {
	return s.balanceText(message)
}

func (s *Service) TopText() string {
	return s.topText()
}

func (s *Service) ShopText() string {
	return s.shopText()
}

func (s *Service) balanceText(message twitch.TwitchChatMessage) string {
	wallet, ok := s.Wallet(message.Author.ID)

	if !ok {
		return fmt.Sprintf("%s, у тебя пока 0 баллов — пиши в чат и смотри стрим!", display(message))
	}

	return fmt.Sprintf("%s, у тебя %d баллов (заработано %d).", display(message), wallet.Balance, wallet.TotalEarned)
}

func (s *Service) topText() string {
	top := s.Top(3)

	if len(top) == 0 {
		return "Топ пока пуст."
	}

	parts := make([]string, 0, len(top))

	for index, wallet := range top {
		parts = append(parts, fmt.Sprintf("%d) %s — %d", index+1, wallet.DisplayName, wallet.Balance))
	}

	return "Топ: " + strings.Join(parts, ", ")
}

func (s *Service) shopText() string {
	rewards := s.Rewards()

	if len(rewards) == 0 {
		return "Магазин пока пуст."
	}

	parts := make([]string, 0, len(rewards))

	for _, reward := range rewards {
		if reward.Enabled {
			parts = append(parts, fmt.Sprintf("%s (%d)", reward.Name, reward.Cost))
		}
	}

	return "Магазин: " + strings.Join(parts, ", ") + ". Купить: !redeem <название>"
}

// Redeem списывает баллы и возвращает текст для чата.
func (s *Service) Redeem(message twitch.TwitchChatMessage, query string) string {
	query = strings.ToLower(strings.TrimSpace(query))

	var matched *Reward

	s.mu.Lock()

	for _, reward := range s.rewards {
		if !reward.Enabled {
			continue
		}

		if strings.ToLower(reward.Name) == query || reward.ID == query {
			matched = reward

			break
		}
	}

	s.mu.Unlock()

	if matched == nil {
		return "Награда не найдена. Посмотри !shop"
	}

	wallet, ok := s.Wallet(message.Author.ID)

	if !ok || wallet.Balance < matched.Cost {
		return fmt.Sprintf("%s, не хватает баллов: нужно %d.", display(message), matched.Cost)
	}

	s.Add(message.Author.ID, message.Author.Login, message.Author.DisplayName, -matched.Cost, "награда: "+matched.Name)

	return renderReward(matched.Response, message)
}

func renderReward(template string, message twitch.TwitchChatMessage) string {
	result := strings.ReplaceAll(template, "$(user)", display(message))

	channel := message.Broadcaster.DisplayName
	if channel == "" {
		channel = message.Broadcaster.Login
	}

	return strings.ReplaceAll(result, "$(channel)", channel)
}

func display(message twitch.TwitchChatMessage) string {
	if message.Author.DisplayName != "" {
		return message.Author.DisplayName
	}

	return message.Author.Login
}

func (s *Service) reply(text string) {
	if s.writer == nil || text == "" {
		return
	}

	if err := s.writer.Say(text); err != nil {
		log.Printf("[Points] send failed: %v", err)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) persistWallet(wallet Wallet) {
	if s.db == nil {
		return
	}

	query := `
		INSERT INTO points_wallet (user_id, login, display_name, balance, total_earned, total_spent, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id) DO UPDATE SET
			login = EXCLUDED.login,
			display_name = EXCLUDED.display_name,
			balance = EXCLUDED.balance,
			total_earned = EXCLUDED.total_earned,
			total_spent = EXCLUDED.total_spent,
			updated_at = EXCLUDED.updated_at
	`

	_, err := s.db.ExecContext(context.Background(), query,
		wallet.UserID, wallet.Login, wallet.DisplayName, wallet.Balance,
		wallet.TotalEarned, wallet.TotalSpent, wallet.CreatedAt, wallet.UpdatedAt,
	)
	if err != nil {
		log.Printf("[Points] wallet persist failed: %v", err)
	}
}

func (s *Service) persistTransaction(transaction Transaction) {
	if s.db == nil {
		return
	}

	query := `
		INSERT INTO points_transaction (id, user_id, login, delta, reason, balance_after, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := s.db.ExecContext(context.Background(), query,
		transaction.ID, transaction.UserID, transaction.Login, transaction.Delta,
		transaction.Reason, transaction.BalanceAfter, transaction.CreatedAt,
	)
	if err != nil {
		log.Printf("[Points] transaction persist failed: %v", err)
	}
}

func (s *Service) persistReward(reward Reward) {
	if s.db == nil {
		return
	}

	query := `
		INSERT INTO reward (id, name, cost, response, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			cost = EXCLUDED.cost,
			response = EXCLUDED.response,
			enabled = EXCLUDED.enabled
	`

	_, err := s.db.ExecContext(context.Background(), query,
		reward.ID, reward.Name, reward.Cost, reward.Response, reward.Enabled,
	)
	if err != nil {
		log.Printf("[Points] reward persist failed: %v", err)
	}
}

func (s *Service) cleanupTestData() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for userID, wallet := range s.wallets {
		if !isTestAccount(userID, wallet.Login, wallet.DisplayName) {
			continue
		}

		delete(s.wallets, userID)
		delete(s.history, userID)

		if s.db != nil {
			_, _ = s.db.ExecContext(context.Background(), `DELETE FROM points_transaction WHERE user_id = $1`, userID)
			_, _ = s.db.ExecContext(context.Background(), `DELETE FROM points_wallet WHERE user_id = $1`, userID)
		}
	}

	filtered := s.transacion[:0]

	for _, transaction := range s.transacion {
		if !isTestAccount(transaction.UserID, transaction.Login, transaction.Login) {
			filtered = append(filtered, transaction)
		}
	}

	s.transacion = filtered
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	s.loadWallets()
	s.loadRewards()
}

func (s *Service) loadWallets() {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT user_id, login, display_name, balance, total_earned, total_spent, created_at, updated_at FROM points_wallet`)
	if err != nil {
		log.Printf("[Points] wallets load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var wallet Wallet

		if err := rows.Scan(
			&wallet.UserID, &wallet.Login, &wallet.DisplayName, &wallet.Balance,
			&wallet.TotalEarned, &wallet.TotalSpent, &wallet.CreatedAt, &wallet.UpdatedAt,
		); err != nil {
			continue
		}

		s.wallets[wallet.UserID] = &wallet
	}
}

func (s *Service) loadRewards() {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, name, cost, response, enabled FROM reward`)
	if err != nil {
		log.Printf("[Points] rewards load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var reward Reward

		if err := rows.Scan(&reward.ID, &reward.Name, &reward.Cost, &reward.Response, &reward.Enabled); err != nil {
			continue
		}

		s.rewards[reward.ID] = &reward
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/points", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch || request.Method == http.MethodPost {
			var settings Settings

			if err := json.NewDecoder(request.Body).Decode(&settings); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			writeJSON(response, http.StatusOK, service.SetSettings(settings))

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"top":          service.Top(50),
			"settings":     service.GetSettings(),
			"rewards":      service.Rewards(),
			"transactions": service.RecentTransactions(30),
		})
	})

	srv.ServeEndpoint("/rgg-sync/points/rewards", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var reward Reward

		if err := json.NewDecoder(request.Body).Decode(&reward); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		saved, err := service.UpsertReward(reward)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, saved)
	})

	srv.ServeEndpoint("/rgg-sync/points/rewards/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.DeleteReward(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "reward not found"})

			return
		}

		response.WriteHeader(http.StatusNoContent)
	})

	srv.ServeEndpoint("/rgg-sync/points/user/{userId}", func(response http.ResponseWriter, request *http.Request) {
		userID := request.PathValue("userId")

		wallet, ok := service.Wallet(userID)

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "wallet not found"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"wallet":  wallet,
			"history": service.History(userID, 50),
		})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
