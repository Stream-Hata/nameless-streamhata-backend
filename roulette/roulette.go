// Package roulette — серверная рулетка игр: classic, cat-bag, elimination.
package roulette

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/catalog"
)

const (
	configKey = "roulette_config"
	stateKey  = "roulette_state"
)

var (
	ErrEmptyPool = errors.New("empty pool")
	ErrMode      = errors.New("unknown mode")
)

type Filters struct {
	Platforms []string `json:"platforms"`
	Tiers     []string `json:"tiers"`
	Ages      []string `json:"ages"`
	Emulator  *bool    `json:"emulator"`
	YearFrom  int      `json:"yearFrom"`
	YearTo    int      `json:"yearTo"`
}

type Weights struct {
	Tiers     map[string]float64 `json:"tiers"`
	Platforms map[string]float64 `json:"platforms"`
	Games     map[string]float64 `json:"games"`
}

type Appearance struct {
	Background string  `json:"background"`
	Accent     string  `json:"accent"`
	Text       string  `json:"text"`
	FontFamily string  `json:"fontFamily"`
	FontURL    string  `json:"fontUrl"`
	Scale      float64 `json:"scale"`
	MemeURL    string  `json:"memeUrl"`
}

type Audio struct {
	MusicURL  string `json:"musicUrl"`
	RevealURL string `json:"revealUrl"`
	Volume    int    `json:"volume"`
}

type Reveal struct {
	DurationSeconds int    `json:"durationSeconds"`
	ShowCat         bool   `json:"showCat"`
	CatImage        string `json:"catImage"`
	TitleTemplate   string `json:"titleTemplate"`
}

type Config struct {
	Mode   string     `json:"mode"`
	Filter Filters    `json:"filters"`
	Weight Weights    `json:"weights"`
	Avoid  int        `json:"avoidRecent"`
	Look   Appearance `json:"appearance"`
	Audio  Audio      `json:"audio"`
	Reveal Reveal     `json:"reveal"`
}

type Round struct {
	ID        string           `json:"id"`
	Mode      string           `json:"mode"`
	Platform  catalog.Platform `json:"platform"`
	Game      catalog.Game     `json:"game"`
	CreatedAt time.Time        `json:"createdAt"`
}

type State struct {
	Config     Config   `json:"config"`
	History    []Round  `json:"history"`
	Eliminated []string `json:"eliminated"`
	Remaining  int      `json:"remaining"`
}

type persistedState struct {
	History    []Round  `json:"history"`
	Recent     []string `json:"recent"`
	Eliminated []string `json:"eliminated"`
}

type Writer interface {
	Say(text string) error
}

type Service struct {
	mu         sync.Mutex
	config     Config
	history    []Round
	recent     []string
	eliminated map[string]bool
	catalog    *catalog.Service
	db         *sql.DB
	poolCache  []catalog.Game
	poolCached time.Time
	writer     Writer
}

func (s *Service) SetWriter(writer Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.writer = writer
}

func New(db *sql.DB, games *catalog.Service) *Service {
	service := &Service{
		config:     defaultConfig(),
		eliminated: make(map[string]bool),
		catalog:    games,
		db:         db,
	}

	service.load()

	return service
}

func defaultConfig() Config {
	return Config{
		Mode: "classic",
		Filter: Filters{
			Tiers: []string{"gold", "diamond", "platinum", "none"},
			Ages:  []string{"classic", "retro", "modern", "new"},
		},
		Weight: Weights{
			Tiers:     map[string]float64{"gold": 2, "diamond": 3, "platinum": 5, "none": 1},
			Platforms: map[string]float64{},
			Games:     map[string]float64{},
		},
		Avoid: 10,
		Look: Appearance{
			Background: "#0F0817",
			Accent:     "#FF2EA6",
			Text:       "#F5EAFF",
			FontFamily: "JetBrains Mono",
			Scale:      1,
		},
		Audio: Audio{Volume: 70},
		Reveal: Reveal{
			DurationSeconds: 12,
			ShowCat:         true,
			TitleTemplate:   "{{game}} · {{platform}}",
		},
	}
}

func (s *Service) GetConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	return cloneConfig(s.config)
}

