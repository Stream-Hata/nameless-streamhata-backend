package catalog

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	cacheKey       = "games_catalog_cache"
	cacheVersion   = 1
	defaultURL     = "https://rgg.land/lists/cat.txt"
	refreshEvery   = 5 * time.Minute
	maxCatalogSize = 128 << 20
)

type GameSource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	ExternalID string `json:"externalId,omitempty"`
	ImportedAt string `json:"importedAt"`
}

type DownloadLink struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Language string `json:"language,omitempty"`
	Note     string `json:"note,omitempty"`
}

type Game struct {
	ID                 string              `json:"id"`
	Slug               string              `json:"slug"`
	Title              string              `json:"title"`
	TitleRu            string              `json:"titleRu,omitempty"`
	SearchTags         []string            `json:"searchTags,omitempty"`
	OriginalTitle      string              `json:"originalTitle,omitempty"`
	Aliases            []string            `json:"aliases,omitempty"`
	Year               int                 `json:"year,omitempty"`
	ReleaseDate        string              `json:"releaseDate,omitempty"`
	Platforms          []Platform          `json:"platforms"`
	Genres             []string            `json:"genres,omitempty"`
	GenresRU           []string            `json:"genresRu,omitempty"`
	Modes              []string            `json:"modes,omitempty"`
	Perspectives       []string            `json:"perspectives,omitempty"`
	Themes             []string            `json:"themes,omitempty"`
	Developers         []string            `json:"developers,omitempty"`
	Publishers         []string            `json:"publishers,omitempty"`
	Description        string              `json:"description,omitempty"`
	DescriptionRu      string              `json:"descriptionRu,omitempty"`
	Cover              string              `json:"cover,omitempty"`
	Screenshots        []string            `json:"screenshots,omitempty"`
	Tier               string              `json:"tier"`
	Age                string              `json:"age"`
	Status             string              `json:"status"`
	EmulatorsRequired  bool                `json:"emulatorsRequired"`
	Emulators          []Emulator          `json:"emulators"`
	Sources            []GameSource        `json:"sources"`
	Completion         *Completion         `json:"completion,omitempty"`
	Downloads          []DownloadLink      `json:"downloads,omitempty"`
	AgeRating          string              `json:"ageRating,omitempty"`
	Rating             float64             `json:"rating,omitempty"`
	AggregatedRating   float64             `json:"aggregatedRating,omitempty"`
	DLC                []string            `json:"dlc,omitempty"`
	Editions           []string            `json:"editions,omitempty"`
	Mods               []string            `json:"mods,omitempty"`
	PopularMods        []PopularMod        `json:"popularMods,omitempty"`
	PopularCollections []PopularCollection `json:"popularCollections,omitempty"`
	Website            string              `json:"website,omitempty"`
	CreatedAt          string              `json:"createdAt"`
	UpdatedAt          string              `json:"updatedAt"`
}

type Filters struct {
	Query      string
	Platform   string
	Tier       string
	Age        string
	Emulator   *bool
	YearFrom   int
	YearTo     int
	Status     string
	Verified   *bool
	Page       int
	PageSize   int
	Sort       string
	Order      string
	SkipFacets bool
	SkipEnrich bool
}

type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type PlatformFacet struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Facets struct {
	Platforms []PlatformFacet `json:"platforms"`
	Tiers     []FacetValue    `json:"tiers"`
	Ages      []FacetValue    `json:"ages"`
}

