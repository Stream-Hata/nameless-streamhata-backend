// Package lots — метаданные лотов для колеса: игры, фильмы, музыка.
package lots

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

type Completion struct {
	Hastily    float64 `json:"hastilyHours,omitempty"`
	Normally   float64 `json:"normallyHours,omitempty"`
	Completely float64 `json:"completelyHours,omitempty"`
}

type Meta struct {
	Type             string      `json:"type"`
	Title            string      `json:"title"`
	Subtitle         string      `json:"subtitle"`
	Year             int         `json:"year"`
	Genre            string      `json:"genre"`
	Description      string      `json:"description"`
	Image            string      `json:"image"`
	URL              string      `json:"url"`
	Source           string      `json:"source"`
	DLC              []string    `json:"dlc,omitempty"`
	Editions         []string    `json:"editions,omitempty"`
	Mods             []string    `json:"mods,omitempty"`
	ModsURL          string      `json:"modsUrl,omitempty"`
	Platforms        []string    `json:"platforms,omitempty"`
	Rating           float64     `json:"rating,omitempty"`
	AggregatedRating float64     `json:"aggregatedRating,omitempty"`
	Completion       *Completion `json:"completion,omitempty"`
	OriginalTitle    string      `json:"originalTitle,omitempty"`
	Developers       []string    `json:"developers,omitempty"`
	Publishers       []string    `json:"publishers,omitempty"`
	Themes           []string    `json:"themes,omitempty"`
	GameModes        []string    `json:"gameModes,omitempty"`
	Perspectives     []string    `json:"perspectives,omitempty"`
	ReleaseDate      string      `json:"releaseDate,omitempty"`
	Website          string      `json:"website,omitempty"`
	Storyline        string      `json:"storyline,omitempty"`
	RuntimeMinutes   int         `json:"runtimeMinutes,omitempty"`
	Artist           string      `json:"artist,omitempty"`
	Album            string      `json:"album,omitempty"`
	DurationSeconds  int         `json:"durationSeconds,omitempty"`
}

var (
	nonWord      = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	editionRegex = regexp.MustCompile(`\b([A-Z][A-Za-z0-9'’\-]+(?:\s+[A-Z][A-Za-z0-9'’\-]+){0,4}\s+Edition)\b`)
)

var romanTokens = map[string]string{
	"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6",
	"vii": "7", "viii": "8", "ix": "9", "x": "10", "xi": "11",
	"xii": "12", "xiii": "13", "xiv": "14", "xv": "15",
}

func normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonWord.ReplaceAllString(value, " ")
	fields := strings.Fields(value)

	for index, field := range fields {
		if digit, ok := romanTokens[field]; ok {
			fields[index] = digit
		}
	}

	return strings.Join(fields, " ")
}

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/lots/meta", func(response http.ResponseWriter, request *http.Request) {
		name := strings.TrimSpace(request.URL.Query().Get("name"))
		kind := strings.TrimSpace(request.URL.Query().Get("type"))

		if name == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "name is required"})

			return
		}

		lang := request.URL.Query().Get("lang")
		if lang != "en" {
			lang = "ru"
		}

		meta, err := Lookup(request.Context(), name, kind, lang)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, meta)
	})
}

type cacheEntry struct {
	meta    Meta
	expires time.Time
}

var metaCache = struct {
	sync.RWMutex
	items map[string]cacheEntry
}{items: make(map[string]cacheEntry)}

func Lookup(ctx context.Context, name, kind, lang string) (*Meta, error) {
	key := lang + "|" + kind + "|" + normalize(name)

	metaCache.RLock()
	entry, found := metaCache.items[key]
	metaCache.RUnlock()

	if found && time.Now().Before(entry.expires) {
		cached := entry.meta

		return &cached, nil
	}

	meta, err := lookup(ctx, name, kind, lang)
	if err != nil {
		return nil, err
	}

	localizeMeta(ctx, meta, lang)

	metaCache.Lock()
	metaCache.items[key] = cacheEntry{meta: *meta, expires: time.Now().Add(time.Hour)}
	metaCache.Unlock()

	return meta, nil
}

func needsTranslation(value, lang string) bool {
	if value == "" {
		return false
	}

	cyrillic := hasCyrillic(value)

	if lang == "ru" {
		return !cyrillic
	}

	return cyrillic
}

func anyNeedsTranslation(values []string, lang string) bool {
	for _, value := range values {
		if needsTranslation(value, lang) {
			return true
		}
	}

	return false
}

var translationCache = struct {
	sync.Mutex
	items map[string]string
}{items: make(map[string]string)}

func translateGoogle(ctx context.Context, text, lang string) string {
	if text == "" {
		return text
	}

	key := lang + "|" + text
	translationCache.Lock()
	if cached, ok := translationCache.items[key]; ok {
		translationCache.Unlock()

		return cached
	}
	translationCache.Unlock()

	endpoint := "https://clients5.google.com/translate_a/single?client=dict-chrome-ex&sl=auto&tl=" +
		url.QueryEscape(lang) + "&dt=t&q=" + url.QueryEscape(text)

	body, err := get(ctx, endpoint)
	if err != nil {
		return text
	}

	var payload []any
	if err := json.Unmarshal(body, &payload); err != nil || len(payload) == 0 {
		return text
	}

	segments, ok := payload[0].([]any)
	if !ok {
		return text
	}

	var result strings.Builder

	for _, item := range segments {
		parts, ok := item.([]any)
		if !ok || len(parts) == 0 {
			continue
		}

		if value, ok := parts[0].(string); ok {
			result.WriteString(value)
		}
	}

	translated := strings.TrimSpace(result.String())
	if translated == "" {
		return text
	}

	translationCache.Lock()
	translationCache.items[key] = translated
	translationCache.Unlock()

	return translated
}

