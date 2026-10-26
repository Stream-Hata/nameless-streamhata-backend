package games

import (
	"fmt"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// WheelSegment — сектор колеса.
type WheelSegment struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Multiplier float64 `json:"multiplier"`
	Weight     int     `json:"weight"`
	Color      string  `json:"color"`
}

// WheelParticipant — участник колеса на выбывание.
type WheelParticipant struct {
	UserID         string  `json:"userId"`
	TwitchUserID   string  `json:"twitchUserId,omitempty"`
	Login          string  `json:"login"`
	DisplayName    string  `json:"displayName"`
	Bet            int     `json:"bet"`
	BaseBet        int     `json:"baseBet"`
	Boost          float64 `json:"boost,omitempty"`
	Chance         float64 `json:"chance"`
	Color          string  `json:"color"`
	Lot            string  `json:"lot"`
	Key            string  `json:"key,omitempty"`
	Luck           bool    `json:"luck"`
	BoostUsed      bool    `json:"boostUsed"`
	LuckMultiplier float64 `json:"luckMultiplier,omitempty"`
}

// WheelRound — активный раунд колеса.
type WheelRound struct {
	ID             string             `json:"id"`
	Mode           string             `json:"mode"`
	Title          string             `json:"title"`
	Bet            int                `json:"bet"`
	Participants   []WheelParticipant `json:"participants"`
	Pot            int                `json:"pot"`
	State          string             `json:"state"`
	StartedAt      time.Time          `json:"startedAt"`
	Deadline       time.Time          `json:"deadline"`
	SpinAt         *time.Time         `json:"spinAt,omitempty"`
	SpinEndsAt     *time.Time         `json:"spinEndsAt,omitempty"`
	NextSpinAt     *time.Time         `json:"nextSpinAt,omitempty"`
	FinishedAt     *time.Time         `json:"finishedAt,omitempty"`
	Order          []WheelParticipant `json:"order,omitempty"`
	Winner         *WheelParticipant  `json:"winner,omitempty"`
	LastEliminated *WheelParticipant  `json:"lastEliminated,omitempty"`
	EliminatedAt   *time.Time         `json:"eliminatedAt,omitempty"`
	Refunded       bool               `json:"refunded"`
	Test           bool               `json:"test,omitempty"`
}

type WheelBoost struct {
	Value  float64 `json:"value"`
	Weight int     `json:"weight"`
}

func defaultWheelBoosts() []WheelBoost {
	return []WheelBoost{
		{Value: 0, Weight: 20},
		{Value: 1, Weight: 20},
		{Value: 2, Weight: 25},
		{Value: 3, Weight: 15},
		{Value: 5, Weight: 10},
		{Value: 10, Weight: 8},
		{Value: 100, Weight: 2},
	}
}

func defaultWheelSegments() []WheelSegment {
	return []WheelSegment{
		{ID: "zero", Label: "ноль", Multiplier: 0, Weight: 35, Color: "#FF2EA6"},
		{ID: "x1_5", Label: "полторы ставки", Multiplier: 1.5, Weight: 30, Color: "#22E0FF"},
		{ID: "x2", Label: "двойная", Multiplier: 2, Weight: 20, Color: "#FFE14D"},
		{ID: "x3", Label: "тройная", Multiplier: 3, Weight: 10, Color: "#7C5CFF"},
		{ID: "x5", Label: "пятерка", Multiplier: 5, Weight: 5, Color: "#2EE6A8"},
	}
}

// GetWheelState отдаёт конфиг, активный раунд и последний результат.
func (s *Service) GetWheelState() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return map[string]any{
		"settings": s.settings,
		"round":    s.wheelRound,
		"last":     s.lastResult,
	}
}

// HandleWheelCommand обрабатывает !wheel в чате.
func (s *Service) HandleWheelCommand(
	message twitch.TwitchChatMessage,
	args []string,
	settings Settings,
) {
	hasTitle := len(args) > 0 && !isNumericArg(args[0])

	if settings.WheelMode != "elimination" && !hasTitle {
		s.reply(s.Wheel(message, bet(args, settings)))

		return
	}

	title, stake := parseWheelArgs(args, settings)

	s.mu.Lock()
	round := s.wheelRound
	s.mu.Unlock()

	if round == nil || round.State == "finished" {
		s.startWheelRound(title, stake, settings)
		round = s.GetWheelRound()
	}

	if round == nil || round.State != "collecting" {
		s.reply("Колесо уже крутится — дождитесь результата.")

		return
	}

	if title != "Колесо" && round.Title != "" && !strings.EqualFold(title, round.Title) {
		s.reply(fmt.Sprintf("Сейчас колесо на «%s». Вход: !wheel %s <ставка>.", round.Title, round.Title))

		return
	}

	s.joinWheelRound(message, stake, round.ID, settings)
}