type ListResponse struct {
	Items    []Game `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
	Facets   Facets `json:"facets"`
}

type cachePayload struct {
	Version int       `json:"version"`
	Hash    string    `json:"hash"`
	Updated time.Time `json:"updated"`
	Games   []Game    `json:"games"`
}

type Service struct {
	mu           sync.RWMutex
	games        []Game
	bySlug       map[string]int
	hash         string
	etag         string
	lastModified string
	updated      time.Time
	db           *sql.DB
	client       *http.Client
	sourceURL    string
	rggTitles    map[string]bool
	enricher     *Enricher
	enrichCursor atomic.Uint64
	importing    atomic.Bool
}

func New(db *sql.DB) *Service {
	service := &Service{
		bySlug:    make(map[string]int),
		db:        db,
		client:    &http.Client{Timeout: 90 * time.Second},
		sourceURL: sourceURL(),
		rggTitles: make(map[string]bool),
		enricher:  newEnricher(db),
	}

	initRAWGQuota(db)

	if strings.TrimSpace(os.Getenv("CATALOG_ENABLED")) != "0" {
		service.loadHash()
		go service.refreshLoop()
		go service.importLoop()
	}

	go service.enrichLoop()

	return service
}

func sourceURL() string {
	if value := strings.TrimSpace(os.Getenv("GAMES_CATALOG_URL")); value != "" {
		return value
	}

	return defaultURL
}

func (s *Service) refreshLoop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	_ = s.Refresh(ctx)
	cancel()

	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_ = s.Refresh(ctx)
		cancel()
	}
}

func (s *Service) Refresh(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.sourceURL, nil)
	if err != nil {
		return err
	}

	s.mu.RLock()
	etag := s.etag
	lastModified := s.lastModified
	s.mu.RUnlock()

	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}

	if lastModified != "" {
		request.Header.Set("If-Modified-Since", lastModified)
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (+catalog importer)")

	response, err := s.client.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode == http.StatusNotModified {
		return nil
	}

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("catalog: unexpected status %d", response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogSize))
	if err != nil {
		return err
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	s.mu.RLock()
	unchanged := hash == s.hash
	s.mu.RUnlock()

	if unchanged {
		return nil
	}

	now := time.Now().UTC()
	games, _ := parseCatalog(data, s.sourceURL, now)

	if err := s.upsertCatalog(ctx, games); err != nil {
		return err
	}

	s.mu.Lock()
	s.hash = hash
	s.etag = response.Header.Get("ETag")
	s.lastModified = response.Header.Get("Last-Modified")
	s.updated = now
	s.mu.Unlock()

	s.saveHash(hash)
	log.Printf("[catalog] upserted %d games from %s", len(games), s.sourceURL)

	return nil
}

func (s *Service) upsertCatalog(ctx context.Context, games []Game) error {
	if s.db == nil || len(games) == 0 {
		return nil
	}

	const batchSize = 500

	for start := 0; start < len(games); start += batchSize {
		end := start + batchSize
		if end > len(games) {
			end = len(games)
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		statement, err := tx.PrepareContext(ctx, `
			INSERT INTO catalog_game (slug, title, source, payload, updated_at)
			VALUES ($1, $2, 'rgg-cat', $3, now())
			ON CONFLICT (slug) DO UPDATE SET
				title = EXCLUDED.title,
				payload = EXCLUDED.payload,
				updated_at = now()
		`)
		if err != nil {
			_ = tx.Rollback()

			return err
		}

		for _, game := range games[start:end] {
			raw, err := json.Marshal(game)
			if err != nil {
				continue
			}

			if _, err := statement.ExecContext(ctx, game.Slug, game.Title, raw); err != nil {
				_ = statement.Close()
				_ = tx.Rollback()

				return err
			}
		}

		_ = statement.Close()

		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) loadHash() {
	if s.db == nil {
		return
	}

	var hash string

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = 'catalog_hash'`,
	).Scan(&hash); err == nil {
		s.hash = hash
	}
}

func (s *Service) saveHash(hash string) {
	if s.db == nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('catalog_hash', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		hash,
	)
}