func localizeList(ctx context.Context, values []string, lang string) []string {
	result := make([]string, 0, len(values))

	for _, value := range values {
		if needsTranslation(value, lang) {
			result = append(result, translateGoogle(ctx, value, lang))
		} else {
			result = append(result, value)
		}
	}

	return result
}

func localizeMeta(ctx context.Context, meta *Meta, lang string) {
	if lang != "ru" && lang != "en" {
		return
	}

	if meta.Description != "" && needsTranslation(meta.Description, lang) {
		meta.Description = translateGoogle(ctx, meta.Description, lang)
	}

	if meta.Storyline != "" && needsTranslation(meta.Storyline, lang) {
		meta.Storyline = translateGoogle(ctx, meta.Storyline, lang)
	}

	if meta.Genre != "" && needsTranslation(meta.Genre, lang) {
		if lang == "ru" {
			meta.Genre = localizeGenre(meta.Genre, lang)
		}

		if needsTranslation(meta.Genre, lang) {
			meta.Genre = translateGoogle(ctx, meta.Genre, lang)
		}
	}

	meta.Themes = localizeList(ctx, meta.Themes, lang)
	meta.GameModes = localizeList(ctx, meta.GameModes, lang)
	meta.Perspectives = localizeList(ctx, meta.Perspectives, lang)
	meta.Platforms = localizeList(ctx, meta.Platforms, lang)
	meta.DLC = localizeList(ctx, meta.DLC, lang)
	meta.Editions = localizeList(ctx, meta.Editions, lang)
}

func lookup(ctx context.Context, name, kind, lang string) (*Meta, error) {
	switch kind {
	case "game":
		if meta, err := lookupWikipediaTyped(ctx, name, "game", lang); err == nil {
			enrichIGDB(ctx, meta, name, lang)

			return meta, nil
		}

		if english := translateViaWikipedia(ctx, name); english != "" {
			if meta, err := lookupIGDB(ctx, english, lang); err == nil {
				return meta, nil
			}

			if meta, err := lookupSteam(ctx, english, lang); err == nil {
				return meta, nil
			}
		}

		if meta, err := lookupIGDB(ctx, name, lang); err == nil {
			return meta, nil
		}

		if meta, err := lookupSteam(ctx, name, lang); err == nil {
			return meta, nil
		}

		return lookupWikipedia(ctx, name, lang)
	case "movie":
		if meta, err := lookupWikipediaTyped(ctx, name, "movie", lang); err == nil {
			return meta, nil
		}

		return lookupITunes(ctx, name, "movie", "Фильм", lang)
	case "music":
		if meta, err := lookupDeezer(ctx, name); err == nil {
			return meta, nil
		}

		if meta, err := lookupWikipedia(ctx, name, lang); err == nil && meta.Type == "music" {
			return meta, nil
		}

		return lookupITunes(ctx, name, "song", "Музыка", lang)
	default:
		type candidate struct {
			meta     *Meta
			score    int
			priority int
		}

		results := make(chan candidate, 3)

		queries := []struct {
			query    func(context.Context, string) (*Meta, error)
			priority int
		}{
			{func(ctx context.Context, value string) (*Meta, error) {
				return lookupWikipedia(ctx, value, lang)
			}, 5},
			{func(ctx context.Context, value string) (*Meta, error) {
				return lookupSteam(ctx, value, lang)
			}, 4},
			{func(ctx context.Context, value string) (*Meta, error) {
				return lookupIGDB(ctx, value, lang)
			}, 4},
			{lookupDeezer, 3},
			{func(ctx context.Context, value string) (*Meta, error) {
				return lookupITunes(ctx, value, "movie", "Фильм", lang)
			}, 2},
			{func(ctx context.Context, value string) (*Meta, error) {
				return lookupITunes(ctx, value, "song", "Музыка", lang)
			}, 1},
		}

		for _, item := range queries {
			go func(query func(context.Context, string) (*Meta, error), priority int) {
				meta, err := query(ctx, name)
				if err != nil {
					results <- candidate{}

					return
				}

				results <- candidate{meta: meta, score: matchScore(name, meta.Title), priority: priority}
			}(item.query, item.priority)
		}

		best := candidate{}
		preferred := candidate{}
		gamePreferred := candidate{}

		for range queries {
			current := <-results
			if current.meta == nil {
				continue
			}

			if current.meta.Source == "wikipedia" &&
				(current.meta.Type == "game" || current.meta.Type == "movie") &&
				current.score >= 2 &&
				(current.score > preferred.score ||
					(current.score == preferred.score && current.priority > preferred.priority)) {
				preferred = current
			}

			if current.meta.Source == "igdb" && current.meta.Type == "game" && current.score == 2 &&
				(current.score > gamePreferred.score ||
					(current.score == gamePreferred.score && current.priority > gamePreferred.priority)) {
				gamePreferred = current
			}

			if current.score > best.score || (current.score == best.score && current.priority > best.priority) {
				best = current
			}
		}

		if preferred.meta != nil {
			if preferred.meta.Type == "game" {
				enrichIGDB(ctx, preferred.meta, name, lang)
			}

			return preferred.meta, nil
		}

		if gamePreferred.meta != nil {
			return gamePreferred.meta, nil
		}

		if best.meta == nil {
			return nil, fmt.Errorf("лот не найден")
		}

		if best.meta.Source == "wikipedia" && best.meta.Type == "other" {
			if typed, err := lookupWikipediaTyped(ctx, name, "", lang); err == nil {
				return typed, nil
			}
		}

		if best.meta.Type == "game" {
			enrichIGDB(ctx, best.meta, name, lang)
		}

		return best.meta, nil
	}
}

