// Package commands — кастомные команды чата с переменными, кулдаунами,
// уровнями доступа и счётчиками.
package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/bots"
	"github.com/p7x-dev/nameless-streamhata-backend/chat"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Command — пользовательская команда чата.
type Command struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Aliases         []string  `json:"aliases"`
	Action          string    `json:"action"`
	Response        string    `json:"response"`
	Reply           string    `json:"reply"`
	LimitReply      string    `json:"limitReply"`
	CooldownSeconds int       `json:"cooldownSeconds"`
	UserLevel       string    `json:"userLevel"`
	Enabled         bool      `json:"enabled"`
	Counter         int       `json:"counter"`
	CreatedAt       time.Time `json:"createdAt"`
}

var levels = map[string]int{
	"everyone":    0,
	"subscriber":  1,
	"vip":         2,
	"moderator":   3,
	"broadcaster": 4,
}

// Service хранит команды, кулдауны и обрабатывает сообщения чата.
type Actioner interface {
	AwardPoints(userID, login, displayName string, amount int, reason string) error
	TimeoutUser(ctx context.Context, userID string, seconds int, reason string) error
	WheelEntry(userID, login, displayName, title string, bet int) error
	AddMusic(ctx context.Context, url, addedBy string) error
	Clip(ctx context.Context) error
	Twitch(ctx context.Context, action, userID string) string
}

// AdvancedActioner — расширенные действия, которые не ломают старые реализации.
type AdvancedActioner interface {
	Perform(ctx context.Context, action, value string, message twitch.TwitchChatMessage) (string, error, bool)
}

type VariableResolver func(name, value string, message twitch.TwitchChatMessage) string

type Variable struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Action string `json:"action"`
}

type Service struct {
	mu        sync.Mutex
	commands  map[string]*Command
	cooldowns map[string]time.Time

	writer    chat.Writer
	actioner  Actioner
	variables VariableResolver
	custom    map[string]Variable
	actions   map[string]string
	replies   map[string]string
	limits    map[string]string
	db        *sql.DB
	now       func() time.Time
}

func (s *Service) SetVariableResolver(resolver VariableResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.variables = resolver
}

func (s *Service) SetActioner(actioner Actioner) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.actioner = actioner
}

func New(writer chat.Writer, db *sql.DB) *Service {
	service := &Service{
		commands:  make(map[string]*Command),
		cooldowns: make(map[string]time.Time),
		custom:    make(map[string]Variable),
		actions:   make(map[string]string),
		replies:   make(map[string]string),
		limits:    make(map[string]string),
		writer:    writer,
		db:        db,
		now:       time.Now,
	}

	service.load()
	service.loadActions()
	service.loadReplies()
	service.loadLimits()
	service.loadVariables()

	return service
}

/* ── Контракт integrations.Component ──────────────────────────────── */

func (s *Service) ID() string {
	return "commands"
}

func (s *Service) Name() string {
	return "Команды чата"
}

func (s *Service) Kind() string {
	return "commands"
}

func (s *Service) Enabled() bool {
	return true
}

func (s *Service) SetEnabled(bool) error {
	return nil
}

func (s *Service) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return fmt.Sprintf("команд: %d", len(s.commands))
}

/* ── CRUD ─────────────────────────────────────────────────────────── */

func (s *Service) List() []Command {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]Command, 0, len(s.commands))

	for _, command := range s.commands {
		list = append(list, *command)
	}

	return list
}

func isAdvancedAction(action string) bool {
	switch action {
	case "points.add", "points.remove", "points.set", "ban", "unban", "delete", "announce", "shoutout",
		"raid", "raid.cancel", "marker", "shield.on", "shield.off", "title.set", "game.set", "commercial",
		"poll", "poll.end", "dice", "coinflip", "random", "random.number", "quote.add", "points.redeem", "games.slots",
		"games.wheel", "games.trivia", "games.duel", "games.accept", "music.current", "timer.start",
		"timer.pause", "timer.reset", "timer.add", "widget.enable", "widget.disable", "widget.toggle",
		"widget.config", "alert.test", "roulette.spin", "roulette.last", "reward.create", "reward.update", "reward.delete",
		"reward.fulfill", "mod.add", "mod.remove", "vip.add", "vip.remove", "chat.settings":
		return true
	}

	return false
}