func parseCatalog(data []byte, source string, now time.Time) ([]Game, map[string]int) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	games := make([]Game, 0, 128*1024)
	bySlug := make(map[string]int, 128*1024)
	importedAt := now.Format(time.RFC3339)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		title, label, ok := splitLine(line)
		if !ok {
			continue
		}

		original := title
		title = sanitizeTitle(title)
		status := "seed"

		if original != title {
			status = "pending"
		}

		platform := platformByLabel(label)
		baseSlug := slugify(title)
		if baseSlug == "" {
			baseSlug = fmt.Sprintf("game-%x", sha1.Sum([]byte(line)))[:13]
		}

		slug := baseSlug + "-" + platform.Code

		for index := 2; bySlug[slug] != 0; index++ {
			slug = fmt.Sprintf("%s-%d", baseSlug+"-"+platform.Code, index)
		}

		year := extractYear(title)
		age := ageFor(year, platform)
		emulators := emulatorsForPlatform(platform.Code)

		games = append(games, Game{
			ID:                shortID(slug),
			Slug:              slug,
			Title:             title,
			Year:              year,
			Platforms:         []Platform{platform},
			Tier:              tierFor(title, platform, year),
			Age:               age,
			Status:            status,
			EmulatorsRequired: len(emulators) > 0,
			Emulators:         emulators,
			Sources: []GameSource{{
				ID:         "rgg-cat",
				Name:       "RGG cat.txt",
				URL:        source,
				ImportedAt: importedAt,
			}},
			Downloads: defaultDownloads(title),
			CreatedAt: importedAt,
			UpdatedAt: importedAt,
		})

		bySlug[slug] = len(games) - 1
	}

	return games, bySlug
}

func sanitizeTitle(value string) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	value = strings.ReplaceAll(value, "\uFEFF", "")

	if repaired, ok := repairMojibake(value); ok {
		value = repaired
	}

	return strings.TrimSpace(value)
}

func repairMojibake(value string) (string, bool) {
	if !strings.ContainsAny(value, "ÐÑÂÃ") {
		return value, false
	}

	raw := make([]byte, 0, len(value))

	for _, r := range value {
		if r > 255 {
			return value, false
		}

		raw = append(raw, byte(r))
	}

	if !utf8.Valid(raw) {
		return value, false
	}

	repaired := string(raw)

	if repaired == value {
		return value, false
	}

	return repaired, true
}

func defaultDownloads(title string) []DownloadLink {
	query := url.QueryEscape(title)

	return []DownloadLink{
		{Name: "Old-Games.RU", URL: "https://www.old-games.ru/search/?q=" + query, Language: "RU", Note: "Ретро и DOS"},
		{Name: "MyAbandonware", URL: "https://www.myabandonware.com/search/?q=" + query, Language: "EN", Note: "Abandonware"},
		{Name: "Internet Archive", URL: "https://archive.org/search?query=" + query, Language: "multi", Note: "Архив"},
		{Name: "GOG", URL: "https://www.gog.com/games?query=" + query, Language: "EN", Note: "Купить"},
		{Name: "Steam", URL: "https://store.steampowered.com/search/?term=" + query, Language: "multi", Note: "Купить"},
		{Name: "ROMSPedia", URL: "https://www.romspedia.com/search?q=" + query, Language: "EN", Note: "ROM"},
		{Name: "RomsFun", URL: "https://romsfun.com/?s=" + query, Language: "EN", Note: "ROM"},
	}
}

func splitLine(line string) (string, string, bool) {
	if !strings.HasSuffix(line, ")") {
		return "", "", false
	}

	index := strings.LastIndex(line, " (")
	if index <= 0 {
		return "", "", false
	}

	title := strings.TrimSpace(line[:index])
	label := strings.TrimSpace(line[index+2 : len(line)-1])

	if title == "" || label == "" {
		return "", "", false
	}

	return title, label, true
}

func normalizeTitleKey(value string) string {
	return strings.Join(titleTokens(value), " ")
}

func slugify(value string) string {
	var builder strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastDash = false
		case !lastDash:
			builder.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func shortID(value string) string {
	sum := sha1.Sum([]byte(value))

	return hex.EncodeToString(sum[:8])
}

var yearPattern = regexp.MustCompile(`\b(19[5-9]\d|20[0-2]\d)\b`)

func extractYear(title string) int {
	value := yearPattern.FindString(title)
	if value == "" {
		return 0
	}

	year, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}

	return year
}