func (s *Service) ResetWheel() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wheelTimer != nil {
		s.wheelTimer.Stop()
		s.wheelTimer = nil
	}

	s.wheelRound = nil
	s.lastResult = nil
}

func (s *Service) GetWheelRound() *WheelRound {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.wheelRound
}

func (s *Service) startWheelRound(title string, bet int, settings Settings) *WheelRound {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wheelTimer != nil {
		s.wheelTimer.Stop()
	}

	seconds := settings.WheelRoundSeconds
	if seconds <= 0 {
		seconds = 60
	}

	now := s.now()
	round := &WheelRound{
		ID:           fmt.Sprintf("wheel-%d", now.UnixNano()),
		Mode:         settings.WheelMode,
		Title:        title,
		Bet:          bet,
		Participants: []WheelParticipant{},
		State:        "collecting",
		StartedAt:    now,
		Deadline:     now.Add(time.Duration(seconds) * time.Second),
	}

	s.wheelRound = round
	s.wheelTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		s.SpinWheel(round.ID)
	})

	s.reply(fmt.Sprintf(
		"Колесо на выбывание «%s»: вход %d баллов, %d сек. Команда: !wheel %s <ставка>.",
		title,
		bet,
		seconds,
		title,
	))

	return round
}

func (s *Service) joinWheelRound(
	message twitch.TwitchChatMessage,
	stake int,
	roundID string,
	settings Settings,
) {
	s.joinWheelRoundAs(
		WheelParticipant{
			UserID:       message.Author.ID,
			TwitchUserID: message.Author.ID,
			Login:        message.Author.Login,
			DisplayName:  display(message),
			Bet:          stake,
			Luck:         hasLuckText(message.Text),
			Lot:          s.currentLot(roundID),
		},
		roundID,
		settings,
		true,
	)
}

// EnterWheel добавляет участника из награды/панели без списания баллов.
func (s *Service) EnterWheel(userID, login, displayName, title string, luck bool) error {
	return s.EnterWheelWithCost(userID, login, displayName, title, luck, 0)
}

// EnterWheelWithCost добавляет участника с шансом пропорционально стоимости награды.
func (s *Service) EnterWheelWithCost(userID, login, displayName, title string, luck bool, cost int) error {
	settings := s.GetSettings()
	args := strings.Fields(title)
	roundTitle, stake := parseWheelArgs(args, settings)

	if cost > 0 {
		stake = clampBet(cost, settings)
	}

	if stake <= 0 {
		stake = settings.MinBet
	}

	if roundTitle == "Колесо" && strings.TrimSpace(title) != "" {
		roundTitle = strings.TrimSpace(title)
	}

	return s.enterWheelWithBet(userID, login, displayName, roundTitle, "", stake, luck)
}

// BoostParticipant крутит рулетку множителей для лота удачи.
func (s *Service) BoostParticipant(userID string) (float64, int, float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	round := s.wheelRound
	if round == nil || round.State != "collecting" {
		return 0, 0, 0, fmt.Errorf("wheel is not collecting")
	}

	index := -1

	for i, participant := range round.Participants {
		if participant.UserID == userID {
			index = i

			break
		}
	}

	if index < 0 {
		return 0, 0, 0, fmt.Errorf("participant not found")
	}

	if !round.Participants[index].Luck {
		return 0, 0, 0, fmt.Errorf("participant has no luck")
	}

	if round.Participants[index].BoostUsed {
		return 0, 0, 0, fmt.Errorf("boost already used")
	}

	boosts := s.settings.WheelBoosts
	if len(boosts) == 0 {
		boosts = defaultWheelBoosts()
	}

	totalWeight := 0
	for _, boost := range boosts {
		if boost.Weight > 0 {
			totalWeight += boost.Weight
		}
	}

	if totalWeight <= 0 {
		totalWeight = len(boosts)
	}

	roll := s.randN(totalWeight)
	accumulated := 0
	multiplier := boosts[0].Value

	for _, boost := range boosts {
		weight := boost.Weight
		if weight <= 0 {
			weight = 1
		}

		accumulated += weight

		if roll < accumulated {
			multiplier = boost.Value

			break
		}
	}

	participant := &round.Participants[index]

	oldBet := participant.Bet
	participant.Bet = int(float64(participant.BaseBet) * multiplier)
	round.Pot += participant.Bet - oldBet

	participant.Boost = multiplier
	participant.BoostUsed = true
	recalculateChances(round)

	return multiplier, participant.Bet, participant.Chance, nil
}