func knownAction(action string) bool {
	if isAdvancedAction(action) {
		return true
	}

	switch action {
	case "say", "points", "timeout", "wheel", "music", "clip", "lastmusic", "quotes.random",
		"variable.set", "variable.inc", "variable.dec", "command.run":
		return true
	}

	return strings.HasPrefix(action, "twitch.") || strings.HasPrefix(action, "points.")
}

func (s *Service) Upsert(command Command) (Command, error) {
	command.Name = normalizeName(command.Name)

	if command.Action == "" {
		command.Action = "say"
	}

	if !knownAction(command.Action) {
		return Command{}, fmt.Errorf("неизвестное действие команды")
	}

	if command.Name == "" {
		return Command{}, fmt.Errorf("команда должна иметь имя")
	}

	if command.Response == "" {
		return Command{}, fmt.Errorf("команда должна иметь ответ")
	}

	if _, ok := levels[command.UserLevel]; !ok {
		command.UserLevel = "everyone"
	}

	if command.ID == "" {
		command.ID = fmt.Sprintf("cmd-%d", time.Now().UnixNano())
		command.CreatedAt = s.now().UTC()
	}

	s.mu.Lock()
	s.commands[command.Name] = &command
	s.actions[command.ID] = command.Action
	s.replies[command.ID] = command.Reply
	s.limits[command.ID] = command.LimitReply
	s.mu.Unlock()

	s.persist(command)
	s.persistActions()
	s.persistReplies()
	s.persistLimits()

	return command, nil
}

func (s *Service) SetCommandEnabled(id string, enabled bool) (Command, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, command := range s.commands {
		if command.ID != id {
			continue
		}

		command.Enabled = enabled
		snapshot := *command
		s.actions[snapshot.ID] = snapshot.Action
		s.persist(snapshot)

		return snapshot, true
	}

	return Command{}, false
}

func (s *Service) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, command := range s.commands {
		if command.ID == id {
			delete(s.commands, name)
			delete(s.actions, command.ID)
			delete(s.replies, command.ID)
			delete(s.limits, command.ID)
			s.persistActions()
			s.persistReplies()
			s.persistLimits()

			if s.db != nil {
				_, _ = s.db.ExecContext(context.Background(), `DELETE FROM command WHERE id = $1`, id)
			}

			return true
		}
	}

	return false
}

/* ── Обработка чата ───────────────────────────────────────────────── */

func (s *Service) Variables() []Variable {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Variable, 0, len(s.custom))

	for _, variable := range s.custom {
		result = append(result, variable)
	}

	return result
}

func (s *Service) SaveVariable(variable Variable) (Variable, error) {
	variable.Name = strings.TrimSpace(strings.TrimPrefix(variable.Name, "$("))
	variable.Name = strings.TrimSuffix(variable.Name, ")")
	variable.Name = strings.ToLower(strings.TrimSpace(variable.Name))

	if variable.Name == "" {
		return Variable{}, fmt.Errorf("имя переменной обязательно")
	}

	if variable.ID == "" {
		variable.ID = fmt.Sprintf("var-%d", time.Now().UnixNano())
	}

	s.mu.Lock()
	s.custom[variable.Name] = variable
	s.mu.Unlock()

	s.persistVariables()

	return variable, nil
}

func (s *Service) DeleteVariable(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, variable := range s.custom {
		if variable.ID == id {
			delete(s.custom, name)
			s.persistVariables()

			return true
		}
	}

	return false
}