func ageFor(year int, platform Platform) string {
	if year > 0 {
		switch {
		case year <= 1994:
			return "classic"
		case year <= 2005:
			return "retro"
		case year <= 2015:
			return "modern"
		default:
			return "new"
		}
	}

	switch platform.Era {
	case "classic":
		return "classic"
	case "retro":
		return "retro"
	case "modern":
		return "modern"
	default:
		return "retro"
	}
}

var iconicTiers = map[string]string{
	"super mario bros":                    "platinum",
	"the legend of zelda":                 "platinum",
	"the legend of zelda ocarina of time": "platinum",
	"chrono trigger":                      "platinum",
	"final fantasy vii":                   "platinum",
	"metal gear solid":                    "platinum",
	"resident evil":                       "platinum",
	"silent hill":                         "platinum",
	"castlevania symphony of the night":   "platinum",
	"super metroid":                       "platinum",
	"half-life":                           "platinum",
	"doom":                                "platinum",
	"sonic the hedgehog":                  "diamond",
	"mortal kombat":                       "diamond",
	"street fighter ii":                   "diamond",
	"tekken 3":                            "diamond",
	"gran turismo":                        "diamond",
	"crash bandicoot":                     "diamond",
	"spyro the dragon":                    "diamond",
	"pokemon red":                         "diamond",
	"pokemon blue":                        "diamond",
}

func tierFor(title string, platform Platform, year int) string {
	key := strings.ToLower(strings.TrimSpace(title))
	if tier, ok := iconicTiers[key]; ok {
		return tier
	}

	for name, tier := range iconicTiers {
		if strings.Contains(key, name) {
			return tier
		}
	}

	hash := sha1.Sum([]byte(key + platform.Code))
	roll := int(hash[0]) % 100
	rare := platform.Rare
	retro := year > 0 && year <= 1995

	switch {
	case rare && roll < 15:
		return "platinum"
	case (rare || retro) && roll < 25:
		return "diamond"
	case (retro || platform.Era == "classic") && roll < 35:
		return "gold"
	default:
		return "none"
	}
}

func (s *Service) listMemory(filters Filters) ListResponse {
	s.mu.RLock()
	games := s.games
	s.mu.RUnlock()

	filtered := filterGamesWith(games, filters, s.enricher.searchExtras)

	if filters.Verified != nil {
		filtered = s.enricher.FilterVerified(filtered, *filters.Verified)
	}

	total := len(filtered)

	page := filters.Page
	if page < 1 {
		page = 1
	}

	pageSize := filters.PageSize
	if pageSize < 1 {
		pageSize = 24
	}

	if pageSize > 100 {
		pageSize = 100
	}

	sortGames(filtered, filters.Sort, filters.Order)

	start := (page - 1) * pageSize
	if start > total {
		start = total
	}

	end := start + pageSize
	if end > total {
		end = total
	}

	items := s.enricher.EnrichMany(context.Background(), filtered[start:end], false, false)
	s.enricher.Start(filtered[start:end])

	return ListResponse{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Facets:   facetsFor(filtered),
	}
}