// AddWheelLot добавляет лот вручную из панели.
func (s *Service) AddWheelLot(name, viewer string, bet int, luck bool, key string) error {
	name = strings.TrimSpace(name)
	viewer = strings.TrimSpace(viewer)

	if name == "" {
		return fmt.Errorf("lot name is required")
	}

	if viewer == "" {
		viewer = "Вручную"
	}

	settings := s.GetSettings()
	if bet < settings.MinBet {
		bet = settings.MinBet
	}

	return s.enterWheelWithBet(
		fmt.Sprintf("manual-%d", s.now().UnixNano()),
		strings.ToLower(strings.ReplaceAll(viewer, " ", "_")),
		viewer,
		name,
		key,
		bet,
		luck,
	)
}

// RemoveWheelParticipant убирает лот из текущего раунда.
func (s *Service) RemoveWheelParticipant(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	round := s.wheelRound
	if round == nil {
		return false
	}

	found := false
	participants := make([]WheelParticipant, 0, len(round.Participants))

	for _, participant := range round.Participants {
		if participant.UserID == userID {
			found = true
			round.Pot -= participant.Bet

			continue
		}

		participants = append(participants, participant)
	}

	if !found {
		return false
	}

	round.Participants = participants
	recalculateChances(round)

	if round.Winner != nil && round.Winner.UserID == userID {
		round.Winner = nil
		s.lastResult = nil
	}

	if len(round.Order) > 0 {
		order := make([]WheelParticipant, 0, len(round.Order))

		for _, participant := range round.Order {
			if participant.UserID != userID {
				order = append(order, participant)
			}
		}

		round.Order = order
	}

	return true
}

func (s *Service) enterWheelWithBet(
	userID, login, displayName, title, key string,
	bet int,
	luck bool,
) error {
	settings := s.GetSettings()

	s.mu.Lock()
	round := s.wheelRound
	s.mu.Unlock()

	if round == nil || round.State == "finished" {
		round = s.startWheelRound(title, bet, settings)
	}

	if round == nil || round.State != "collecting" {
		return fmt.Errorf("wheel is not collecting")
	}

	s.joinWheelRoundAs(
		WheelParticipant{
			UserID:       userID,
			TwitchUserID: userID,
			Login:        login,
			DisplayName:  displayName,
			Bet:          bet,
			Luck:         luck,
			Lot:          title,
			Key:          key,
		},
		round.ID,
		settings,
		false,
	)

	return nil
}