// HandleMessage вызывается из шины чата.
func (s *Service) HandleMessage(message twitch.TwitchChatMessage) {
	if bots.Is(message.Author.Login) {
		return
	}

	text := strings.TrimSpace(message.Text)

	if !strings.HasPrefix(text, "!") {
		return
	}

	parts := strings.Fields(text)
	name := normalizeName(parts[0])

	s.mu.Lock()

	command, ok := s.commands[name]

	if !ok {
		command = s.findByAliasLocked(name)
	}

	if command == nil || !command.Enabled {
		s.mu.Unlock()

		return
	}

	userKey := command.Name + ":" + message.Author.ID
	now := s.now()

	if until, cooling := s.cooldowns[userKey]; cooling && now.Before(until) {
		limitReply := strings.TrimSpace(s.expand(command.LimitReply, message))
		s.mu.Unlock()

		if limitReply != "" && s.writer != nil {
			mention := "@" + message.Author.Login
			if !strings.Contains(strings.ToLower(limitReply), strings.ToLower(mention)) {
				limitReply = mention + ", " + limitReply
			}
			_ = s.writer.Say(limitReply)
		}

		return
	}

	command.Counter++
	response := render(command.Response, message, command.Counter)

	if command.CooldownSeconds > 0 {
		s.cooldowns[userKey] = now.Add(time.Duration(command.CooldownSeconds) * time.Second)
	}

	s.mu.Unlock()

	s.persist(*command)
	s.execute(*command, message, s.expand(response, message))
}

func (s *Service) Run(id string, message twitch.TwitchChatMessage) bool {
	s.mu.Lock()

	for _, command := range s.commands {
		if command.ID != id || !command.Enabled {
			continue
		}

		snapshot := *command
		response := render(snapshot.Response, message, snapshot.Counter+1)
		s.mu.Unlock()
		s.execute(snapshot, message, s.expand(response, message))

		return true
	}

	s.mu.Unlock()

	return false
}

var variablePattern = regexp.MustCompile(`\$\(([a-zA-Z0-9_]+)\)|\$\{([a-zA-Z0-9_]+)\}`)

func (s *Service) expand(text string, message twitch.TwitchChatMessage) string {
	return s.expandDepth(text, message, 0)
}

func (s *Service) expandDepth(text string, message twitch.TwitchChatMessage, depth int) string {
	if text == "" || depth > 10 {
		return text
	}

	return variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "$("), ")")
		if strings.HasPrefix(match, "${") {
			name = strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		}

		s.mu.Lock()
		variable, ok := s.custom[strings.ToLower(name)]
		s.mu.Unlock()

		if ok {
			if variable.Action != "" && s.variables != nil {
				if value := s.variables(variable.Action, variable.Value, message); value != "" {
					return value
				}
			}

			if variable.Value != "" {
				return s.expandDepth(variable.Value, message, depth+1)
			}
		}

		if s.variables != nil {
			if value := s.variables(name, "", message); value != "" {
				return value
			}
		}

		return match
	})
}

func diceArgument(message twitch.TwitchChatMessage) string {
	fields := strings.Fields(message.Text)
	if len(fields) > 1 {
		return fields[1]
	}

	return ""
}

func isDiceCommand(command Command, message twitch.TwitchChatMessage) bool {
	fields := strings.Fields(message.Text)
	if len(fields) == 0 {
		return false
	}

	if normalizeName(command.Name) == "dice" {
		return true
	}

	for _, alias := range command.Aliases {
		if normalizeName(alias) == "dice" {
			return true
		}
	}

	return false
}

func diceLimits(value string, message twitch.TwitchChatMessage) (int, int) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if fields := strings.Fields(message.Text); len(fields) > 1 {
		raw = strings.ToLower(strings.TrimSpace(fields[1]))
	}

	count, sides := 1, 6
	if _, err := fmt.Sscanf(raw, "%dd%d", &count, &sides); err != nil {
		if _, err := fmt.Sscanf(raw, "d%d", &sides); err != nil {
			return 1, 6
		}
	}

	return count, sides
}