func (s *Service) searchMemory(query string, limit int) []Game {
	if limit < 1 {
		limit = 8
	}

	if limit > 50 {
		limit = 50
	}

	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return []Game{}
	}

	s.mu.RLock()
	games := s.games
	s.mu.RUnlock()

	result := make([]Game, 0, limit)
	seen := make(map[string]bool, limit)

	for _, game := range games {
		if game.Status != "seed" {
			continue
		}

		if matchesGameQuery(game, query) {
			result = append(result, game)
			seen[game.Slug] = true

			if len(result) >= limit {
				break
			}
		}
	}

	if len(result) < limit {
		type scored struct {
			game  Game
			score float64
		}

		candidates := make([]scored, 0, limit*2)

		for _, game := range games {
			if game.Status != "seed" || seen[game.Slug] {
				continue
			}

			score := fuzzyTitleScore(query, game.Title)
			if score < 0.7 {
				continue
			}

			candidates = append(candidates, scored{game, score})
		}

		sort.SliceStable(candidates, func(i, j int) bool {
			return candidates[i].score > candidates[j].score
		})

		for _, candidate := range candidates {
			result = append(result, candidate.game)

			if len(result) >= limit {
				break
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return s.enricher.EnrichMany(ctx, result, false, false)
}

var genreTranslations = map[string]string{
	"action": "Экшен", "adventure": "Приключение", "role-playing (rpg)": "Ролевая игра", "role-playing": "Ролевая игра",
	"rpg": "Ролевая игра", "strategy": "Стратегия", "real time strategy (rts)": "Стратегия в реальном времени",
	"turn-based strategy (tbs)": "Пошаговая стратегия", "tactical": "Тактика", "shooter": "Шутер",
	"fighting": "Файтинг", "platform": "Платформер", "puzzle": "Головоломка", "racing": "Гонки",
	"sport": "Спорт", "simulator": "Симулятор", "indie": "Инди", "arcade": "Аркада",
	"visual novel": "Визуальная новелла", "music": "Музыка", "point-and-click": "Квест",
	"quiz/trivia": "Викторина", "card & board game": "Карточные и настольные", "moba": "MOBA",
	"hack and slash/beat 'em up": "Слэшер", "mmo": "MMO", "educational": "Обучающая",
	"horror": "Хоррор", "stealth": "Стелс", "survival": "Выживание", "open world": "Открытый мир",
	"sandbox": "Песочница", "comedy": "Комедия", "science fiction": "Научная фантастика",
	"fantasy": "Фэнтези", "puzzle-platform": "Платформер-головоломка",
}

func translateGenres(genres []string) []string {
	result := make([]string, 0, len(genres))

	for _, genre := range genres {
		key := strings.ToLower(strings.TrimSpace(genre))

		if translated, ok := genreTranslations[key]; ok {
			result = appendUnique(result, translated)
		} else {
			result = appendUnique(result, genre)
		}
	}

	return result
}

func matchesGameQueryCached(game Game, query string, extrasFor func(string) []string) bool {
	if matchesGameQuery(game, query) {
		return true
	}

	if extrasFor == nil {
		return false
	}

	for _, value := range extrasFor(game.Slug) {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}

	return false
}

func matchesGameQuery(game Game, query string) bool {
	if matchesTitleQuery(game.Title, query) {
		return true
	}

	if game.TitleRu != "" && matchesTitleQuery(game.TitleRu, query) {
		return true
	}

	for _, tag := range game.SearchTags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}

	if containsCyrillic(query) {
		if latin := transliterateRu(query); latin != query && matchesTitleQuery(game.Title, latin) {
			return true
		}
	}

	for _, genre := range game.Genres {
		if strings.Contains(strings.ToLower(genre), query) {
			return true
		}
	}

	for _, genre := range game.GenresRU {
		if strings.Contains(strings.ToLower(genre), query) {
			return true
		}
	}

	return false
}

func matchesTitleQuery(title string, query string) bool {
	if strings.Contains(strings.ToLower(title), query) {
		return true
	}

	if len([]rune(query)) < 4 {
		return false
	}

	return fuzzyTitleScore(query, title) >= 0.75
}

func containsCyrillic(value string) bool {
	for _, r := range value {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' {
			return true
		}
	}

	return false
}