func (s *Service) joinWheelRoundAs(
	participant WheelParticipant,
	roundID string,
	settings Settings,
	charge bool,
) {
	if charge {
		message := twitch.TwitchChatMessage{
			Author: twitch.ChatUser{
				ID:          participant.UserID,
				Login:       participant.Login,
				DisplayName: participant.DisplayName,
			},
		}

		if reply := s.spend(message, participant.Bet, "wheel", settings.WheelCooldown); reply != "" {
			s.reply(reply)

			return
		}
	}

	s.mu.Lock()
	round := s.wheelRound

	if round == nil || round.ID != roundID || round.State != "collecting" {
		s.mu.Unlock()

		if charge {
			s.points.Add(participant.UserID, participant.Login, participant.DisplayName, participant.Bet, "wheel: возврат")
		}

		return
	}

	for index := range round.Participants {
		existing := &round.Participants[index]

		sameUser := (participant.TwitchUserID != "" && existing.TwitchUserID == participant.TwitchUserID) ||
			(participant.TwitchUserID == "" && existing.UserID == participant.UserID)
		sameLot := strings.EqualFold(existing.Lot, participant.Lot)
		sameKey := participant.Key != "" && existing.Key == participant.Key
		manualPair := strings.HasPrefix(participant.UserID, "manual-") &&
			strings.HasPrefix(existing.UserID, "manual-")

		if (sameUser && sameLot) || (sameKey && (sameUser || manualPair)) {
			existing.BaseBet += participant.Bet
			existing.Bet += participant.Bet
			existing.Luck = existing.Luck || participant.Luck

			if participant.Luck {
				existing.BoostUsed = false
			}

			round.Pot += participant.Bet
			recalculateChances(round)
			count := len(round.Participants)
			lot := existing.Lot
			pot := round.Pot
			s.mu.Unlock()

			s.reply(fmt.Sprintf("%s увеличил ставку в лоте «%s» (участников: %d, банк: %d).", participant.DisplayName, lot, count, pot))

			return
		}
	}

	if participant.BaseBet <= 0 {
		participant.BaseBet = participant.Bet
	}

	if participant.TwitchUserID != "" {
		participant.UserID = participant.TwitchUserID + "|" + slug(participant.Lot)
	}

	if participant.Luck {
		participant.Color = "#FFD166"
	} else if participant.Color == "" {
		palette := []string{
			"#FF2EA6", "#22E0FF", "#FFE14D", "#7C5CFF",
			"#2EE6A8", "#FF8A3D", "#FF5C8A", "#5CFFB1",
		}
		participant.Color = palette[s.randN(len(palette))]
	}

	round.Participants = append(round.Participants, participant)
	round.Pot += participant.Bet
	recalculateChances(round)
	count := len(round.Participants)
	s.mu.Unlock()

	s.reply(fmt.Sprintf("%s в колесе «%s» (%d участников, банк %d).", participant.DisplayName, round.Title, count, round.Pot))
}

// SpinWheel завершает набор и запускает прокрутку.
func (s *Service) SpinWheel(roundID string) {
	settings := s.GetSettings()
	delay := settings.WheelSpinDelay
	if delay < 0 {
		delay = 0
	}

	s.mu.Lock()

	round := s.wheelRound
	auto := round != nil && round.NextSpinAt != nil

	if auto {
		delay = 0
	}
	if round != nil && round.NextSpinAt != nil && s.now().Before(*round.NextSpinAt) {
		s.mu.Unlock()

		return
	}

	if round == nil || round.ID != roundID || round.State != "collecting" {
		s.mu.Unlock()

		return
	}

	if len(round.Participants) == 0 {
		round.State = "finished"
		s.mu.Unlock()
		s.reply("Колесо остановлено: никто не участвовал.")

		return
	}

	if round.Mode == "elimination" && len(round.Participants) > 1 {
		round.NextSpinAt = nil
		eliminated := pickWeighted(round.Participants, s.randN)
		now := s.now()
		spinAt := now.Add(time.Duration(delay) * time.Second)
		spinSeconds := 4
		spinEndsAt := spinAt.Add(time.Duration(spinSeconds) * time.Second)

		round.Order = []WheelParticipant{eliminated}
		round.State = "spinning"
		round.SpinAt = &spinAt
		round.SpinEndsAt = &spinEndsAt
		round.LastEliminated = nil
		round.EliminatedAt = nil
		s.mu.Unlock()

		time.AfterFunc(time.Duration(delay+spinSeconds)*time.Second, func() {
			s.finishWheelElimination(roundID, eliminated.UserID)
		})

		return
	}

	round.NextSpinAt = nil
	order := weightedOrder(round.Participants, s.randN)

	now := s.now()
	spinAt := now.Add(time.Duration(delay) * time.Second)
	round.Order = order
	round.State = "spinning"
	round.SpinAt = &spinAt

	spinSeconds := len(order) * 2
	if spinSeconds < 4 {
		spinSeconds = 4
	}

	spinEndsAt := spinAt.Add(time.Duration(spinSeconds) * time.Second)
	round.SpinEndsAt = &spinEndsAt

	s.mu.Unlock()

	time.AfterFunc(time.Duration(delay+spinSeconds)*time.Second, func() {
		s.finishWheelRound(roundID)
	})
}