func (s *Service) sendLimitReply(command Command, message twitch.TwitchChatMessage, fallback string) {
	reply := strings.TrimSpace(s.expand(command.LimitReply, message))
	if reply == "" {
		reply = fallback
	}

	if s.writer == nil || reply == "" {
		return
	}

	mention := "@" + message.Author.Login
	if !strings.Contains(strings.ToLower(reply), strings.ToLower(mention)) {
		reply = mention + ", " + reply
	}
	_ = s.writer.Say(singleChatMessage(reply, 500))
}

func (s *Service) execute(command Command, message twitch.TwitchChatMessage, response string) {
	defer func() { s.sendReply(command, message, response) }()

	switch command.Action {
	case "variable.set":
		s.setVariableAction(strings.TrimSpace(command.Response), false)
		return
	case "variable.inc", "variable.dec":
		delta := 1
		if command.Action == "variable.dec" {
			delta = -1
		}
		s.setVariableAction(strings.TrimSpace(command.Response), true, delta)
		return
	case "command.run":
		s.runByName(strings.TrimSpace(command.Response), message)
		return
	}

	if command.Action == "dice" || (command.Action == "say" && isDiceCommand(command, message)) {
		value := strings.TrimSpace(command.Response)
		if command.Action == "say" {
			value = diceArgument(message)
		} else if fields := strings.Fields(message.Text); len(fields) > 1 {
			value = strings.Join(fields[1:], " ")
		}

		count, sides := diceLimits(value, message)
		if count > 20 || sides > 100 {
			s.sendLimitReply(command, message, "максимум 100 сторон и 20 кубиков")
			command.Reply = ""
			response = ""

			return
		}
	}

	if command.Action == "say" && isDiceCommand(command, message) {
		log.Printf("[Dice] say command=%q text=%q arg=%q", command.Name, message.Text, diceArgument(message))
		if advanced, ok := s.actioner.(AdvancedActioner); ok {
			text, err, handled := advanced.Perform(context.Background(), "dice", diceArgument(message), message)
			log.Printf("[Dice] perform handled=%v err=%v text=%q", handled, err, text)
		} else {
			log.Printf("[Dice] actioner does not implement AdvancedActioner")
		}
	}

	if advanced, ok := s.actioner.(AdvancedActioner); ok && isAdvancedAction(command.Action) {
		value := strings.TrimSpace(command.Response)
		if command.Action == "dice" {
			if fields := strings.Fields(message.Text); len(fields) > 1 {
				value = strings.Join(fields[1:], " ")
			}
		}
		if text, err, handled := advanced.Perform(context.Background(), command.Action, value, message); handled {
			if err != nil {
				log.Printf("[Commands] %s failed: %v", command.Action, err)

				return
			}

			if text != "" && s.writer != nil {
				_ = s.writer.Say(s.expand(text, message))
			}

			command.Reply = ""
			response = ""

			return
		}
	}

	if s.actioner != nil {
		switch command.Action {
		case "points":
			if amount, err := strconv.Atoi(strings.TrimSpace(command.Response)); err == nil {
				name := message.Author.DisplayName
				if name == "" {
					name = message.Author.Login
				}
				_ = s.actioner.AwardPoints(message.Author.ID, message.Author.Login, name, amount, "command: "+command.Name)
			}

			return
		case "timeout":
			if seconds, err := strconv.Atoi(strings.TrimSpace(command.Response)); err == nil {
				_ = s.actioner.TimeoutUser(context.Background(), message.Author.ID, seconds, "command: "+command.Name)
			}

			return
		case "music":
			url := strings.TrimSpace(message.Text)
			if url == "" {
				url = strings.TrimSpace(command.Response)
			}
			_ = s.actioner.AddMusic(context.Background(), url, message.Author.Login)

			return
		case "clip":
			_ = s.actioner.Clip(context.Background())

			return
		default:
			if strings.HasPrefix(command.Action, "twitch.") || strings.HasPrefix(command.Action, "points.") || command.Action == "quotes.random" {
				text := s.actioner.Twitch(context.Background(), command.Action, message.Author.ID)
				if text != "" && s.writer != nil {
					_ = s.writer.Say(text)
				}

				return
			}
		case "lastmusic":
			if s.writer != nil {
				_ = s.writer.Say(s.expand("Сейчас играет: $(lastmusic)", message))
			}

			return
		case "wheel":
			title := strings.TrimSpace(command.Response)
			if title == "" {
				title = command.Name
			}
			name := message.Author.DisplayName
			if name == "" {
				name = message.Author.Login
			}
			_ = s.actioner.WheelEntry(message.Author.ID, message.Author.Login, name, title, 0)

			return
		}
	}

}