var transliteration = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func transliterateRu(value string) string {
	var builder strings.Builder

	for _, r := range strings.ToLower(value) {
		if replacement, ok := transliteration[r]; ok {
			builder.WriteString(replacement)
		} else {
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

func fuzzyTitleScore(query string, title string) float64 {
	queryTokens := titleTokens(query)
	titleTokensList := titleTokens(title)

	if len(queryTokens) == 0 || len(titleTokensList) == 0 {
		return 0
	}

	matched := 0

	for _, queryToken := range queryTokens {
		best := 0.0

		for _, titleToken := range titleTokensList {
			if score := tokenSimilarity(queryToken, titleToken); score > best {
				best = score
			}
		}

		if best >= 0.8 {
			matched++
		}
	}

	return float64(matched) / float64(len(queryTokens))
}

func tokenSimilarity(left string, right string) float64 {
	if left == right {
		return 1
	}

	if len(left) >= 3 && len(right) >= 3 {
		if strings.HasPrefix(left, right) || strings.HasPrefix(right, left) {
			return 0.9
		}

		if sortedRunes(left) == sortedRunes(right) {
			return 0.95
		}
	}

	distance := damerauLevenshtein(left, right)
	maximum := max(len(left), len(right))

	if maximum == 0 {
		return 1
	}

	return 1 - float64(distance)/float64(maximum)
}

func sortedRunes(value string) string {
	runes := []rune(value)
	sort.Slice(runes, func(i, j int) bool {
		return runes[i] < runes[j]
	})

	return string(runes)
}

func damerauLevenshtein(left string, right string) int {
	a := []rune(left)
	b := []rune(right)
	rows := len(a) + 1
	cols := len(b) + 1
	matrix := make([][]int, rows)

	for i := range matrix {
		matrix[i] = make([]int, cols)
		matrix[i][0] = i
	}

	for j := 0; j < cols; j++ {
		matrix[0][j] = j
	}

	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			cost := 0

			if a[i-1] != b[j-1] {
				cost = 1
			}

			matrix[i][j] = min(
				matrix[i-1][j]+1,
				matrix[i][j-1]+1,
				matrix[i-1][j-1]+cost,
			)

			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				matrix[i][j] = min(matrix[i][j], matrix[i-2][j-2]+1)
			}
		}
	}

	return matrix[len(a)][len(b)]
}

func (s *Service) getMemory(slug string) (Game, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	index, ok := s.bySlug[slug]
	if !ok {
		return Game{}, false
	}

	game := s.games[index]

	for _, edition := range s.relatedEditionsMemory(game) {
		game.Editions = appendUnique(game.Editions, edition)
	}

	return game, true
}

var editionWords = map[string]bool{
	"edition": true, "game": true, "year": true, "goty": true, "legendary": true,
	"special": true, "anniversary": true, "vr": true, "remastered": true,
	"remaster": true, "complete": true, "deluxe": true, "definitive": true,
	"enhanced": true, "director": true, "cut": true, "collector": true,
	"collectors": true, "ultimate": true, "gold": true, "premium": true,
	"standard": true, "bundle": true, "pack": true, "the": true, "of": true,
	"and": true, "a": true, "an": true, "digital": true, "content": true,
}

func editionKey(title string) string {
	tokens := titleTokens(title)
	base := make([]string, 0, len(tokens))

	for _, token := range tokens {
		if editionWords[token] {
			continue
		}

		base = append(base, token)
	}

	return strings.Join(base, " ")
}

func (s *Service) relatedEditionsMemory(game Game) []string {
	key := editionKey(game.Title)

	if len(key) < 6 || len(strings.Fields(key)) < 2 {
		return nil
	}

	result := make([]string, 0, 8)
	seen := map[string]bool{strings.ToLower(game.Title): true}

	for _, candidate := range s.games {
		if candidate.Status != "seed" && candidate.Status != "approved" {
			continue
		}

		if editionKey(candidate.Title) != key {
			continue
		}

		if len(titleTokens(candidate.Title)) <= len(titleTokens(game.Title)) {
			continue
		}

		lower := strings.ToLower(candidate.Title)
		if seen[lower] {
			continue
		}

		seen[lower] = true
		result = append(result, candidate.Title)

		if len(result) >= 10 {
			break
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})

	return result
}

func (s *Service) poolMemory(filters Filters) []Game {
	s.mu.RLock()
	games := s.games
	s.mu.RUnlock()

	return filterGames(games, filters)
}

func (s *Service) Platforms() []Platform {
	list := PlatformList()
	sort.Slice(list, func(i, j int) bool {
		if list[i].Maintainer == list[j].Maintainer {
			return list[i].Name < list[j].Name
		}

		return list[i].Maintainer < list[j].Maintainer
	})

	return list
}