func (s *Service) finishWheelElimination(roundID, userID string) {
	settings := s.GetSettings()

	s.mu.Lock()

	round := s.wheelRound
	if round == nil || round.ID != roundID || round.State != "spinning" {
		s.mu.Unlock()

		return
	}

	removed := WheelParticipant{}
	remaining := make([]WheelParticipant, 0, len(round.Participants))

	for _, participant := range round.Participants {
		if participant.UserID == userID {
			removed = participant

			continue
		}

		remaining = append(remaining, participant)
	}

	if removed.UserID == "" {
		s.mu.Unlock()

		return
	}

	now := s.now()
	round.Participants = remaining
	round.Pot -= removed.Bet
	round.LastEliminated = &removed
	round.EliminatedAt = &now
	round.Order = nil
	recalculateChances(round)

	if len(remaining) == 1 {
		winner := remaining[0]
		round.Winner = &winner
		round.State = "finished"
		round.FinishedAt = &now
		s.lastResult = &LastResult{
			Kind:         "wheel",
			Mode:         round.Mode,
			Title:        round.Title,
			User:         winner.DisplayName,
			Bet:          round.Bet,
			Win:          round.Pot,
			Label:        lotLabel(winner, round.Title),
			Multiplier:   1,
			At:           now,
			Participants: round.Participants,
		}
		title := round.Title
		name := winner.DisplayName
		s.mu.Unlock()

		if round.Pot > 0 && !round.Test {
			walletUserID := winner.TwitchUserID
			if walletUserID == "" {
				walletUserID = winner.UserID
			}

			s.points.Add(walletUserID, winner.Login, winner.DisplayName, round.Pot, "колесо: победа")
		}

		s.reply(fmt.Sprintf("🏆 Колесо «%s»: победил %s!", title, name))

		return
	}

	round.State = "collecting"
	round.SpinAt = nil
	round.SpinEndsAt = nil
	cooldown := settings.WheelSpinCooldown
	if cooldown < 5 {
		cooldown = 10
	}

	nextSpin := now.Add(time.Duration(cooldown) * time.Second)
	round.NextSpinAt = &nextSpin

	if round.Deadline.Before(now) {
		round.Deadline = now.Add(60 * time.Second)
	}

	title := round.Title
	name := removed.DisplayName
	count := len(remaining)
	s.mu.Unlock()

	s.reply(fmt.Sprintf("Колесо «%s»: %s выбыл. Осталось лотов: %d.", title, name, count))

	if cooldown >= 0 {
		time.AfterFunc(time.Duration(cooldown)*time.Second, func() {
			s.SpinWheel(roundID)
		})
	}
}

func pickWeighted(participants []WheelParticipant, randN func(int) int) WheelParticipant {
	total := 0

	for _, participant := range participants {
		total += participant.Bet
	}

	if total <= 0 {
		return participants[randN(len(participants))]
	}

	roll := randN(total)
	accumulated := 0

	for _, participant := range participants {
		accumulated += participant.Bet

		if roll < accumulated {
			return participant
		}
	}

	return participants[len(participants)-1]
}

func lotLabel(participant WheelParticipant, fallback string) string {
	if strings.TrimSpace(participant.Lot) != "" {
		return participant.Lot
	}

	return fallback
}

func (s *Service) finishWheelRound(roundID string) {
	s.mu.Lock()

	round := s.wheelRound
	if round == nil || round.ID != roundID || round.State != "spinning" || len(round.Order) == 0 {
		s.mu.Unlock()

		return
	}

	winner := round.Order[len(round.Order)-1]
	now := s.now()
	round.Winner = &winner

	luckMultiplier := float64(1)

	if winner.Luck {
		options := []float64{2, 3, 5}
		luckMultiplier = options[s.randN(len(options))]
		winner.LuckMultiplier = luckMultiplier
	}

	round.State = "finished"
	round.FinishedAt = &now
	totalWin := int(float64(round.Pot) * luckMultiplier)

	s.lastResult = &LastResult{
		Kind:           "wheel",
		Mode:           round.Mode,
		Title:          round.Title,
		User:           winner.DisplayName,
		Bet:            round.Bet,
		Win:            totalWin,
		Label:          lotLabel(winner, round.Title),
		Multiplier:     luckMultiplier,
		LuckMultiplier: luckMultiplier,
		At:             now,
		Participants:   round.Participants,
		Order:          round.Order,
	}

	s.mu.Unlock()

	if totalWin > 0 && !round.Test {
		walletUserID := winner.TwitchUserID
		if walletUserID == "" {
			walletUserID = winner.UserID
		}

		s.points.Add(walletUserID, winner.Login, winner.DisplayName, totalWin, "колесо: победа")
	}

	if luckMultiplier > 1 {
		s.reply(fmt.Sprintf("🍀 Удача сработала: множитель ×%.0f! %s забирает %d баллов.", luckMultiplier, winner.DisplayName, totalWin))

		return
	}

	s.reply(fmt.Sprintf("🏆 Колесо «%s»: победил %s и забрал %d баллов!", round.Title, winner.DisplayName, totalWin))
}