type igdbGame struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Summary          string  `json:"summary"`
	FirstReleaseDate int64   `json:"first_release_date"`
	Rating           float64 `json:"rating"`
	AggregatedRating float64 `json:"aggregated_rating"`
	Storyline        string  `json:"storyline"`
	Genres           []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Themes []struct {
		Name string `json:"name"`
	} `json:"themes"`
	GameModes []struct {
		Name string `json:"name"`
	} `json:"game_modes"`
	Perspectives []struct {
		Name string `json:"name"`
	} `json:"player_perspectives"`
	Platforms []struct {
		Name string `json:"name"`
	} `json:"platforms"`
	Cover struct {
		URL string `json:"url"`
	} `json:"cover"`
	DLCs []struct {
		Name string `json:"name"`
	} `json:"dlcs"`
	Expansions []struct {
		Name string `json:"name"`
	} `json:"expansions"`
	Companies []struct {
		Developer bool `json:"developer"`
		Publisher bool `json:"publisher"`
		Company   struct {
			Name string `json:"name"`
		} `json:"company"`
	} `json:"involved_companies"`
	Websites []struct {
		URL      string `json:"url"`
		Category int    `json:"category"`
	} `json:"websites"`
}

var igdbAuth = struct {
	sync.Mutex
	clientID string
	token    string
	expires  time.Time
}{}

func igdbCreds() (string, string) {
	clientID := os.Getenv("TWITCH_CLIENT_KEY")
	if clientID == "" {
		clientID = os.Getenv("TWITCH_CLIENT_ID")
	}

	secret := os.Getenv("TWITCH_SECRET_KEY")
	if secret == "" {
		secret = os.Getenv("TWITCH_CLIENT_SECRET")
	}

	return clientID, secret
}

func igdbToken(ctx context.Context) (string, string, error) {
	clientID, secret := igdbCreds()
	if clientID == "" || secret == "" {
		return "", "", fmt.Errorf("igdb credentials are not configured")
	}

	igdbAuth.Lock()
	defer igdbAuth.Unlock()

	if igdbAuth.token != "" && igdbAuth.clientID == clientID && time.Now().Before(igdbAuth.expires) {
		return igdbAuth.clientID, igdbAuth.token, nil
	}

	tokenURL := "https://id.twitch.tv/oauth2/token?client_id=" + url.QueryEscape(clientID) +
		"&client_secret=" + url.QueryEscape(secret) + "&grant_type=client_credentials"

	body, err := post(ctx, tokenURL)
	if err != nil {
		return "", "", err
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.AccessToken == "" {
		return "", "", fmt.Errorf("igdb token is unavailable")
	}

	ttl := time.Duration(payload.ExpiresIn) * time.Second
	if ttl > time.Hour {
		ttl -= time.Hour
	}

	igdbAuth.clientID = clientID
	igdbAuth.token = payload.AccessToken
	igdbAuth.expires = time.Now().Add(ttl)

	return clientID, payload.AccessToken, nil
}

func igdbQuery(ctx context.Context, endpoint, query string) ([]byte, error) {
	clientID, token, err := igdbToken(ctx)
	if err != nil {
		return nil, err
	}

	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.igdb.com/v4/"+endpoint, bytes.NewBufferString(query))
		if err != nil {
			return nil, err
		}

		request.Header.Set("Client-ID", clientID)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "text/plain")
		request.Header.Set("Accept", "application/json")

		response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
		if err != nil {
			return nil, err
		}

		if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) && attempt < 2 {
			_ = response.Body.Close()

			select {
			case <-time.After(time.Duration(attempt+1) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()

			return nil, fmt.Errorf("igdb вернул %d", response.StatusCode)
		}

		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		_ = response.Body.Close()

		return body, readErr
	}
}