func (s *Service) SetConfig(config Config) Config {
	s.mu.Lock()
	defer s.mu.Unlock()

	if config.Mode == "" {
		config.Mode = "classic"
	}

	if config.Avoid < 0 {
		config.Avoid = 0
	}

	if config.Audio.Volume < 0 || config.Audio.Volume > 100 {
		config.Audio.Volume = 70
	}

	if config.Look.Scale <= 0 {
		config.Look.Scale = 1
	}

	s.config = config
	s.poolCache = nil
	s.persist()

	return cloneConfig(s.config)
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()

	eliminated := make([]string, 0, len(s.eliminated))

	for slug := range s.eliminated {
		eliminated = append(eliminated, slug)
	}

	remaining := 0
	if s.poolCache != nil {
		remaining = len(s.poolLocked(s.config.Mode, true))
	}

	return State{
		Config:     cloneConfig(s.config),
		History:    append([]Round(nil), s.history...),
		Eliminated: eliminated,
		Remaining:  remaining,
	}
}

func (s *Service) Reel(limit int) []catalog.Game {
	if limit < 1 {
		limit = 30
	}

	if limit > 120 {
		limit = 120
	}

	s.mu.Lock()
	pool := s.poolLocked("classic", false)
	s.mu.Unlock()

	if len(pool) == 0 {
		return []catalog.Game{}
	}

	result := make([]catalog.Game, 0, limit)
	used := make(map[int]bool)

	for len(result) < limit && len(used) < len(pool) {
		index := randomInt(len(pool))

		if used[index] {
			continue
		}

		used[index] = true
		result = append(result, pool[index])
	}

	return result
}

func (s *Service) History(limit int) []Round {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit < 1 || limit > len(s.history) {
		limit = len(s.history)
	}

	return append([]Round(nil), s.history[:limit]...)
}

func (s *Service) Spin(mode string) (Round, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if mode == "" {
		mode = s.config.Mode
	}

	if mode != "classic" && mode != "cat-bag" && mode != "elimination" {
		return Round{}, ErrMode
	}

	pool := s.poolLocked(mode, false)
	if len(pool) == 0 {
		return Round{}, ErrEmptyPool
	}

	var selected catalog.Game

	if mode == "cat-bag" {
		platforms := make(map[string][]catalog.Game)

		for _, game := range pool {
			for _, platform := range game.Platforms {
				platforms[platform.Code] = append(platforms[platform.Code], game)
			}
		}

		keys := make([]string, 0, len(platforms))
		weights := make([]float64, 0, len(platforms))

		for code, list := range platforms {
			keys = append(keys, code)
			weights = append(weights, s.weightForPlatform(code)*float64(len(list)))
		}

		index := weightedIndex(weights)
		list := platforms[keys[index]]
		selected = list[weightedGameIndex(list, s.config.Weight.Games)]
	} else {
		selected = pool[s.weightedGameIndexLocked(pool)]
	}

	round := Round{
		ID:        roundID(),
		Mode:      mode,
		Platform:  selected.Platforms[0],
		Game:      selected,
		CreatedAt: time.Now().UTC(),
	}

	s.history = append([]Round{round}, s.history...)
	if len(s.history) > 100 {
		s.history = s.history[:100]
	}

	s.recent = append(s.recent, selected.Slug)
	if s.config.Avoid > 0 && len(s.recent) > s.config.Avoid {
		s.recent = s.recent[len(s.recent)-s.config.Avoid:]
	}

	if mode == "elimination" {
		s.eliminated[selected.Slug] = true
	}

	s.persistState()

	writer := s.writer
	title := selected.Title
	if writer != nil {
		go func() {
			_ = writer.Say("Выпала игра: " + title)
		}()
	}

	return round, nil
}

func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recent = nil
	s.eliminated = make(map[string]bool)
	s.persistState()
}

func (s *Service) poolLocked(mode string, countAll bool) []catalog.Game {
	var all []catalog.Game

	if s.poolCache != nil && time.Since(s.poolCached) < 30*time.Second {
		all = s.poolCache
	} else {
		all = s.catalog.Pool(catalog.Filters{
			Emulator: s.config.Filter.Emulator,
			YearFrom: s.config.Filter.YearFrom,
			YearTo:   s.config.Filter.YearTo,
			Status:   "seed",
		})
		s.poolCache = all
		s.poolCached = time.Now()
	}

	platforms := toSet(s.config.Filter.Platforms)
	tiers := toSet(s.config.Filter.Tiers)
	ages := toSet(s.config.Filter.Ages)
	result := make([]catalog.Game, 0, len(all))

	for _, game := range all {
		if len(platforms) > 0 && !gameHasPlatform(game, platforms) {
			continue
		}

		if len(tiers) > 0 && !tiers[game.Tier] {
			continue
		}

		if len(ages) > 0 && !ages[game.Age] {
			continue
		}

		if !countAll {
			if s.eliminated[game.Slug] {
				continue
			}

			if contains(s.recent, game.Slug) {
				continue
			}
		}

		result = append(result, game)
	}

	return result
}