func (s *Service) RefundWheel() (*WheelRound, error) {
	s.mu.Lock()

	round := s.wheelRound
	if round == nil {
		s.mu.Unlock()

		return nil, fmt.Errorf("раунд не найден")
	}

	if round.State == "finished" {
		s.mu.Unlock()

		return nil, fmt.Errorf("раунд уже завершён")
	}

	if round.Refunded {
		s.mu.Unlock()

		return nil, fmt.Errorf("баллы уже возвращены")
	}

	participants := append([]WheelParticipant(nil), round.Participants...)
	round.Refunded = true
	round.State = "finished"
	round.FinishedAt = ptrTime(s.now())
	s.mu.Unlock()

	if round.Test {
		return round, nil
	}

	for _, participant := range participants {
		if participant.Bet <= 0 || participant.TwitchUserID == "" {
			continue
		}

		s.points.Add(participant.TwitchUserID, participant.Login, participant.DisplayName,
			participant.Bet, "колесо: возврат баллов")
	}

	return round, nil
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func recalculateChances(round *WheelRound) {
	total := 0

	for _, participant := range round.Participants {
		total += participant.Bet
	}

	for index := range round.Participants {
		if total > 0 {
			round.Participants[index].Chance = float64(round.Participants[index].Bet) / float64(total) * 100
		} else if len(round.Participants) > 0 {
			round.Participants[index].Chance = 100 / float64(len(round.Participants))
		}
	}
}

// weightedOrder выбирает победителя пропорционально ставке и ставит его последним.
func weightedOrder(participants []WheelParticipant, randN func(int) int) []WheelParticipant {
	if len(participants) == 0 {
		return nil
	}

	total := 0

	for _, participant := range participants {
		total += participant.Bet
	}

	winnerIndex := 0

	if total > 0 {
		roll := randN(total)
		accumulated := 0

		for index, participant := range participants {
			accumulated += participant.Bet

			if roll < accumulated {
				winnerIndex = index

				break
			}
		}
	} else {
		winnerIndex = randN(len(participants))
	}

	winner := participants[winnerIndex]
	others := make([]WheelParticipant, 0, len(participants)-1)

	for index, participant := range participants {
		if index != winnerIndex {
			others = append(others, participant)
		}
	}

	for i := len(others) - 1; i > 0; i-- {
		j := randN(i + 1)
		others[i], others[j] = others[j], others[i]
	}

	return append(others, winner)
}

func isNumericArg(value string) bool {
	parsed := 0
	_, err := fmt.Sscanf(value, "%d", &parsed)

	return err == nil
}

func parseWheelArgs(args []string, settings Settings) (string, int) {
	title := "Колесо"
	stake := settings.MinBet
	parts := make([]string, 0, len(args))

	for _, arg := range args {
		value := 0
		if _, err := fmt.Sscanf(arg, "%d", &value); err == nil {
			stake = clampBet(value, settings)

			continue
		}

		lower := strings.ToLower(arg)
		if strings.Contains(lower, "удач") || strings.Contains(lower, "luck") {
			continue
		}

		parts = append(parts, strings.TrimSpace(arg))
	}

	if len(parts) > 0 {
		title = strings.Join(parts, " ")
	}

	return title, stake
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")

	return value
}

func hasLuckText(text string) bool {
	lower := strings.ToLower(text)

	return strings.Contains(lower, "удач") || strings.Contains(lower, "luck")
}

func (s *Service) currentLot(roundID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wheelRound != nil && s.wheelRound.ID == roundID {
		return s.wheelRound.Title
	}

	return "Колесо"
}

func clampBet(value int, settings Settings) int {
	if value < settings.MinBet {
		return settings.MinBet
	}

	if value > settings.MaxBet {
		return settings.MaxBet
	}

	return value
}