func lookupIGDB(ctx context.Context, name, lang string) (*Meta, error) {
	search := strings.ReplaceAll(name, `"`, "")
	search = strings.ReplaceAll(search, `\`, "")
	search = strings.ReplaceAll(search, "\n", " ")

	body, err := igdbQuery(ctx, "games",
		fmt.Sprintf(`search "%s"; fields name,summary,storyline,first_release_date,rating,aggregated_rating,genres.name,themes.name,game_modes.name,player_perspectives.name,platforms.name,cover.url,dlcs.name,expansions.name,involved_companies.developer,involved_companies.publisher,involved_companies.company.name,websites.url,websites.category; limit 20;`, search))
	if err != nil {
		return nil, err
	}

	var games []igdbGame
	if err := json.Unmarshal(body, &games); err != nil || len(games) == 0 {
		return nil, fmt.Errorf("игра не найдена")
	}

	best := games[0]
	bestScore := 0

	for _, game := range games {
		if score := matchScore(name, game.Name); score > bestScore {
			best = game
			bestScore = score
		}
	}

	if bestScore == 0 {
		return nil, fmt.Errorf("игра не найдена")
	}

	genres := make([]string, 0, len(best.Genres))
	for _, genre := range best.Genres {
		if genre.Name != "" {
			genres = append(genres, genre.Name)
		}
	}

	platforms := make([]string, 0, len(best.Platforms))
	for _, platform := range best.Platforms {
		if platform.Name != "" {
			platforms = append(platforms, platform.Name)
		}
	}

	themes := make([]string, 0, len(best.Themes))
	for _, item := range best.Themes {
		if item.Name != "" {
			themes = append(themes, item.Name)
		}
	}

	modes := make([]string, 0, len(best.GameModes))
	for _, item := range best.GameModes {
		if item.Name != "" {
			modes = append(modes, item.Name)
		}
	}

	perspectives := make([]string, 0, len(best.Perspectives))
	for _, item := range best.Perspectives {
		if item.Name != "" {
			perspectives = append(perspectives, item.Name)
		}
	}

	developers := make([]string, 0)
	publishers := make([]string, 0)

	for _, item := range best.Companies {
		if item.Company.Name == "" {
			continue
		}

		if item.Developer {
			developers = append(developers, item.Company.Name)
		}

		if item.Publisher {
			publishers = append(publishers, item.Company.Name)
		}
	}

	website := ""
	for _, item := range best.Websites {
		if item.URL != "" && (website == "" || item.Category == 1) {
			website = item.URL
		}
	}

	dlc := make([]string, 0, len(best.DLCs)+len(best.Expansions))
	seenDLC := make(map[string]bool)

	for _, item := range best.DLCs {
		if item.Name != "" && !seenDLC[item.Name] {
			dlc = append(dlc, item.Name)
			seenDLC[item.Name] = true
		}
	}

	for _, item := range best.Expansions {
		if item.Name != "" && !seenDLC[item.Name] {
			dlc = append(dlc, item.Name)
			seenDLC[item.Name] = true
		}
	}

	editions := make([]string, 0)
	seenEditions := make(map[string]bool)

	for _, game := range games {
		if game.ID == best.ID || game.Name == "" || seenEditions[game.Name] {
			continue
		}

		if isEdition(best.Name, game.Name) {
			editions = append(editions, game.Name)
			seenEditions[game.Name] = true
		}
	}

	year := 0
	if best.FirstReleaseDate > 0 {
		year = time.Unix(best.FirstReleaseDate, 0).UTC().Year()
	}

	image := best.Cover.URL
	if strings.HasPrefix(image, "//") {
		image = "https:" + image
	}

	image = strings.ReplaceAll(image, "/t_thumb/", "/t_cover_big/")

	releaseDate := ""
	if best.FirstReleaseDate > 0 {
		releaseDate = time.Unix(best.FirstReleaseDate, 0).UTC().Format("2006-01-02")
	}

	meta := &Meta{
		Type:             "game",
		Title:            best.Name,
		Subtitle:         "IGDB",
		Year:             year,
		Genre:            localizeGenre(strings.Join(genres, ", "), lang),
		Description:      best.Summary,
		Image:            image,
		Source:           "igdb",
		DLC:              dlc,
		Editions:         editions,
		Platforms:        platforms,
		Rating:           best.Rating,
		AggregatedRating: best.AggregatedRating,
		Developers:       developers,
		Publishers:       publishers,
		Themes:           themes,
		GameModes:        modes,
		Perspectives:     perspectives,
		ReleaseDate:      releaseDate,
		Website:          website,
		Storyline:        best.Storyline,
	}

	if completion, err := lookupCompletion(ctx, best.ID); err == nil {
		meta.Completion = completion
	}

	enrichMods(ctx, meta)

	return meta, nil
}

func nexusDomain(game string) string {
	lower := strings.ToLower(game)
	mappings := [][2]string{
		{"skyrim", "skyrimspecialedition"},
		{"witcher 3", "witcher3"},
		{"fallout 4", "fallout4"},
		{"fallout: new vegas", "newvegas"},
		{"cyberpunk 2077", "cyberpunk2077"},
		{"baldur's gate 3", "baldursgate3"},
		{"stardew valley", "stardewvalley"},
		{"terraria", "terraria"},
		{"minecraft", "minecraft"},
		{"mount & blade", "mountandblade2bannerlord"},
		{"dark souls", "darksouls"},
		{"elden ring", "eldenring"},
	}

	for _, mapping := range mappings {
		if strings.Contains(lower, mapping[0]) {
			return mapping[1]
		}
	}

	return ""
}

func enrichMods(ctx context.Context, meta *Meta) {
	domain := nexusDomain(meta.Title)
	if domain == "" {
		return
	}

	meta.ModsURL = "https://www.nexusmods.com/games/" + domain + "/mods?sort=trending"

	apiKey := os.Getenv("NEXUS_API_KEY")
	if apiKey == "" {
		return
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.nexusmods.com/v1/games/"+domain+"/mods/trending.json", nil)
	if err != nil {
		return
	}

	request.Header.Set("apikey", apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
	if err != nil {
		return
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return
	}

	var payload []struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return
	}

	for index, item := range payload {
		if index >= 5 {
			break
		}

		if item.Name != "" {
			duplicate := false

			for _, existing := range meta.Mods {
				if existing == item.Name {
					duplicate = true

					break
				}
			}

			if !duplicate {
				meta.Mods = append(meta.Mods, item.Name)
			}
		}
	}
}

func isEdition(base, candidate string) bool {
	lower := strings.ToLower(candidate)
	words := []string{"edition", "remaster", "reforged", "definitive", "complete", "deluxe",
		"ultimate", "gold", "anniversary", "collection", "vr", "enhanced", "legendary"}

	hasEditionWord := false

	for _, word := range words {
		if strings.Contains(lower, word) {
			hasEditionWord = true

			break
		}
	}

	if !hasEditionWord {
		return false
	}

	baseWords := strings.Fields(normalize(base))
	candidateWords := strings.Fields(normalize(candidate))
	matches := 0

	for _, baseWord := range baseWords {
		if len(baseWord) < 3 {
			continue
		}

		for _, candidateWord := range candidateWords {
			if baseWord == candidateWord {
				matches++

				break
			}
		}
	}

	return matches >= 2
}

func localizeGenre(value, lang string) string {
	if value == "" || lang != "ru" {
		return value
	}

	replacements := [][2]string{
		{"Real Time Strategy (RTS)", "Стратегия в реальном времени (RTS)"},
		{"Role-playing (RPG)", "Ролевая игра (RPG)"},
		{"Adventure", "Приключение"},
		{"Strategy", "Стратегия"},
		{"Shooter", "Шутер"},
		{"Simulator", "Симулятор"},
		{"Racing", "Гонки"},
		{"Sport", "Спорт"},
		{"Fighting", "Файтинг"},
		{"Puzzle", "Головоломка"},
		{"Platform", "Платформер"},
		{"Indie", "Инди"},
		{"Arcade", "Аркада"},
		{"Tactical", "Тактика"},
		{"Music", "Музыка"},
		{"Point-and-click", "Квест"},
		{"Visual Novel", "Визуальная новелла"},
		{"Card & Board Game", "Карточная игра"},
	}

	for _, pair := range replacements {
		value = strings.ReplaceAll(value, pair[0], pair[1])
	}

	return value
}

func lookupCompletion(ctx context.Context, gameID int) (*Completion, error) {
	body, err := igdbQuery(ctx, "game_time_to_beats",
		fmt.Sprintf("fields game_id,hastily,normally,completely; where game_id = %d; limit 1;", gameID))
	if err != nil {
		return nil, err
	}

	var values []struct {
		Hastily    int `json:"hastily"`
		Normally   int `json:"normally"`
		Completely int `json:"completely"`
	}

	if err := json.Unmarshal(body, &values); err != nil || len(values) == 0 {
		return nil, fmt.Errorf("время прохождения не найдено")
	}

	hours := func(seconds int) float64 {
		if seconds <= 0 {
			return 0
		}

		return math.Round(float64(seconds)/3600*10) / 10
	}

	completion := &Completion{
		Hastily:    hours(values[0].Hastily),
		Normally:   hours(values[0].Normally),
		Completely: hours(values[0].Completely),
	}

	if completion.Hastily == 0 && completion.Normally == 0 && completion.Completely == 0 {
		return nil, fmt.Errorf("время прохождения не найдено")
	}

	return completion, nil
}

func enrichIGDB(ctx context.Context, meta *Meta, name, lang string) {
	igdb, err := lookupIGDB(ctx, name, lang)
	if err != nil {
		return
	}

	if igdb.Image != "" {
		meta.Image = igdb.Image
	}

	if len(igdb.DLC) > 0 {
		meta.DLC = igdb.DLC
	}

	if len(meta.Editions) == 0 && len(igdb.Editions) > 0 {
		meta.Editions = igdb.Editions
	}

	if len(igdb.Platforms) > 0 {
		meta.Platforms = igdb.Platforms
	}

	if igdb.Completion != nil {
		meta.Completion = igdb.Completion
	}

	if igdb.Rating > 0 {
		meta.Rating = igdb.Rating
	}

	if meta.Genre == "" {
		meta.Genre = igdb.Genre
	}

	if meta.Year == 0 {
		meta.Year = igdb.Year
	}

	if meta.Description == "" {
		meta.Description = igdb.Description
	}

	if meta.Storyline == "" {
		meta.Storyline = igdb.Storyline
	}

	if len(meta.Developers) == 0 {
		meta.Developers = igdb.Developers
	}

	if len(meta.Publishers) == 0 {
		meta.Publishers = igdb.Publishers
	}

	if len(meta.Themes) == 0 {
		meta.Themes = igdb.Themes
	}

	if len(meta.GameModes) == 0 {
		meta.GameModes = igdb.GameModes
	}

	if len(meta.Perspectives) == 0 {
		meta.Perspectives = igdb.Perspectives
	}

	if meta.ReleaseDate == "" {
		meta.ReleaseDate = igdb.ReleaseDate
	}

	if meta.Website == "" {
		meta.Website = igdb.Website
	}

	if meta.AggregatedRating == 0 {
		meta.AggregatedRating = igdb.AggregatedRating
	}

	meta.Mods = igdb.Mods
	meta.ModsURL = igdb.ModsURL

	meta.Source = "igdb"
	meta.Subtitle = "IGDB"
}

func lookupSteam(ctx context.Context, name, lang string) (*Meta, error) {
	language := "english"

	if lang == "ru" || (lang == "" && hasCyrillic(name)) {
		language = "russian"
	}

	meta, err := lookupSteamLang(ctx, name, language)
	if err == nil {
		return meta, nil
	}

	fallback := "russian"
	if language == "russian" {
		fallback = "english"
	}

	return lookupSteamLang(ctx, name, fallback)
}

func lookupSteamLang(ctx context.Context, name, language string) (*Meta, error) {
	endpoint := "https://store.steampowered.com/api/storesearch/?term=" +
		url.QueryEscape(name) + "&l=" + language + "&cc=US"

	body, err := get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var search struct {
		Items []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &search); err != nil || len(search.Items) == 0 {
		return nil, fmt.Errorf("игра не найдена")
	}

	best := search.Items[0]
	bestScore := 0

	for _, item := range search.Items {
		if isSteamNoise(name, item.Name) {
			continue
		}

		score := matchScore(name, item.Name)

		if score > bestScore {
			best = item
			bestScore = score
		}
	}

	if bestScore == 0 {
		return nil, fmt.Errorf("игра не найдена")
	}

	detailsURL := "https://store.steampowered.com/api/appdetails?appids=" +
		strconv.Itoa(best.ID) + "&l=" + language + "&cc=US"

	detailsBody, err := get(ctx, detailsURL)
	if err != nil {
		return nil, err
	}

	var details map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name         string `json:"name"`
			ShortDesc    string `json:"short_description"`
			DetailedDesc string `json:"detailed_description"`
			HeaderImage  string `json:"header_image"`
			Website      string `json:"website"`
			ReleaseDate  struct {
				Date string `json:"date"`
			} `json:"release_date"`
			Developers []string `json:"developers"`
			Publishers []string `json:"publishers"`
			Platforms  struct {
				Windows bool `json:"windows"`
				Mac     bool `json:"mac"`
				Linux   bool `json:"linux"`
			} `json:"platforms"`
			Genres []struct {
				Description string `json:"description"`
			} `json:"genres"`
			Categories []struct {
				Description string `json:"description"`
			} `json:"categories"`
		} `json:"data"`
	}

	if err := json.Unmarshal(detailsBody, &details); err != nil {
		return nil, err
	}

	info, ok := details[strconv.Itoa(best.ID)]
	if !ok || !info.Success {
		return nil, fmt.Errorf("игра не найдена")
	}

	genres := make([]string, 0, len(info.Data.Genres))
	for _, genre := range info.Data.Genres {
		genres = append(genres, genre.Description)
	}

	platforms := make([]string, 0, 3)
	if info.Data.Platforms.Windows {
		platforms = append(platforms, "Windows")
	}

	if info.Data.Platforms.Mac {
		platforms = append(platforms, "macOS")
	}

	if info.Data.Platforms.Linux {
		platforms = append(platforms, "Linux")
	}

	return &Meta{
		Type:        "game",
		Title:       info.Data.Name,
		Subtitle:    "Steam",
		Year:        parseYear(info.Data.ReleaseDate.Date),
		Genre:       strings.Join(genres, ", "),
		Description: info.Data.ShortDesc,
		Image:       info.Data.HeaderImage,
		URL:         "https://store.steampowered.com/app/" + strconv.Itoa(best.ID),
		Source:      "steam",
		Developers:  info.Data.Developers,
		Publishers:  info.Data.Publishers,
		Platforms:   platforms,
		ReleaseDate: info.Data.ReleaseDate.Date,
		Website:     info.Data.Website,
		Storyline:   stripHTML(info.Data.DetailedDesc),
	}, nil
}

func lookupITunes(ctx context.Context, name, entity, label, lang string) (*Meta, error) {
	country := "RU"
	locale := "ru_ru"

	if lang == "en" {
		country = "US"
		locale = "en_us"
	}

	endpoint := "https://itunes.apple.com/search?term=" +
		url.QueryEscape(name) + "&entity=" + entity + "&limit=5&country=" + country + "&lang=" + locale

	body, err := get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Results []struct {
			TrackName       string `json:"trackName"`
			ArtistName      string `json:"artistName"`
			CollectionName  string `json:"collectionName"`
			ReleaseDate     string `json:"releaseDate"`
			PrimaryGenre    string `json:"primaryGenreName"`
			Artwork         string `json:"artworkUrl100"`
			ViewURL         string `json:"trackViewUrl"`
			ShortDesc       string `json:"shortDescription"`
			LongDesc        string `json:"longDescription"`
			TrackTimeMillis int64  `json:"trackTimeMillis"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Results) == 0 {
		return nil, fmt.Errorf("%s не найден", strings.ToLower(label))
	}

	bestScore := 0
	best := payload.Results[0]

	for _, result := range payload.Results {
		title := result.TrackName
		if title == "" {
			title = result.CollectionName
		}

		if score := matchScore(name, title); score > bestScore {
			best = result
			bestScore = score
		}
	}

	if bestScore == 0 {
		return nil, fmt.Errorf("%s не найден", strings.ToLower(label))
	}

	title := best.TrackName
	if title == "" {
		title = best.CollectionName
	}

	subtitle := best.ArtistName
	if subtitle == "" {
		subtitle = best.CollectionName
	}

	description := best.ShortDesc
	if description == "" {
		description = best.LongDesc
	}

	return &Meta{
		Type:        map[string]string{"movie": "movie", "song": "music"}[entity],
		Title:       title,
		Subtitle:    subtitle,
		Year:        parseYear(best.ReleaseDate),
		Genre:       best.PrimaryGenre,
		Description: description,
		Image:       strings.ReplaceAll(best.Artwork, "100x100", "300x300"),
		URL:         best.ViewURL,
		Source:      "itunes",
	}, nil
}