func (s *Service) Updated() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.updated
}

func filterGames(games []Game, filters Filters) []Game {
	return filterGamesWith(games, filters, nil)
}

func filterGamesWith(games []Game, filters Filters, extrasFor func(string) []string) []Game {
	query := strings.ToLower(strings.TrimSpace(filters.Query))
	result := make([]Game, 0)

	for _, game := range games {
		if query != "" && !matchesGameQueryCached(game, query, extrasFor) {
			continue
		}

		if filters.Platform != "" && !hasPlatform(game, filters.Platform) {
			continue
		}

		if filters.Tier != "" && game.Tier != filters.Tier {
			continue
		}

		if filters.Age != "" && game.Age != filters.Age {
			continue
		}

		if filters.Emulator != nil && game.EmulatorsRequired != *filters.Emulator {
			continue
		}

		if filters.YearFrom > 0 && (game.Year == 0 || game.Year < filters.YearFrom) {
			continue
		}

		if filters.YearTo > 0 && (game.Year == 0 || game.Year > filters.YearTo) {
			continue
		}

		status := filters.Status
		if status == "" {
			status = "seed"
		}

		if game.Status != status {
			continue
		}

		result = append(result, game)
	}

	return result
}

func hasPlatform(game Game, code string) bool {
	for _, platform := range game.Platforms {
		if platform.Code == code {
			return true
		}
	}

	return false
}

func sortGames(games []Game, field string, order string) {
	if strings.TrimSpace(field) == "" && strings.TrimSpace(order) == "" {
		return
	}

	descending := strings.EqualFold(order, "desc")

	sort.SliceStable(games, func(i, j int) bool {
		left, right := games[i], games[j]
		less := false

		switch field {
		case "year":
			less = left.Year < right.Year
		case "tier":
			less = tierRank(left.Tier) < tierRank(right.Tier)
		case "rating":
			less = left.Rating < right.Rating
		case "updated":
			less = left.UpdatedAt < right.UpdatedAt
		default:
			less = strings.ToLower(left.Title) < strings.ToLower(right.Title)
		}

		if descending {
			return !less
		}

		return less
	})
}

func tierRank(tier string) int {
	switch tier {
	case "platinum":
		return 3
	case "diamond":
		return 2
	case "gold":
		return 1
	default:
		return 0
	}
}

func facetsFor(games []Game) Facets {
	platformCounts := make(map[string]int)
	tierCounts := make(map[string]int)
	ageCounts := make(map[string]int)
	platformNames := make(map[string]string)

	for _, game := range games {
		tierCounts[game.Tier]++
		ageCounts[game.Age]++

		for _, platform := range game.Platforms {
			platformCounts[platform.Code]++
			platformNames[platform.Code] = platform.Name
		}
	}

	platformsFacet := make([]PlatformFacet, 0, len(platformCounts))

	for code, count := range platformCounts {
		platformsFacet = append(platformsFacet, PlatformFacet{
			Code:  code,
			Name:  platformNames[code],
			Count: count,
		})
	}

	sort.Slice(platformsFacet, func(i, j int) bool {
		return platformsFacet[i].Count > platformsFacet[j].Count
	})

	tiers := []FacetValue{
		{Value: "platinum", Count: tierCounts["platinum"]},
		{Value: "diamond", Count: tierCounts["diamond"]},
		{Value: "gold", Count: tierCounts["gold"]},
		{Value: "none", Count: tierCounts["none"]},
	}

	ages := []FacetValue{
		{Value: "classic", Count: ageCounts["classic"]},
		{Value: "retro", Count: ageCounts["retro"]},
		{Value: "modern", Count: ageCounts["modern"]},
		{Value: "new", Count: ageCounts["new"]},
	}

	return Facets{Platforms: platformsFacet, Tiers: tiers, Ages: ages}
}

func randomIndex(total int) int {
	if total <= 0 {
		return -1
	}

	value, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return 0
	}

	return int(value.Int64())
}