func (s *Service) weightedGameIndexLocked(pool []catalog.Game) int {
	weights := make([]float64, len(pool))

	for index, game := range pool {
		weights[index] = s.weightForGame(game)
	}

	return weightedIndex(weights)
}

func (s *Service) weightForGame(game catalog.Game) float64 {
	weight := s.config.Weight.Tiers[game.Tier]
	if weight <= 0 {
		weight = 1
	}

	if value := s.config.Weight.Games[game.Slug]; value > 0 {
		weight *= value
	}

	if value := s.config.Weight.Platforms[game.Platforms[0].Code]; value > 0 {
		weight *= value
	}

	return weight
}

func (s *Service) weightForPlatform(code string) float64 {
	if value := s.config.Weight.Platforms[code]; value > 0 {
		return value
	}

	return 1
}

func (s *Service) load() {
	if s.db == nil {
		return
	}

	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		configKey,
	).Scan(&raw); err == nil {
		var config Config

		if json.Unmarshal(raw, &config) == nil {
			s.config = config
		}
	}

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		stateKey,
	).Scan(&raw); err == nil {
		var state persistedState

		if json.Unmarshal(raw, &state) == nil {
			s.history = state.History
			s.recent = state.Recent

			for _, slug := range state.Eliminated {
				s.eliminated[slug] = true
			}
		}
	}
}

func (s *Service) persist() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.config)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		configKey,
		string(raw),
	)
}

func (s *Service) persistState() {
	if s.db == nil {
		return
	}

	eliminated := make([]string, 0, len(s.eliminated))

	for slug := range s.eliminated {
		eliminated = append(eliminated, slug)
	}

	raw, err := json.Marshal(persistedState{
		History:    s.history,
		Recent:     s.recent,
		Eliminated: eliminated,
	})
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		stateKey,
		string(raw),
	)
}

func toSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))

	for _, value := range values {
		if value != "" {
			result[value] = true
		}
	}

	return result
}

func gameHasPlatform(game catalog.Game, platforms map[string]bool) bool {
	for _, platform := range game.Platforms {
		if platforms[platform.Code] {
			return true
		}
	}

	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func cloneConfig(config Config) Config {
	raw, _ := json.Marshal(config)
	var result Config

	_ = json.Unmarshal(raw, &result)

	if result.Weight.Tiers == nil {
		result.Weight.Tiers = map[string]float64{}
	}

	if result.Weight.Platforms == nil {
		result.Weight.Platforms = map[string]float64{}
	}

	if result.Weight.Games == nil {
		result.Weight.Games = map[string]float64{}
	}

	return result
}

func weightedGameIndex(pool []catalog.Game, weights map[string]float64) int {
	values := make([]float64, len(pool))

	for index, game := range pool {
		value := weights[game.Slug]
		if value <= 0 {
			value = 1
		}

		values[index] = value
	}

	return weightedIndex(values)
}

func weightedIndex(weights []float64) int {
	total := 0.0

	for _, weight := range weights {
		if weight > 0 {
			total += weight
		}
	}

	if total <= 0 {
		return randomInt(len(weights))
	}

	target := randomFloat() * total
	sum := 0.0

	for index, weight := range weights {
		if weight <= 0 {
			continue
		}

		sum += weight
		if target <= sum {
			return index
		}
	}

	return len(weights) - 1
}

func randomInt(total int) int {
	if total <= 0 {
		return 0
	}

	value, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return 0
	}

	return int(value.Int64())
}

func randomFloat() float64 {
	value, err := rand.Int(rand.Reader, big.NewInt(1<<31))
	if err != nil {
		return 0
	}

	return float64(value.Int64()) / float64(1<<31)
}

func roundID() string {
	buffer := make([]byte, 12)

	if _, err := rand.Read(buffer); err != nil {
		return time.Now().Format("20060102150405")
	}

	return hexEncode(buffer)
}

func hexEncode(buffer []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(buffer)*2)

	for index, value := range buffer {
		result[index*2] = digits[value>>4]
		result[index*2+1] = digits[value&0x0f]
	}

	return string(result)
}