func post(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (https://github.com/p7x-dev/nameless-streamhata)")
	request.Header.Set("Accept", "application/json")

	response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("источник вернул %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 2<<20))
}

func get(ctx context.Context, endpoint string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}

		request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (https://github.com/p7x-dev/nameless-streamhata)")
		request.Header.Set("Accept", "application/json")

		response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
		if err != nil {
			return nil, err
		}

		if response.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			delay := 2 * time.Second

			if retryAfter, parseErr := strconv.Atoi(response.Header.Get("Retry-After")); parseErr == nil &&
				retryAfter > 0 && retryAfter <= 5 {
				delay = time.Duration(retryAfter) * time.Second
			}

			_ = response.Body.Close()

			select {
			case <-time.After(delay):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()

			return nil, fmt.Errorf("источник вернул %d", response.StatusCode)
		}

		body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		_ = response.Body.Close()

		return body, readErr
	}
}

func parseYear(value string) int {
	for index := 0; index+4 <= len(value); index++ {
		year, err := strconv.Atoi(value[index : index+4])
		if err == nil && year >= 1900 && year <= 2100 {
			return year
		}
	}

	return 0
}

func matchScore(query, candidate string) int {
	normalizedQuery := normalize(query)
	normalizedCandidate := normalize(candidate)

	if normalizedQuery == "" || normalizedCandidate == "" {
		return 0
	}

	if normalizedQuery == normalizedCandidate {
		return 4
	}

	if strings.Contains(normalizedCandidate, normalizedQuery) ||
		strings.Contains(normalizedQuery, normalizedCandidate) {
		return 2
	}

	return 0
}

func hasCyrillic(value string) bool {
	for _, symbol := range value {
		if unicode.Is(unicode.Cyrillic, symbol) {
			return true
		}
	}

	return false
}

func isSteamNoise(query, candidate string) bool {
	lowerQuery := strings.ToLower(query)
	lowerCandidate := strings.ToLower(candidate)
	noise := []string{"soundtrack", "ost", "dlc", "upgrade", "expansion", "season pass", "creations", "kit", "editor", "mod", "demo"}

	for _, word := range noise {
		if strings.Contains(lowerCandidate, word) && !strings.Contains(lowerQuery, word) {
			return true
		}
	}

	return false
}

func wikiImage(ctx context.Context, images []struct {
	Title string `json:"title"`
}, query, title, kind, lang string) string {
	query = normalize(query)
	title = normalize(title)
	best := ""
	bestScore := -100

	for _, image := range images {
		name := strings.TrimPrefix(image.Title, "Файл:")
		lower := strings.ToLower(name)

		if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") &&
			!strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".webp") {
			continue
		}

		junk := []string{"flag", "commons", "icon", "logo", "star", "map", "gameplay", "cosplay",
			"screenshot", "notification", "wikiquote", "question", "arrow", "edit", "youtube",
			"folder", "double-dagger", "person", "portrait"}

		if kind != "music" {
			junk = append(junk, "soundtrack", "album")
		}

		score := 0

		for _, word := range junk {
			if strings.Contains(lower, word) {
				score -= 20

				break
			}
		}

		normalized := normalize(strings.TrimSuffix(name, ".jpg"))
		if query != "" && strings.Contains(normalized, query) {
			score += 8
		}

		if title != "" && strings.Contains(normalized, title) {
			score += 6
		}

		for _, word := range []string{"poster", "cover", "постер", "обложка"} {
			if strings.Contains(lower, word) {
				score += 4

				break
			}
		}

		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
			score += 1
		}

		if score > bestScore {
			best = image.Title
			bestScore = score
		}
	}

	if best == "" || bestScore < 0 {
		return ""
	}

	endpoint := "https://" + lang + ".wikipedia.org/w/api.php?action=query&prop=imageinfo&iiprop=url&iiurlwidth=600&titles=" +
		url.QueryEscape(best) + "&format=json&utf8=1"

	body, err := get(ctx, endpoint)
	if err != nil {
		return ""
	}

	var payload struct {
		Query struct {
			Pages map[string]struct {
				ImageInfo []struct {
					ThumbURL string `json:"thumburl"`
					URL      string `json:"url"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	for _, page := range payload.Query.Pages {
		if len(page.ImageInfo) == 0 {
			continue
		}

		imageURL := page.ImageInfo[0].ThumbURL
		if imageURL == "" {
			imageURL = page.ImageInfo[0].URL
		}

		if index := strings.Index(imageURL, "?"); index >= 0 {
			imageURL = imageURL[:index]
		}

		return imageURL
	}

	return ""
}

func firstDate(value string) string {
	if len(value) >= 10 {
		return value[:10]
	}

	return value
}

var htmlTag = regexp.MustCompile(`<[^>]+>`)

func stripHTML(value string) string {
	value = strings.ReplaceAll(value, "<br>", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	value = strings.ReplaceAll(value, "<br />", "\n")
	value = htmlTag.ReplaceAllString(value, " ")

	return strings.Join(strings.Fields(value), " ")
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}

func lookupDeezer(ctx context.Context, name string) (*Meta, error) {
	endpoint := "https://api.deezer.com/search?q=" + url.QueryEscape(name) + "&limit=5"

	body, err := get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Data []struct {
			Title    string `json:"title"`
			Link     string `json:"link"`
			Duration int    `json:"duration"`
			Artist   struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				ID     int    `json:"id"`
				Title  string `json:"title"`
				Cover  string `json:"cover_xl"`
				CoverB string `json:"cover_big"`
			} `json:"album"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		return nil, fmt.Errorf("трек не найден")
	}

	bestScore := 0
	best := payload.Data[0]

	for _, item := range payload.Data {
		if score := matchScore(name, item.Title); score > bestScore {
			best = item
			bestScore = score
		}
	}

	if bestScore == 0 {
		return nil, fmt.Errorf("трек не найден")
	}

	image := best.Album.Cover
	if image == "" {
		image = best.Album.CoverB
	}

	return &Meta{
		Type:            "music",
		Title:           best.Title,
		Subtitle:        best.Artist.Name,
		Genre:           best.Album.Title,
		Image:           image,
		URL:             best.Link,
		Source:          "deezer",
		Artist:          best.Artist.Name,
		Album:           best.Album.Title,
		DurationSeconds: best.Duration,
	}, nil
}

func canonicalEdition(value, lang string) string {
	lower := strings.ToLower(value)

	if strings.Contains(lower, "very special") {
		return ""
	}

	switch {
	case strings.Contains(lower, "legendary") || strings.Contains(lower, "легендар"):
		if lang == "ru" {
			return "Легендарное издание"
		}

		return "Legendary Edition"
	case strings.Contains(lower, "anniversary") || strings.Contains(lower, "юбилей"):
		if lang == "ru" {
			return "Юбилейное издание"
		}

		return "Anniversary Edition"
	case strings.Contains(lower, "collector") || strings.Contains(lower, "коллекцион"):
		if lang == "ru" {
			return "Коллекционное издание"
		}

		return "Collector's Edition"
	case strings.Contains(lower, "premium") || strings.Contains(lower, "премиум"):
		if lang == "ru" {
			return "Премиум-издание"
		}

		return "Premium Edition"
	case strings.Contains(lower, "special") || strings.Contains(lower, "специальн"):
		if lang == "ru" {
			return "Специальное издание"
		}

		return "Special Edition"
	case strings.Contains(lower, "vr"):
		return "VR"
	}

	return ""
}

func wikiEditions(ctx context.Context, lang, title string) []string {
	endpoint := "https://" + lang + ".wikipedia.org/w/api.php?action=query&prop=extracts&explaintext=1&titles=" +
		url.QueryEscape(title) + "&format=json&utf8=1"

	body, err := get(ctx, endpoint)
	if err != nil {
		return nil
	}

	var payload struct {
		Query struct {
			Pages map[string]struct {
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	result := make([]string, 0)

	for _, page := range payload.Query.Pages {
		lower := strings.ToLower(page.Extract)

		keywords := []string{"legendary edition", "special edition", "anniversary edition",
			"collector's edition", "collectors edition", "premium edition", "vr version",
			"легендарное издание", "специальное издание", "юбилейное издание",
			"коллекционное издание", "премиум-издание"}

		for _, keyword := range keywords {
			if !strings.Contains(lower, keyword) {
				continue
			}

			name := canonicalEdition(keyword, lang)
			if name == "" || seen[name] {
				continue
			}

			seen[name] = true
			result = append(result, name)
		}
	}

	return result
}

func translateViaWikipedia(ctx context.Context, name string) string {
	if !hasCyrillic(name) {
		return ""
	}

	endpoint := "https://ru.wikipedia.org/w/api.php?action=query&format=json&redirects=1&prop=langlinks&lllang=en&titles=" +
		url.QueryEscape(name)

	body, err := get(ctx, endpoint)
	if err != nil {
		return ""
	}

	var payload struct {
		Query struct {
			Pages map[string]struct {
				LangLinks []struct {
					Lang  string `json:"lang"`
					Title string `json:"*"`
				} `json:"langlinks"`
			} `json:"pages"`
		} `json:"query"`
	}

	if json.Unmarshal(body, &payload) != nil {
		return ""
	}

	for _, page := range payload.Query.Pages {
		for _, link := range page.LangLinks {
			if link.Lang == "en" && link.Title != "" {
				return link.Title
			}
		}
	}

	return ""
}

func lookupWikipedia(ctx context.Context, name, lang string) (*Meta, error) {
	primary := "en"
	secondary := "ru"

	if lang == "ru" || (lang == "" && hasCyrillic(name)) {
		primary, secondary = "ru", "en"
	}

	first, firstErr := lookupWikipediaLang(ctx, name, primary)
	if firstErr == nil && first.Type != "other" {
		return first, nil
	}

	second, secondErr := lookupWikipediaLang(ctx, name, secondary)
	if secondErr == nil && second.Type != "other" {
		return second, nil
	}

	if firstErr == nil {
		return first, nil
	}

	if secondErr == nil {
		return second, nil
	}

	return nil, firstErr
}

func lookupWikipediaTyped(ctx context.Context, name, kind, lang string) (*Meta, error) {
	if meta, err := lookupWikipedia(ctx, name, lang); err == nil && meta.Type != "other" {
		if kind == "" || meta.Type == kind {
			return meta, nil
		}
	}

	suffixes := []string{" film", " video game", " song", " album"}

	switch kind {
	case "movie":
		suffixes = []string{" film", " movie"}
	case "game":
		suffixes = []string{" video game", " game"}
	case "music":
		suffixes = []string{" song", " album", " single"}
	}

	for _, suffix := range suffixes {
		if meta, err := lookupWikipedia(ctx, name+suffix, lang); err == nil &&
			meta.Type != "other" && (kind == "" || meta.Type == kind) {
			return meta, nil
		}
	}

	return nil, fmt.Errorf("страница не найдена")
}

func lookupWikipediaLang(ctx context.Context, name, lang string) (*Meta, error) {
	searchURL := "https://" + lang + ".wikipedia.org/w/api.php?action=query&list=search&srsearch=" +
		url.QueryEscape(name) + "&srlimit=3&format=json&utf8=1"

	body, err := get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	var search struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &search); err != nil || len(search.Query.Search) == 0 {
		return nil, fmt.Errorf("страница не найдена")
	}

	bestTitle := ""
	bestScore := 0

	for _, item := range search.Query.Search {
		if score := matchScore(name, item.Title); score > bestScore {
			bestTitle = item.Title
			bestScore = score
		}
	}

	if bestTitle == "" || bestScore == 0 {
		return nil, fmt.Errorf("страница не найдена")
	}

	pageURL := "https://" + lang + ".wikipedia.org/w/api.php?action=query&prop=pageimages|extracts|info|images&imlimit=50&exintro=1&explaintext=1&piprop=original&inprop=url&titles=" +
		url.QueryEscape(bestTitle) + "&format=json&utf8=1"

	pageBody, err := get(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	var page struct {
		Query struct {
			Pages map[string]struct {
				Title    string `json:"title"`
				Extract  string `json:"extract"`
				FullURL  string `json:"fullurl"`
				Original struct {
					Source string `json:"source"`
				} `json:"original"`
				Images []struct {
					Title string `json:"title"`
				} `json:"images"`
			} `json:"pages"`
		} `json:"query"`
	}

	if err := json.Unmarshal(pageBody, &page); err != nil {
		return nil, err
	}

	for _, item := range page.Query.Pages {
		if item.Title == "" {
			continue
		}

		extract := strings.TrimSpace(item.Extract)
		lower := strings.ToLower(extract)
		kind := "other"

		switch {
		case strings.Contains(lower, "фильм") || strings.Contains(lower, "film"):
			kind = "movie"
		case strings.Contains(lower, "игр") || strings.Contains(lower, "video game"):
			kind = "game"
		case strings.Contains(lower, "песн") || strings.Contains(lower, "song") ||
			strings.Contains(lower, "альбом") || strings.Contains(lower, "album"):
			kind = "music"
		}

		image := item.Original.Source
		if image == "" {
			image = wikiImage(ctx, item.Images, name, item.Title, kind, lang)
		}

		meta := &Meta{
			Type:        kind,
			Title:       item.Title,
			Subtitle:    "Wikipedia",
			Year:        parseYear(extract),
			Genre:       "",
			Description: truncate(extract, 300),
			Image:       image,
			URL:         item.FullURL,
			Source:      "wikipedia",
		}

		if meta.Image == "" {
			switch kind {
			case "game":
				if steam, err := lookupSteam(ctx, item.Title, lang); err == nil {
					meta.Image = steam.Image
				}
			case "movie":
				if movie, err := lookupITunes(ctx, item.Title, "movie", "Фильм", lang); err == nil {
					meta.Image = movie.Image
				}
			}
		}

		if meta.Image == "" && kind == "music" {
			if track, err := lookupDeezer(ctx, item.Title); err == nil {
				meta.Image = track.Image
			}
		}

		if kind == "game" {
			meta.Editions = wikiEditions(ctx, lang, item.Title)
		}

		return meta, nil
	}

	return nil, fmt.Errorf("страница не найдена")
}

func truncate(value string, limit int) string {
	runes := []rune(value)

	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit]) + "…"
}