func (s *Service) setVariableAction(raw string, increment bool, delta ...int) {
	name, value, _ := strings.Cut(raw, "=")
	name = strings.TrimSpace(strings.TrimPrefix(name, "$("))
	name = strings.TrimSuffix(name, ")")
	if name == "" {
		return
	}

	s.mu.Lock()
	variable, ok := s.custom[strings.ToLower(name)]
	if !ok {
		variable = Variable{ID: name, Name: name}
	}

	if increment {
		step := 1
		if len(delta) > 0 {
			step = delta[0]
		}
		current := 0
		if parsed, err := strconv.Atoi(strings.TrimSpace(variable.Value)); err == nil {
			current = parsed
		}
		variable.Value = strconv.Itoa(current + step)
	} else {
		variable.Value = strings.TrimSpace(value)
	}

	s.custom[strings.ToLower(name)] = variable
	s.mu.Unlock()
	s.persistVariables()
}

func (s *Service) RunByName(name string, message twitch.TwitchChatMessage) bool {
	s.mu.Lock()
	var target *Command
	for _, command := range s.commands {
		if command.Name == normalizeName(strings.TrimPrefix(strings.TrimSpace(name), "!")) {
			target = command

			break
		}
	}
	s.mu.Unlock()

	if target == nil {
		return false
	}

	return s.Run(target.ID, message)
}

func (s *Service) ApplyVariable(action, raw string) {
	switch action {
	case "variable.inc":
		s.setVariableAction(raw, true, 1)
	case "variable.dec":
		s.setVariableAction(raw, true, -1)
	default:
		s.setVariableAction(raw, false)
	}
}

func (s *Service) runByName(raw string, message twitch.TwitchChatMessage) {
	name := normalizeName(strings.TrimPrefix(strings.TrimSpace(raw), "!"))

	s.mu.Lock()
	var target *Command
	for _, command := range s.commands {
		if command.Name == name {
			target = command

			break
		}
	}
	s.mu.Unlock()

	if target != nil {
		s.Run(target.ID, message)
	}
}

func singleChatMessage(text string, limit int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.Join(strings.Fields(text), " ")

	if len([]rune(text)) <= limit {
		return text
	}

	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

func (s *Service) sendReply(command Command, message twitch.TwitchChatMessage, response string) {
	reply := strings.TrimSpace(s.expand(command.Reply, message))
	if reply == "" {
		reply = strings.TrimSpace(s.expand(response, message))
	}

	if s.writer == nil || reply == "" {
		return
	}

	mention := "@" + message.Author.Login
	if !strings.Contains(strings.ToLower(reply), strings.ToLower(mention)) {
		reply = mention + ", " + reply
	}

	if err := s.writer.Say(singleChatMessage(reply, 500)); err != nil {
		log.Printf("[Commands] send failed: %v", err)
	}
}

// Test рендерит ответ команды без отправки (кнопка «Тест» в панели).
func (s *Service) Test(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, command := range s.commands {
		if command.ID == id {
			sample := twitch.TwitchChatMessage{
				Author: twitch.ChatUser{ID: "test", Login: "tester", DisplayName: "Тестер"},
			}

			return render(command.Response, sample, command.Counter+1), true
		}
	}

	return "", false
}

func (s *Service) findByAliasLocked(alias string) *Command {
	for _, command := range s.commands {
		for _, item := range command.Aliases {
			if normalizeName(item) == alias {
				return command
			}
		}
	}

	return nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "!"))
}