func (s *Service) loadCache() {
	if s.db == nil {
		return
	}

	var raw []byte

	err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		cacheKey,
	).Scan(&raw)
	if err != nil {
		return
	}

	var payload cachePayload

	if err := json.Unmarshal(raw, &payload); err != nil || payload.Version != cacheVersion || len(payload.Games) == 0 {
		return
	}

	bySlug := make(map[string]int, len(payload.Games))

	for index, game := range payload.Games {
		bySlug[game.Slug] = index
	}

	index := make(map[string]bool, len(payload.Games))

	for _, game := range payload.Games {
		if game.Sources != nil && len(game.Sources) > 0 && game.Sources[0].ID == "rgg-cat" {
			index[normalizeTitleKey(game.Title)] = true
		}
	}

	s.mu.Lock()
	s.games = payload.Games
	s.bySlug = bySlug
	s.rggTitles = index
	s.hash = payload.Hash
	s.updated = payload.Updated
	s.mu.Unlock()

	log.Printf("[catalog] restored %d games from cache", len(payload.Games))
}

func (s *Service) enrichLoop() {
	if strings.TrimSpace(os.Getenv("ENRICH_LOOP")) == "0" {
		return
	}

	interval := 5 * time.Second

	if value := strings.TrimSpace(os.Getenv("ENRICH_LOOP_INTERVAL")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			interval = parsed
		}
	}

	batch := 20

	if value := strings.TrimSpace(os.Getenv("ENRICH_LOOP_BATCH")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			batch = parsed
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		s.enrichBatch(batch)
	}
}

func (s *Service) enrichBatch(batch int) {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT payload FROM catalog_game c
		 WHERE NOT EXISTS (SELECT 1 FROM game_enrichment e WHERE e.slug = c.slug)
		 ORDER BY random() LIMIT $1`,
		batch,
	)
	if err != nil {
		return
	}

	candidates := make([]Game, 0, batch)

	for rows.Next() {
		var raw []byte

		if err := rows.Scan(&raw); err != nil {
			continue
		}

		var game Game

		if json.Unmarshal(raw, &game) == nil {
			candidates = append(candidates, game)
		}
	}

	_ = rows.Close()

	if len(candidates) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	enriched := s.enricher.EnrichMany(ctx, candidates, true, false)
	count := 0

	for _, game := range enriched {
		if isServiceGame(game) || game.Cover != "" {
			count++
		}
	}

	if count > 0 {
		log.Printf("[enrich-loop] %d/%d games enriched", count, len(candidates))
	}
}

func (s *Service) loadImported() {
	if s.db == nil || strings.TrimSpace(os.Getenv("CATALOG_LOAD_IMPORTED")) == "0" {
		return
	}

	query := `SELECT payload FROM catalog_game`
	args := make([]any, 0, 1)

	if value := strings.TrimSpace(os.Getenv("CATALOG_IMPORTED_LIMIT")); value != "" {
		if limit, err := strconv.Atoi(value); err == nil && limit > 0 {
			query = `SELECT payload FROM catalog_game
				ORDER BY COALESCE((payload->>'rating')::float, 0) DESC
				LIMIT $1`
			args = append(args, limit)
		}
	}

	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return
	}

	defer func() {
		_ = rows.Close()
	}()

	loaded := 0

	s.mu.Lock()

	for rows.Next() {
		var raw []byte

		if err := rows.Scan(&raw); err != nil {
			continue
		}

		var game Game

		if json.Unmarshal(raw, &game) != nil {
			continue
		}

		if _, exists := s.bySlug[game.Slug]; exists {
			continue
		}

		s.bySlug[game.Slug] = len(s.games)
		s.games = append(s.games, game)
		loaded++
	}

	s.mu.Unlock()

	if loaded > 0 {
		log.Printf("[catalog] restored %d imported games from database", loaded)
	}
}

func (s *Service) persist(games []Game, hash string, updated time.Time) {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(cachePayload{
		Version: cacheVersion,
		Hash:    hash,
		Updated: updated,
		Games:   games,
	})
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		cacheKey,
		string(raw),
	)
}
