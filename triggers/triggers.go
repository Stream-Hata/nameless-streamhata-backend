// Package triggers — настраиваемые стримером команды для всех подсистем:
// баллы, игры, цитаты, жюри, клипы.
package triggers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type Trigger struct {
	Action      string   `json:"action"`
	Command     string   `json:"command"`
	Aliases     []string `json:"aliases"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description"`
}

func defaults() []Trigger {
	return []Trigger{
		{Action: "points.balance", Command: "!points", Aliases: []string{"!баланс", "!balance"}, Enabled: true, Description: "Баланс баллов"},
		{Action: "points.top", Command: "!top", Aliases: nil, Enabled: true, Description: "Топ зрителей"},
		{Action: "points.shop", Command: "!shop", Aliases: []string{"!магазин"}, Enabled: true, Description: "Магазин наград"},
		{Action: "points.redeem", Command: "!redeem", Aliases: nil, Enabled: true, Description: "Купить награду"},
		{Action: "games.slots", Command: "!slots", Aliases: []string{"!слоты"}, Enabled: true, Description: "Слоты"},
		{Action: "games.wheel", Command: "!wheel", Aliases: []string{"!колесо"}, Enabled: true, Description: "Колесо"},
		{Action: "games.trivia", Command: "!trivia", Aliases: []string{"!викторина"}, Enabled: true, Description: "Викторина"},
		{Action: "games.duel", Command: "!duel", Aliases: []string{"!дуэль"}, Enabled: true, Description: "Дуэль: вызов"},
		{Action: "games.accept", Command: "!accept", Aliases: []string{"!принять"}, Enabled: true, Description: "Дуэль: принять"},
		{Action: "quotes.quote", Command: "!quote", Aliases: nil, Enabled: true, Description: "Цитаты"},
		{Action: "jury.yea", Command: "!yea", Aliases: []string{"!ya"}, Enabled: true, Description: "Голос жюри: за"},
		{Action: "jury.nei", Command: "!nei", Aliases: []string{"!no"}, Enabled: true, Description: "Голос жюри: против"},
		{Action: "clips.clip", Command: "!clip", Aliases: nil, Enabled: true, Description: "Создать клип"},
		{Action: "music.add", Command: "!music", Aliases: []string{"!музыка"}, Enabled: true, Description: "Добавить музыку"},
	}
}

type Service struct {
	mu     sync.RWMutex
	list   []Trigger
	byName map[string]string
	db     *sql.DB
}

func New(db *sql.DB) *Service {
	service := &Service{db: db, list: defaults()}
	service.load()
	service.rebuild()

	return service
}

func (s *Service) ID() string { return "triggers" }

func (s *Service) Name() string { return "Триггеры чата" }

func (s *Service) Kind() string { return "triggers" }

func (s *Service) Enabled() bool { return true }

func (s *Service) SetEnabled(bool) error { return nil }

func (s *Service) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return fmt.Sprintf("триггеров: %d", len(s.list))
}

func (s *Service) List() []Trigger {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return append([]Trigger(nil), s.list...)
}

// Update сохраняет список (уникальные команды/алиасы, известные действия).
func (s *Service) Update(list []Trigger) ([]Trigger, error) {
	known := map[string]bool{}
	for _, item := range defaults() {
		known[item.Action] = true
	}
	for _, action := range []string{
		"say", "announce", "shoutout", "raid", "raid.cancel", "marker", "clip", "delete", "ban", "unban",
		"timeout", "shield.on", "shield.off", "title.set", "game.set", "commercial", "poll", "poll.end",
		"points.add", "points.remove", "points.set", "points.balance", "points.top", "points.shop",
		"points.redeem", "music.add", "music.current", "lastmusic", "wheel", "games.slots", "games.wheel",
		"games.trivia", "games.duel", "games.accept", "quote.add", "quotes.random", "dice", "coinflip", "random.number",
		"random", "variable.set", "variable.inc", "variable.dec", "command.run", "timer.start",
		"timer.pause", "timer.reset", "timer.add", "twitch.followage", "twitch.accountage", "twitch.uptime",
		"twitch.viewers", "twitch.title", "twitch.game", "twitch.followers", "twitch.subscribers", "twitch.chatters",
		"widget.enable", "widget.disable", "widget.toggle", "widget.config", "alert.test", "roulette.spin",
		"roulette.last", "reward.create", "reward.update", "reward.delete", "reward.fulfill", "mod.add", "mod.remove",
		"vip.add", "vip.remove", "chat.settings", "widget.enabled",
	} {
		known[action] = true
	}

	seen := map[string]bool{}
	next := make([]Trigger, 0, len(list))

	for _, item := range list {
		if !known[item.Action] {
			continue
		}

		command := normalize(item.Command)
		if command == "" {
			return s.List(), fmt.Errorf("пустая команда для %s", item.Action)
		}

		if seen[command] {
			return s.List(), fmt.Errorf("команда %s повторяется", command)
		}

		seen[command] = true
		item.Command = "!" + command

		aliases := make([]string, 0, len(item.Aliases))

		for _, alias := range item.Aliases {
			alias = normalize(alias)

			if alias == "" || seen[alias] {
				continue
			}

			seen[alias] = true
			aliases = append(aliases, "!"+alias)
		}

		item.Aliases = aliases
		next = append(next, item)
	}

	s.mu.Lock()
	s.list = next
	s.rebuildLocked()
	s.mu.Unlock()

	s.persist()

	return s.List(), nil
}

// Match определяет действие по имени команды и возвращает аргументы.
func (s *Service) CommandFor(action string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, item := range s.list {
		if item.Action == action && item.Enabled {
			return item.Command
		}
	}

	return ""
}

func (s *Service) Match(text string) (string, []string, bool) {
	fields := strings.Fields(text)

	if len(fields) == 0 {
		return "", nil, false
	}

	name := normalize(fields[0])

	s.mu.RLock()
	action, ok := s.byName[name]
	s.mu.RUnlock()

	if !ok {
		return "", nil, false
	}

	return action, fields[1:], true
}

func (s *Service) rebuild() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rebuildLocked()
}

func (s *Service) rebuildLocked() {
	s.byName = make(map[string]string)

	for _, item := range s.list {
		if !item.Enabled {
			continue
		}

		s.byName[normalize(item.Command)] = item.Action

		for _, alias := range item.Aliases {
			s.byName[normalize(alias)] = item.Action
		}
	}
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "!"))
}

/* ── Персистентность ──────────────────────────────────────────────── */

func (s *Service) load() {
	if s.db == nil {
		return
	}

	var raw []byte

	if err := s.db.QueryRowContext(context.Background(),
		`SELECT value FROM app_setting WHERE key = 'triggers'`).Scan(&raw); err != nil {
		return
	}

	var stored []Trigger

	if err := json.Unmarshal(raw, &stored); err == nil && len(stored) > 0 {
		s.list = stored
	}
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.List())
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('triggers', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/triggers", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writeJSON(response, http.StatusOK, map[string]any{"triggers": service.List()})

			return
		}

		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Triggers []Trigger `json:"triggers"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		list, err := service.Update(body.Triggers)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"triggers": list})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