// render подставляет переменные: $(user), $(login), $(channel), $(count), $(date), $(time).
func render(template string, message twitch.TwitchChatMessage, counter int) string {
	now := time.Now()

	channel := message.Broadcaster.DisplayName

	if channel == "" {
		channel = message.Broadcaster.Login
	}

	display := message.Author.DisplayName

	if display == "" {
		display = message.Author.Login
	}

	replacements := map[string]string{
		"$(user)":    display,
		"$(login)":   message.Author.Login,
		"$(channel)": channel,
		"$(count)":   fmt.Sprintf("%d", counter),
		"$(date)":    now.Format("02.01.2006"),
		"$(time)":    now.Format("15:04"),
	}

	result := template

	for key, value := range replacements {
		result = strings.ReplaceAll(result, key, value)
	}

	return result
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) loadActions() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'command_actions'`).Scan(&raw); err != nil {
		return
	}

	_ = json.Unmarshal(raw, &s.actions)

	for _, command := range s.commands {
		if action := s.actions[command.ID]; action != "" {
			command.Action = action
		}
	}
}

func (s *Service) persistActions() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.actions)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('command_actions', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

func (s *Service) loadReplies() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'command_replies'`).Scan(&raw); err != nil {
		return
	}

	_ = json.Unmarshal(raw, &s.replies)

	for _, command := range s.commands {
		if reply := s.replies[command.ID]; reply != "" {
			command.Reply = reply
		}
		if limit := s.limits[command.ID]; limit != "" {
			command.LimitReply = limit
		}
	}
}

func (s *Service) persistReplies() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.replies)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('command_replies', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

func (s *Service) loadLimits() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'command_limits'`).Scan(&raw); err != nil {
		return
	}

	_ = json.Unmarshal(raw, &s.limits)
	for _, command := range s.commands {
		if limit := s.limits[command.ID]; limit != "" {
			command.LimitReply = limit
		}
	}
}

func (s *Service) persistLimits() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.limits)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('command_limits', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

func (s *Service) loadVariables() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'command_variables'`).Scan(&raw); err != nil {
		return
	}

	var list []Variable
	if json.Unmarshal(raw, &list) != nil {
		return
	}

	for _, variable := range list {
		s.custom[variable.Name] = variable
	}
}

func (s *Service) persistVariables() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.Variables())
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('command_variables', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

func (s *Service) persist(command Command) {
	if s.db == nil {
		return
	}

	aliases, err := json.Marshal(command.Aliases)
	if err != nil {
		return
	}

	query := `
		INSERT INTO command (id, name, aliases, response, cooldown_seconds, user_level, enabled, counter, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			aliases = EXCLUDED.aliases,
			response = EXCLUDED.response,
			cooldown_seconds = EXCLUDED.cooldown_seconds,
			user_level = EXCLUDED.user_level,
			enabled = EXCLUDED.enabled,
			counter = EXCLUDED.counter
	`

	_, err = s.db.ExecContext(context.Background(), query,
		command.ID,
		command.Name,
		string(aliases),
		command.Response,
		command.CooldownSeconds,
		command.UserLevel,
		command.Enabled,
		command.Counter,
		command.CreatedAt,
	)
	if err != nil {
		log.Printf("[Commands] persist failed: %v", err)
	}
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, name, aliases, response, cooldown_seconds, user_level, enabled, counter, created_at FROM command`)
	if err != nil {
		log.Printf("[Commands] load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			command Command
			aliases string
		)

		if err := rows.Scan(
			&command.ID,
			&command.Name,
			&aliases,
			&command.Response,
			&command.CooldownSeconds,
			&command.UserLevel,
			&command.Enabled,
			&command.Counter,
			&command.CreatedAt,
		); err != nil {
			continue
		}

		_ = json.Unmarshal([]byte(aliases), &command.Aliases)
		command.Action = "say"
		if action := s.actions[command.ID]; action != "" {
			command.Action = action
		}
		if reply := s.replies[command.ID]; reply != "" {
			command.Reply = reply
		}
		if limit := s.limits[command.ID]; limit != "" {
			command.LimitReply = limit
		}
		s.commands[command.Name] = &command
	}
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/command-variables", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeJSON(response, http.StatusOK, map[string]any{"variables": service.Variables()})
		case http.MethodPost:
			var variable Variable
			if err := json.NewDecoder(request.Body).Decode(&variable); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}
			saved, err := service.SaveVariable(variable)
			if err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

				return
			}
			writeJSON(response, http.StatusOK, saved)
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/command-variables/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}
		if !service.DeleteVariable(request.PathValue("id")) {
			http.Error(response, "not found", http.StatusNotFound)

			return
		}
		response.WriteHeader(http.StatusNoContent)
	})

	srv.ServeEndpoint("/rgg-sync/commands", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"commands": service.List()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var command Command

		if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		saved, err := service.Upsert(command)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, saved)
	})

	srv.ServeEndpoint("/rgg-sync/commands/export", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		list := service.List()
		sort.Slice(list, func(left, right int) bool {
			return strings.ToLower(list[left].Name) < strings.ToLower(list[right].Name)
		})

		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Content-Disposition", `attachment; filename="streamhata-commands.txt"`)
		_, _ = fmt.Fprintf(response, "# StreamHata commands: %d\n\n", len(list))

		for _, command := range list {
			_, _ = fmt.Fprintf(response, "!%s\n", command.Name)
			_, _ = fmt.Fprintf(response, "aliases: %s\n", strings.Join(command.Aliases, ", "))
			_, _ = fmt.Fprintf(response, "action: %s\n", command.Action)
			_, _ = fmt.Fprintf(response, "cooldown: %d\n", command.CooldownSeconds)
			_, _ = fmt.Fprintf(response, "level: %s\n", command.UserLevel)
			_, _ = fmt.Fprintf(response, "enabled: %t\n", command.Enabled)
			_, _ = fmt.Fprintf(response, "response: |\n  %s\n", strings.ReplaceAll(command.Response, "\n", "\n  "))
			_, _ = fmt.Fprintf(response, "reply: |\n  %s\n", strings.ReplaceAll(command.Reply, "\n", "\n  "))
			_, _ = fmt.Fprintf(response, "limitReply: |\n  %s\n", strings.ReplaceAll(command.LimitReply, "\n", "\n  "))
			_, _ = fmt.Fprint(response, "---\n")
		}
	})

	srv.ServeEndpoint("/rgg-sync/commands/{id}/test", func(response http.ResponseWriter, request *http.Request) {
		text, ok := service.Test(request.PathValue("id"))

		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

			return
		}

		writeJSON(response, http.StatusOK, map[string]string{"response": text})
	})

	srv.ServeEndpoint("/rgg-sync/commands/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPatch {
			var body struct {
				Enabled bool `json:"enabled"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			command, ok := service.SetCommandEnabled(request.PathValue("id"), body.Enabled)
			if !ok {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

				return
			}

			writeJSON(response, http.StatusOK, command)

			return
		}

		if request.Method != http.MethodDelete {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if !service.Delete(request.PathValue("id")) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "command not found"})

			return
		}

		response.WriteHeader(http.StatusNoContent)
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
