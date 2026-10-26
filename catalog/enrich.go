package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Enrichment struct {
	Source             string              `json:"source"`
	Title              string              `json:"title,omitempty"`
	TitleRu            string              `json:"titleRu,omitempty"`
	Aliases            []string            `json:"aliases,omitempty"`
	DLC                []string            `json:"dlc,omitempty"`
	Editions           []string            `json:"editions,omitempty"`
	AgeRating          string              `json:"ageRating,omitempty"`
	SourceURL          string              `json:"sourceUrl"`
	Cover              string              `json:"cover,omitempty"`
	ReleaseDate        string              `json:"releaseDate,omitempty"`
	Year               int                 `json:"year,omitempty"`
	Description        string              `json:"description,omitempty"`
	DescriptionRu      string              `json:"descriptionRu,omitempty"`
	Screenshots        []string            `json:"screenshots,omitempty"`
	Genres             []string            `json:"genres,omitempty"`
	Modes              []string            `json:"modes,omitempty"`
	Perspectives       []string            `json:"perspectives,omitempty"`
	Themes             []string            `json:"themes,omitempty"`
	Developers         []string            `json:"developers,omitempty"`
	Publishers         []string            `json:"publishers,omitempty"`
	Platforms          []string            `json:"platforms,omitempty"`
	Rating             float64             `json:"rating,omitempty"`
	AggregatedRating   float64             `json:"aggregatedRating,omitempty"`
	Website            string              `json:"website,omitempty"`
	Completion         *Completion         `json:"completion,omitempty"`
	Downloads          []DownloadLink      `json:"downloads,omitempty"`
	Sources            []GameSource        `json:"sources,omitempty"`
	PopularMods        []PopularMod        `json:"popularMods,omitempty"`
	PopularCollections []PopularCollection `json:"popularCollections,omitempty"`
	CoverSource        string              `json:"coverSource,omitempty"`
}

type Completion struct {
	HastilyHours    float64 `json:"hastilyHours,omitempty"`
	NormallyHours   float64 `json:"normallyHours,omitempty"`
	CompletelyHours float64 `json:"completelyHours,omitempty"`
}

type Enricher struct {
	db           *sql.DB
	client       *http.Client
	igdb         *igdbClient
	rawg         *rawgClient
	moby         *mobyClient
	old          *oldgamesClient
	wiki         *wikipediaClient
	gb           *giantbombClient
	tgdb         *thegamesdbClient
	play         *longplayClient
	mobyBr       *mobyBrowser
	steam        *steamClient
	vndb         *vndbClient
	wikiD        *wikidataClient
	aband        *abandonwareClient
	gog          *gogClient
	nexus        *nexusClient
	banana       *gamebananaClient
	translations sync.Map
}

func newEnricher(db *sql.DB) *Enricher {
	enricher := &Enricher{
		db:     db,
		client: &http.Client{Timeout: 12 * time.Second},
	}
	enricher.igdb = newIGDBClient(enricher.client)
	enricher.rawg = newRAWGClient(enricher.client)
	enricher.moby = newMobyClient(enricher.client)
	enricher.old = newOldgamesClient(enricher.client)
	enricher.wiki = newWikipediaClient(enricher.client)
	enricher.gb = newGiantbombClient(enricher.client)
	enricher.tgdb = newTheGamesDBClient(enricher.client)
	enricher.play = newLongplayClient(enricher.client)
	enricher.mobyBr = newMobyBrowser()
	enricher.steam = newSteamClient(enricher.client)
	enricher.vndb = newVNDBClient(enricher.client)
	enricher.wikiD = newWikidataClient(enricher.client)
	enricher.aband = newAbandonwareClient(enricher.client)
	enricher.gog = newGOGClient(enricher.client)
	enricher.nexus = newNexusClient(enricher.client)
	enricher.banana = newGameBananaClient(enricher.client)

	if strings.TrimSpace(os.Getenv("CATALOG_ENABLED")) != "0" {
		enricher.loadCache()
	}

	return enricher
}

func (e *Enricher) Enrich(ctx context.Context, game Game, detailed bool, verify bool) Game {
	if cached, ok := e.cached(game.Slug); ok {
		return mergeEnrichment(game, cached)
	}

	if !detailed {
		return game
	}

	enrichment, err := e.fetch(ctx, game.Title, verify)
	if err != nil || enrichment == nil {
		e.storeMiss(game.Slug, err)
		log.Printf("[enrich] %s: %v", game.Slug, err)

		return game
	}

	log.Printf("[enrich] %s: %s", game.Slug, enrichment.Source)

	e.store(game.Slug, *enrichment)

	return mergeEnrichment(game, *enrichment)
}

func (e *Enricher) Start(games []Game) {
	if strings.TrimSpace(os.Getenv("ENRICH_BACKGROUND")) != "1" {
		return
	}

	missing := make([]Game, 0, len(games))

	for _, game := range games {
		if len(missing) >= 8 {
			break
		}

		if _, ok := e.cached(game.Slug); !ok {
			missing = append(missing, game)
		}
	}

	if len(missing) == 0 {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		e.EnrichMany(ctx, missing, true, false)
	}()
}

func (e *Enricher) EnrichMany(ctx context.Context, games []Game, detailed bool, verify bool) []Game {
	result := make([]Game, len(games))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup

	for index, game := range games {
		index, game := index, game

		if cached, ok := e.cached(game.Slug); ok {
			result[index] = mergeEnrichment(game, cached)

			continue
		}

		if !detailed {
			result[index] = game

			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				result[index] = game

				return
			}

			result[index] = e.Enrich(ctx, game, true, verify)
		}()
	}

	wg.Wait()

	return result
}

func bondCanonical(title string) string {
	trimmed := strings.TrimSpace(title)
	lower := strings.ToLower(trimmed)

	if strings.HasPrefix(lower, "007") {
		return "James Bond 007" + trimmed[3:]
	}

	return trimmed
}

func searchTitle(title string) string {
	trimmed := bondCanonical(strings.TrimSpace(title))
	trimmed = strings.Trim(trimmed, `!?.,:;\"'`+"`"+`~#$%^&*()[]{}-_+=|\/`)

	if trimmed == "" {
		return title
	}

	return trimmed
}

func (e *Enricher) fetch(ctx context.Context, title string, verify bool) (*Enrichment, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	type source struct {
		label string
		fn    func(context.Context, string) (*Enrichment, error)
	}

	type outcome struct {
		label      string
		enrichment *Enrichment
		err        error
	}

	sources := make([]source, 0, 9)
	query := searchTitle(title)

	if e.igdb != nil {
		sources = append(sources, source{"igdb", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.igdb.Search(ctx, query)
		}})
	}

	if e.rawg != nil {
		sources = append(sources, source{"rawg", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.rawg.Search(ctx, query)
		}})
	}

	if e.old != nil {
		sources = append(sources, source{"old-games", e.old.Search})
	}

	if e.gb != nil {
		sources = append(sources, source{"giantbomb", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.gb.Search(ctx, query)
		}})
	}

	if e.tgdb != nil {
		sources = append(sources, source{"thegamesdb", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.tgdb.Search(ctx, query)
		}})
	}

	if e.wiki != nil {
		sources = append(sources, source{"wikipedia", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.wiki.Search(ctx, query)
		}})
	}

	if e.play != nil {
		sources = append(sources, source{"longplay", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.play.Search(ctx, query)
		}})
	}

	if e.mobyBr != nil {
		sources = append(sources, source{"mobygames-browser", e.mobyBr.Search})
	}

	if e.moby != nil {
		sources = append(sources, source{"mobygames", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.moby.Search(ctx, query)
		}})
	}

	if e.steam != nil {
		sources = append(sources, source{"steam", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.steam.Search(ctx, query)
		}})
	}

	if e.vndb != nil {
		sources = append(sources, source{"vndb", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.vndb.Search(ctx, query)
		}})
	}

	if e.wikiD != nil {
		sources = append(sources, source{"wikidata", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.wikiD.Search(ctx, query)
		}})
	}

	if e.aband != nil && strings.TrimSpace(os.Getenv("CATALOG_ABANDONWARE")) == "1" {
		sources = append(sources, source{"abandonware", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.aband.Search(ctx, query)
		}})
	}

	if e.gog != nil {
		sources = append(sources, source{"gog", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.gog.Search(ctx, query)
		}})
	}

	if e.nexus != nil && strings.TrimSpace(os.Getenv("NEXUS_API_KEY")) != "" {
		sources = append(sources, source{"nexusmods", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.nexus.Search(ctx, query)
		}})
	}

	if e.banana != nil {
		sources = append(sources, source{"gamebanana", func(ctx context.Context, _ string) (*Enrichment, error) {
			return e.banana.Search(ctx, query)
		}})
	}

	var result *Enrichment

	merge := func(enrichment *Enrichment, err error, label string) {
		if err != nil || enrichment == nil {
			return
		}

		if !matchTitle(title, enrichment.Title) && !matchTitle(query, enrichment.Title) {
			return
		}

		if verify && !e.valid(ctx, enrichment.SourceURL, enrichment.Source) {
			return
		}

		if result == nil {
			result = &Enrichment{}
		}

		mergeEnrichments(result, *enrichment)
	}

	outcomes := make(chan outcome, len(sources))

	for _, item := range sources {
		item := item

		go func() {
			child, childCancel := context.WithTimeout(ctx, 12*time.Second)
			defer childCancel()

			enrichment, err := item.fn(child, title)
			outcomes <- outcome{item.label, enrichment, err}
		}()
	}

collect:
	for range sources {
		select {
		case item := <-outcomes:
			merge(item.enrichment, item.err, item.label)
		case <-ctx.Done():
			break collect
		}
	}

	if result == nil {
		enrichment, err := e.fetchMoby(ctx, title)
		if err == nil && enrichment != nil && matchTitle(title, enrichment.Title) {
			aggregate := &Enrichment{}
			mergeEnrichments(aggregate, *enrichment)
			result = aggregate
		}
	}

	if result == nil {
		return nil, errors.New("no matching sources")
	}

	return result, nil
}

var serviceSourceIDs = map[string]bool{
	"igdb":          true,
	"rawg":          true,
	"mobygames":     true,
	"old-games":     true,
	"giantbomb":     true,
	"thegamesdb":    true,
	"wikipedia":     true,
	"screenscraper": true,
	"longplay":      true,
	"steam":         true,
	"vndb":          true,
	"wikidata":      true,
	"abandonware":   true,
	"gog":           true,
	"nexusmods":     true,
	"gamebanana":    true,
}

func isServiceGame(game Game) bool {
	for _, source := range game.Sources {
		if serviceSourceIDs[source.ID] {
			return true
		}
	}

	return false
}

func (e *Enricher) searchExtras(slug string) []string {
	cached, ok := e.cached(slug)
	if !ok {
		return nil
	}

	result := translateGenres(cached.Genres)

	if cached.TitleRu != "" {
		result = append(result, cached.TitleRu)
	}

	return result
}

func (e *Enricher) Has(slug string) bool {
	_, ok := e.cached(slug)

	return ok
}

func (e *Enricher) FilterVerified(games []Game, verified bool) []Game {
	result := make([]Game, 0, len(games))

	for _, game := range games {
		known := e.Has(game.Slug) || isServiceGame(game)

		if known == verified {
			result = append(result, game)
		}
	}

	return result
}

func (e *Enricher) cached(slug string) (Enrichment, bool) {
	if e.db == nil {
		return Enrichment{}, false
	}

	var raw []byte

	if err := e.db.QueryRowContext(
		context.Background(),
		`SELECT payload FROM game_enrichment WHERE slug = $1`,
		slug,
	).Scan(&raw); err != nil {
		return Enrichment{}, false
	}

	var value Enrichment

	if json.Unmarshal(raw, &value) != nil {
		return Enrichment{}, false
	}

	return value, true
}

func (e *Enricher) store(slug string, enrichment Enrichment) {
	if e.db == nil {
		return
	}

	raw, err := json.Marshal(enrichment)
	if err != nil {
		return
	}

	_, _ = e.db.ExecContext(
		context.Background(),
		`INSERT INTO game_enrichment (slug, source, source_url, payload, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (slug) DO UPDATE SET
		   source = EXCLUDED.source,
		   source_url = EXCLUDED.source_url,
		   payload = EXCLUDED.payload,
		   updated_at = now()`,
		slug,
		enrichment.Source,
		enrichment.SourceURL,
		string(raw),
	)
}

func (e *Enricher) storeMiss(slug string, reason error) {
	if e.db == nil {
		return
	}

	message := ""

	if reason != nil {
		message = reason.Error()
	}

	_, _ = e.db.ExecContext(
		context.Background(),
		`INSERT INTO game_enrichment_miss (slug, attempts, last_error, updated_at)
		 VALUES ($1, 1, $2, now())
		 ON CONFLICT (slug) DO UPDATE SET
		   attempts = game_enrichment_miss.attempts + 1,
		   last_error = EXCLUDED.last_error,
		   updated_at = now()`,
		slug,
		message,
	)
}

func (e *Enricher) loadCache() {}

var coverPriority = map[string]int{
	"igdb":       100,
	"steam":      90,
	"gog":        80,
	"rawg":       70,
	"vndb":       65,
	"mobygames":  60,
	"thegamesdb": 55,
	"giantbomb":  50,
	"old-games":  40,
	"wikidata":   35,
	"longplay":   30,
	"wikipedia":  20,
}

func mergeEnrichments(target *Enrichment, source Enrichment) {
	if target.Source == "" {
		target.Source = source.Source
	}

	if target.SourceURL == "" {
		target.SourceURL = source.SourceURL
	}

	if target.Title == "" {
		target.Title = source.Title
	}

	if source.Cover != "" && (target.Cover == "" || coverPriority[source.Source] > coverPriority[target.CoverSource]) {
		target.Cover = source.Cover
		target.CoverSource = source.Source
	}

	if target.ReleaseDate == "" {
		target.ReleaseDate = source.ReleaseDate
	}

	if target.Year == 0 {
		target.Year = source.Year
	}

	if target.Description == "" {
		target.Description = source.Description
	}

	if target.Website == "" {
		target.Website = source.Website
	}

	if target.AgeRating == "" {
		target.AgeRating = source.AgeRating
	}

	if target.Rating == 0 {
		target.Rating = source.Rating
	}

	if target.AggregatedRating == 0 {
		target.AggregatedRating = source.AggregatedRating
	}

	if source.Completion != nil {
		if target.Completion == nil {
			target.Completion = source.Completion
		} else {
			if target.Completion.HastilyHours == 0 {
				target.Completion.HastilyHours = source.Completion.HastilyHours
			}

			if target.Completion.NormallyHours == 0 {
				target.Completion.NormallyHours = source.Completion.NormallyHours
			}

			if target.Completion.CompletelyHours == 0 {
				target.Completion.CompletelyHours = source.Completion.CompletelyHours
			}
		}
	}

	target.Aliases = appendUnique(target.Aliases, source.Aliases...)
	target.DLC = appendUnique(target.DLC, source.DLC...)
	target.Editions = appendUnique(target.Editions, source.Editions...)
	target.Screenshots = appendUnique(target.Screenshots, source.Screenshots...)
	target.Genres = appendUnique(target.Genres, source.Genres...)
	target.Modes = appendUnique(target.Modes, source.Modes...)
	target.Perspectives = appendUnique(target.Perspectives, source.Perspectives...)
	target.Themes = appendUnique(target.Themes, source.Themes...)
	target.Developers = appendUnique(target.Developers, source.Developers...)
	target.Publishers = appendUnique(target.Publishers, source.Publishers...)
	target.Platforms = appendUnique(target.Platforms, source.Platforms...)

	if len(target.PopularMods) == 0 && len(source.PopularMods) > 0 {
		target.PopularMods = source.PopularMods
	}

	if len(target.PopularCollections) == 0 && len(source.PopularCollections) > 0 {
		target.PopularCollections = source.PopularCollections
	}

	for _, download := range source.Downloads {
		target.Downloads = appendDownload(target.Downloads, download)
	}

	if source.Source != "" && source.SourceURL != "" {
		target.Sources = appendSource(target.Sources, GameSource{
			ID:         source.Source,
			Name:       sourceName(source.Source),
			URL:        source.SourceURL,
			ImportedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func appendUnique(target []string, values ...string) []string {
	for _, value := range values {
		if value == "" {
			continue
		}

		found := false

		for _, item := range target {
			if item == value {
				found = true

				break
			}
		}

		if !found {
			target = append(target, value)
		}
	}

	return target
}

func (e *Enricher) valid(ctx context.Context, target string, source string) bool {
	if !strings.HasPrefix(target, "https://") {
		return false
	}

	switch source {
	case "igdb":
		return strings.HasPrefix(target, "https://www.igdb.com/games/")
	case "rawg":
		return strings.HasPrefix(target, "https://rawg.io/games/")
	case "mobygames":
		return strings.HasPrefix(target, "https://www.mobygames.com/game/")
	case "old-games":
		return strings.HasPrefix(target, "https://www.old-games.ru/game/")
	case "giantbomb":
		return strings.HasPrefix(target, "https://www.giantbomb.com/")
	case "thegamesdb":
		return strings.HasPrefix(target, "https://thegamesdb.net/game.php")
	case "wikipedia":
		return strings.HasPrefix(target, "https://ru.wikipedia.org/wiki/")
	default:
		return true
	}
}

func sourceName(source string) string {
	switch source {
	case "mobygames":
		return "MobyGames"
	case "rawg":
		return "RAWG"
	case "igdb":
		return "IGDB"
	case "old-games":
		return "Old-Games.RU"
	case "giantbomb":
		return "Giant Bomb"
	case "thegamesdb":
		return "TheGamesDB"
	case "wikipedia":
		return "Wikipedia"
	case "longplay":
		return "Longplay (YouTube)"
	case "steam":
		return "Steam"
	case "vndb":
		return "VNDB"
	case "wikidata":
		return "Wikidata"
	case "abandonware":
		return "MyAbandonware"
	case "gog":
		return "GOG"
	case "nexusmods":
		return "Nexus Mods"
	case "gamebanana":
		return "GameBanana"
	default:
		return strings.ToUpper(source)
	}
}

func matchTitle(original string, matched string) bool {
	if matched == "" {
		return false
	}

	return titleScore(bondCanonical(original), bondCanonical(matched)) >= 0.8
}

func titleScore(left string, right string) float64 {
	a := titleTokens(left)
	b := titleTokens(right)

	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	if strings.Join(a, " ") == strings.Join(b, " ") {
		return 1
	}

	set := make(map[string]bool, len(a))
	for _, token := range a {
		set[token] = true
	}

	intersection := 0

	for _, token := range b {
		if set[token] {
			intersection++
		}
	}

	if intersection == len(a) || intersection == len(b) {
		if min(len(a), len(b)) >= 2 && isSubtitleMatch(left, right) {
			return 1
		}
	}

	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

func searchCandidates(title string) []string {
	result := []string{title}
	sanitized := searchTitle(title)

	if sanitized != title {
		result = append(result, sanitized)
	}

	tokens := titleTokens(title)

	for index := len(tokens) - 1; index >= 0; index-- {
		if len(tokens[index]) >= 4 {
			result = append(result, tokens[index])
		}
	}

	return appendUnique(result)
}

func isSubtitleMatch(left string, right string) bool {
	short := strings.ToLower(strings.Join(strings.Fields(left), " "))
	long := strings.ToLower(strings.Join(strings.Fields(right), " "))

	if len(short) > len(long) {
		short, long = long, short
	}

	if !strings.HasPrefix(long, short) {
		return false
	}

	rest := strings.TrimSpace(long[len(short):])

	if strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, "(") {
		return true
	}

	words := strings.Fields(strings.TrimLeft(rest, " :-–—()[]"))
	if len(words) == 0 {
		return false
	}

	for _, word := range words {
		if !editionWords[word] {
			return false
		}
	}

	return true
}

func titleTokens(value string) []string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}

		return ' '
	}, value)

	fields := strings.Fields(cleaned)
	result := make([]string, 0, len(fields))

	for _, field := range fields {
		switch field {
		case "the", "a", "an", "of", "and":
			continue
		default:
			result = append(result, field)
		}
	}

	return result
}

func ageRatingLabel(category int, rating int) string {
	if category == 1 {
		labels := map[int]string{1: "RP", 2: "EC", 3: "E", 4: "E10+", 5: "T", 6: "M", 7: "AO"}

		return labels[rating]
	}

	if category == 2 {
		labels := map[int]string{1: "PEGI 3", 2: "PEGI 7", 3: "PEGI 12", 4: "PEGI 16", 5: "PEGI 18"}

		return labels[rating]
	}

	return ""
}

func (e *Enricher) translateRu(ctx context.Context, text string) string {
	log.Printf("[translate] called len=%d", len(text))
	text = strings.TrimSpace(text)
	if text == "" || containsCyrillic(text) {
		return ""
	}

	if cached, ok := e.translations.Load(text); ok {
		return cached.(string)
	}

	if len(text) > 4500 {
		text = text[:4500]
	}

	endpoint := "https://clients5.google.com/translate_a/t?client=dict-chrome-ex&sl=auto&tl=ru&q=" + url.QueryEscape(text)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}

	request.Header.Set("User-Agent", "Mozilla/5.0")

	response, err := e.client.Do(request)
	if err != nil {
		log.Printf("[translate] request failed: %v", err)
		return ""
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		log.Printf("[translate] status %d", response.StatusCode)
		return ""
	}

	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		log.Printf("[translate] read failed: %v", readErr)
		return ""
	}

	var payload []json.RawMessage
	if json.Unmarshal(body, &payload) != nil || len(payload) == 0 {
		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300]
		}
		log.Printf("[translate] decode failed: %s", preview)
		return ""
	}

	var segments [][]json.RawMessage
	if json.Unmarshal(payload[0], &segments) != nil {
		log.Printf("[translate] payload parse failed: %s", string(payload[0])[:min(200, len(payload[0]))])
		return ""
	}

	var builder strings.Builder
	for _, segment := range segments {
		if len(segment) == 0 {
			continue
		}

		var value string
		if json.Unmarshal(segment[0], &value) == nil {
			builder.WriteString(value)
		}
	}

	result := strings.TrimSpace(builder.String())
	log.Printf("[translate] result len=%d", len(result))
	if result == "" {
		log.Printf("[translate] empty result")
	}
	if result != "" {
		e.translations.Store(text, result)
	}

	return result
}

func mergeEnrichment(game Game, enrichment Enrichment) Game {
	if enrichment.TitleRu != "" {
		game.TitleRu = enrichment.TitleRu
		game.SearchTags = appendUnique(game.SearchTags, strings.ToLower(enrichment.TitleRu))
	}

	if enrichment.Cover != "" {
		game.Cover = enrichment.Cover
	}

	if enrichment.ReleaseDate != "" {
		game.ReleaseDate = enrichment.ReleaseDate
	}

	if enrichment.Year > 0 {
		game.Year = enrichment.Year
	}

	if enrichment.Description != "" {
		game.Description = enrichment.Description
	}

	if enrichment.DescriptionRu != "" {
		game.DescriptionRu = enrichment.DescriptionRu
	}

	if len(enrichment.Screenshots) > 0 {
		game.Screenshots = enrichment.Screenshots
	}

	if len(enrichment.Genres) > 0 {
		game.Genres = enrichment.Genres
		game.GenresRU = translateGenres(enrichment.Genres)
	}

	if enrichment.AgeRating != "" {
		game.AgeRating = enrichment.AgeRating
	}

	if len(enrichment.Aliases) > 0 {
		game.Aliases = enrichment.Aliases
	}

	if len(enrichment.DLC) > 0 {
		game.DLC = appendUnique(game.DLC, enrichment.DLC...)
	}

	for _, edition := range enrichment.Editions {
		if strings.EqualFold(strings.TrimSpace(edition), strings.TrimSpace(game.Title)) {
			continue
		}

		game.Editions = appendUnique(game.Editions, edition)
	}

	if len(enrichment.Modes) > 0 {
		game.Modes = enrichment.Modes
	}

	if len(enrichment.Perspectives) > 0 {
		game.Perspectives = enrichment.Perspectives
	}

	if len(enrichment.Themes) > 0 {
		game.Themes = enrichment.Themes
	}

	if len(enrichment.Developers) > 0 {
		game.Developers = enrichment.Developers
	}

	if len(enrichment.Publishers) > 0 {
		game.Publishers = enrichment.Publishers
	}

	if len(enrichment.PopularMods) > 0 {
		game.PopularMods = enrichment.PopularMods
	}

	if len(enrichment.PopularCollections) > 0 {
		game.PopularCollections = enrichment.PopularCollections
	}

	if enrichment.Rating > 0 {
		game.Rating = enrichment.Rating
	}

	if enrichment.AggregatedRating > 0 {
		game.AggregatedRating = enrichment.AggregatedRating
	}

	if enrichment.Website != "" {
		game.Website = enrichment.Website
	}

	if enrichment.Completion != nil {
		game.Completion = enrichment.Completion
	}

	for _, download := range enrichment.Downloads {
		game.Downloads = appendDownload(game.Downloads, download)
	}

	for _, source := range enrichment.Sources {
		game.Sources = appendSource(game.Sources, source)
	}

	game.Sources = appendSource(game.Sources, GameSource{
		ID:         enrichment.Source,
		Name:       sourceName(enrichment.Source),
		URL:        enrichment.SourceURL,
		ImportedAt: time.Now().UTC().Format(time.RFC3339),
	})

	return game
}

func appendDownload(downloads []DownloadLink, download DownloadLink) []DownloadLink {
	for _, item := range downloads {
		if item.URL == download.URL {
			return downloads
		}
	}

	return append(downloads, download)
}

func appendSource(sources []GameSource, source GameSource) []GameSource {
	for _, item := range sources {
		if item.ID == source.ID && item.URL == source.URL {
			return sources
		}
	}

	return append(sources, source)
}

type igdbClient struct {
	client  *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func newIGDBClient(client *http.Client) *igdbClient {
	if strings.TrimSpace(os.Getenv("TWITCH_CLIENT_KEY")) == "" {
		return nil
	}

	return &igdbClient{client: client}
}

func (c *igdbClient) tokenValue(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expires.Add(-time.Minute)) {
		return c.token, nil
	}

	clientID := os.Getenv("TWITCH_CLIENT_KEY")
	secret := os.Getenv("TWITCH_SECRET_KEY")

	if clientID == "" || secret == "" {
		return "", errors.New("twitch credentials are missing")
	}

	endpoint := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/token?client_id=%s&client_secret=%s&grant_type=client_credentials",
		url.QueryEscape(clientID),
		url.QueryEscape(secret),
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}

	response, err := c.client.Do(request)
	if err != nil {
		return "", errors.New("twitch token request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}

	if payload.AccessToken == "" {
		return "", errors.New("empty twitch token")
	}

	c.token = payload.AccessToken
	c.expires = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)

	return c.token, nil
}

func (c *igdbClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	token, err := c.tokenValue(ctx)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(
		`search "%s"; fields name,first_release_date,cover.url,summary,url,rating,aggregated_rating,genres.name,platforms.name,screenshots.url,websites.url,involved_companies.company.name,involved_companies.developer,involved_companies.publisher,game_modes.name,player_perspectives.name,themes.name,alternative_names.name,dlcs.name,expansions.name,bundles.name,age_ratings.category,age_ratings.rating; limit 5;`,
		strings.ReplaceAll(title, `"`, ""),
	)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.igdb.com/v4/games",
		strings.NewReader(query),
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Client-ID", os.Getenv("TWITCH_CLIENT_KEY"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "text/plain")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("igdb: status %d", response.StatusCode)
	}

	var items []struct {
		ID               int     `json:"id"`
		Name             string  `json:"name"`
		FirstReleaseDate int64   `json:"first_release_date"`
		Summary          string  `json:"summary"`
		URL              string  `json:"url"`
		Rating           float64 `json:"rating"`
		AggregatedRating float64 `json:"aggregated_rating"`
		Cover            *struct {
			URL string `json:"url"`
		} `json:"cover"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		Developers []struct {
			Name string `json:"name"`
		} `json:"developers"`
		Publishers []struct {
			Name string `json:"name"`
		} `json:"publishers"`
		InvolvedCompanies []struct {
			Developer bool `json:"developer"`
			Publisher bool `json:"publisher"`
			Company   struct {
				Name string `json:"name"`
			} `json:"company"`
		} `json:"involved_companies"`
		GameModes []struct {
			Name string `json:"name"`
		} `json:"game_modes"`
		PlayerPerspectives []struct {
			Name string `json:"name"`
		} `json:"player_perspectives"`
		Themes []struct {
			Name string `json:"name"`
		} `json:"themes"`
		AlternativeNames []struct {
			Name string `json:"name"`
		} `json:"alternative_names"`
		DLCs []struct {
			Name string `json:"name"`
		} `json:"dlcs"`
		Expansions []struct {
			Name string `json:"name"`
		} `json:"expansions"`
		Bundles []struct {
			Name string `json:"name"`
		} `json:"bundles"`
		AgeRatings []struct {
			Category int `json:"category"`
			Rating   int `json:"rating"`
		} `json:"age_ratings"`
		Screenshots []struct {
			URL string `json:"url"`
		} `json:"screenshots"`
		Websites []struct {
			URL string `json:"url"`
		} `json:"websites"`
	}

	if err := json.NewDecoder(response.Body).Decode(&items); err != nil || len(items) == 0 {
		return nil, errors.New("igdb: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, item := range items {
		score := titleScore(title, item.Name)
		if score > bestScore {
			bestScore = score
			best = index
		}
	}

	game := items[best]
	enrichment := &Enrichment{
		Source:           "igdb",
		Title:            game.Name,
		SourceURL:        game.URL,
		Description:      game.Summary,
		Rating:           game.Rating,
		AggregatedRating: game.AggregatedRating,
	}

	if enrichment.SourceURL == "" {
		enrichment.SourceURL = fmt.Sprintf("https://www.igdb.com/games/%d", game.ID)
	}

	if game.Cover != nil {
		enrichment.Cover = imageURL(game.Cover.URL)
	}

	if game.FirstReleaseDate > 0 {
		date := time.Unix(game.FirstReleaseDate, 0).UTC()
		enrichment.ReleaseDate = date.Format("2006-01-02")
		enrichment.Year = date.Year()
	}

	for _, item := range game.Genres {
		enrichment.Genres = append(enrichment.Genres, item.Name)
	}

	for _, item := range game.Developers {
		enrichment.Developers = append(enrichment.Developers, item.Name)
	}

	for _, item := range game.Publishers {
		enrichment.Publishers = append(enrichment.Publishers, item.Name)
	}

	for _, item := range game.InvolvedCompanies {
		if item.Developer {
			enrichment.Developers = append(enrichment.Developers, item.Company.Name)
		}

		if item.Publisher {
			enrichment.Publishers = append(enrichment.Publishers, item.Company.Name)
		}
	}

	for _, item := range game.GameModes {
		enrichment.Modes = append(enrichment.Modes, item.Name)
	}

	for _, item := range game.PlayerPerspectives {
		enrichment.Perspectives = append(enrichment.Perspectives, item.Name)
	}

	for _, item := range game.Themes {
		enrichment.Themes = append(enrichment.Themes, item.Name)
	}

	for _, item := range game.AlternativeNames {
		enrichment.Aliases = append(enrichment.Aliases, item.Name)
	}

	for _, item := range game.DLCs {
		enrichment.DLC = append(enrichment.DLC, item.Name)
	}

	for _, item := range game.Expansions {
		enrichment.Editions = append(enrichment.Editions, item.Name)
	}

	for _, item := range game.Bundles {
		enrichment.Editions = append(enrichment.Editions, item.Name)
	}

	for _, item := range game.AgeRatings {
		if value := ageRatingLabel(item.Category, item.Rating); value != "" {
			enrichment.AgeRating = value

			if item.Category == 1 {
				break
			}
		}
	}

	for _, item := range game.Screenshots {
		enrichment.Screenshots = append(enrichment.Screenshots, imageURL(item.URL))
	}

	if len(game.Websites) > 0 {
		enrichment.Website = game.Websites[0].URL
	}

	if completion, err := c.completion(ctx, token, game.ID); err == nil {
		enrichment.Completion = completion
	}

	return enrichment, nil
}

func (c *igdbClient) completion(ctx context.Context, token string, gameID int) (*Completion, error) {
	query := fmt.Sprintf("fields hastily,normally,completely,count; where game_id = %d; limit 1;", gameID)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.igdb.com/v4/game_time_to_beats",
		strings.NewReader(query),
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Client-ID", os.Getenv("TWITCH_CLIENT_KEY"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "text/plain")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("igdb time_to_beat: status %d", response.StatusCode)
	}

	var items []struct {
		Hastily    float64 `json:"hastily"`
		Normally   float64 `json:"normally"`
		Completely float64 `json:"completely"`
		Count      int     `json:"count"`
	}

	if err := json.NewDecoder(response.Body).Decode(&items); err != nil || len(items) == 0 {
		return nil, errors.New("igdb time_to_beat: not found")
	}

	item := items[0]
	if item.Hastily == 0 && item.Normally == 0 && item.Completely == 0 {
		return nil, errors.New("igdb time_to_beat: empty")
	}

	toHours := func(seconds float64) float64 {
		return math.Round(seconds/36) / 100
	}

	return &Completion{
		HastilyHours:    toHours(item.Hastily),
		NormallyHours:   toHours(item.Normally),
		CompletelyHours: toHours(item.Completely),
	}, nil
}

func imageURL(raw string) string {
	if raw == "" {
		return ""
	}

	raw = strings.Replace(raw, "t_thumb", "t_cover_big", 1)

	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}

	return raw
}

type oldgamesClient struct {
	client *http.Client
}

func newOldgamesClient(client *http.Client) *oldgamesClient {
	return &oldgamesClient{client: client}
}

var oldgamesLinkPattern = regexp.MustCompile(`href="/game/(\d+)\.html"\s+title="([^"]*)"`)
var oldgamesPageTitlePattern = regexp.MustCompile(`(?s)<title>(.*?)</title>`)

var oldgamesKnownPages = map[string]string{
	"%$ьаны пилоты, бомбы, самолёты!": "https://www.old-games.ru/game/10181.html",
}
var oldgamesTitlePattern = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
var oldgamesImagePattern = regexp.MustCompile(`<meta[^>]+property="og:image"[^>]+content="([^"]+)"`)
var oldgamesDescriptionPattern = regexp.MustCompile(`<meta[^>]+name="description"[^>]+content="([^"]+)"`)
var oldgamesScreenshotPattern = regexp.MustCompile(`(?:src|href)="(/games/[^"]+/screenshots/[^"]+\.(?:png|jpe?g|gif))"`)
var oldgamesDownloadPattern = regexp.MustCompile(`href="(/game/download/\d+\.html)"`)

func (c *oldgamesClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	pageURL := oldgamesKnownPages[title]

	if pageURL == "" {
		searchURL := "https://www.old-games.ru/search/?q=" + url.QueryEscape(title)

		body, err := c.get(ctx, searchURL)
		if err != nil {
			return nil, err
		}

		matches := oldgamesLinkPattern.FindAllSubmatch(body, -1)
		if len(matches) == 0 {
			return nil, errors.New("old-games: game not found")
		}

		bestID := ""
		bestScore := 0.0

		for _, match := range matches {
			id := string(match[1])
			name := strings.TrimSpace(string(match[2]))
			score := titleScore(title, name)

			if score > bestScore {
				bestScore = score
				bestID = id
			}
		}

		if bestID == "" || bestScore < 0.8 {
			return nil, errors.New("old-games: no matching page")
		}

		pageURL = "https://www.old-games.ru/game/" + bestID + ".html"
	}

	page, err := c.get(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	enrichment := &Enrichment{
		Source:    "old-games",
		SourceURL: pageURL,
	}

	if match := oldgamesTitlePattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Title = stripHTML(string(match[1]))
	}

	if match := oldgamesImagePattern.FindSubmatch(page); len(match) > 1 {
		image := strings.TrimSpace(string(match[1]))

		if !strings.Contains(image, "nocover") {
			enrichment.Cover = absoluteURL(image)
		}
	}

	if match := oldgamesDescriptionPattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Description = stripHTML(string(match[1]))
	}

	for _, match := range oldgamesScreenshotPattern.FindAllSubmatch(page, -1) {
		enrichment.Screenshots = append(enrichment.Screenshots, absoluteURL(string(match[1])))
	}

	if enrichment.Cover == "" && len(enrichment.Screenshots) > 0 {
		enrichment.Cover = enrichment.Screenshots[0]
	}

	if match := oldgamesDownloadPattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Downloads = append(enrichment.Downloads, DownloadLink{
			Name:     "Old-Games.RU",
			URL:      "https://www.old-games.ru" + string(match[1]),
			Language: "RU",
			Note:     "Скачать",
		})
	}

	enrichment.Year = extractYear(enrichment.Title)

	if enrichment.Year == 0 {
		if match := oldgamesPageTitlePattern.FindSubmatch(page); len(match) > 1 {
			enrichment.Year = extractYear(stripHTML(string(match[1])))
		}
	}

	return enrichment, nil
}

func (c *oldgamesClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")
	request.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("old-games: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("old-games: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func stripHTML(value string) string {
	return strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(value, ""))
}

func absoluteURL(value string) string {
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}

	if strings.HasPrefix(value, "/") {
		return "https://www.old-games.ru" + value
	}

	return value
}

type mobyClient struct {
	client *http.Client
	key    string
}

func newMobyClient(client *http.Client) *mobyClient {
	key := strings.TrimSpace(os.Getenv("MOBYGAMES_API_KEY"))
	if key == "" {
		return nil
	}

	return &mobyClient{client: client, key: key}
}

func (c *mobyClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	endpoint := fmt.Sprintf(
		"https://api.mobygames.com/v1/games?title=%s&api_key=%s",
		url.QueryEscape(title),
		url.QueryEscape(c.key),
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mobygames api: status %d", response.StatusCode)
	}

	var payload struct {
		Games []struct {
			ID          int    `json:"game_id"`
			Title       string `json:"title"`
			MobyURL     string `json:"moby_url"`
			Description string `json:"description"`
			Genres      []struct {
				GenreName string `json:"genre_name"`
			} `json:"genres"`
			Platforms []struct {
				PlatformName string `json:"platform_name"`
			} `json:"platforms"`
			SampleCover *struct {
				ImageURL string `json:"image_url"`
			} `json:"sample_cover"`
			SampleScreenshots []struct {
				ImageURL string `json:"image_url"`
			} `json:"sample_screenshots"`
		} `json:"games"`
	}

	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil || len(payload.Games) == 0 {
		return nil, errors.New("mobygames api: game not found")
	}

	game := payload.Games[0]
	enrichment := &Enrichment{
		Source:      "mobygames",
		Title:       game.Title,
		SourceURL:   firstNonEmpty(game.MobyURL, fmt.Sprintf("https://www.mobygames.com/game/%d", game.ID)),
		Description: game.Description,
	}

	if game.SampleCover != nil {
		enrichment.Cover = game.SampleCover.ImageURL
	}

	for _, item := range game.Genres {
		enrichment.Genres = append(enrichment.Genres, item.GenreName)
	}

	for _, item := range game.Platforms {
		enrichment.Platforms = append(enrichment.Platforms, item.PlatformName)
	}

	for _, item := range game.SampleScreenshots {
		enrichment.Screenshots = append(enrichment.Screenshots, item.ImageURL)
	}

	return enrichment, nil
}

type rawgClient struct {
	client *http.Client
	key    string
}

func rawgHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true

	if proxy := strings.TrimSpace(os.Getenv("RAWG_PROXY_URL")); proxy != "" {
		if parsed, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}

	return &http.Client{
		Timeout:   25 * time.Second,
		Transport: transport,
	}
}

var rawgGate struct {
	sync.Mutex
	last time.Time
}

func waitRAWG() {
	rawgGate.Lock()
	defer rawgGate.Unlock()

	if elapsed := time.Since(rawgGate.last); elapsed < 400*time.Millisecond {
		time.Sleep(400*time.Millisecond - elapsed)
	}

	rawgGate.last = time.Now()
}

func newRAWGClient(client *http.Client) *rawgClient {
	key := strings.TrimSpace(os.Getenv("RAWG_API_KEY"))
	if key == "" {
		return nil
	}

	return &rawgClient{client: rawgHTTPClient(), key: key}
}

func (c *rawgClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	endpoint := fmt.Sprintf(
		"https://api.rawg.io/api/games?key=%s&search=%s&page_size=1",
		url.QueryEscape(c.key),
		url.QueryEscape(title),
	)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Results []struct {
			ID              int     `json:"id"`
			Slug            string  `json:"slug"`
			Name            string  `json:"name"`
			Released        string  `json:"released"`
			BackgroundImage string  `json:"background_image"`
			Rating          float64 `json:"rating"`
			Metacritic      *int    `json:"metacritic"`
			Website         string  `json:"website"`
			Playtime        float64 `json:"playtime"`
			Genres          []struct {
				Name string `json:"name"`
			} `json:"genres"`
			Platforms []struct {
				Platform struct {
					Name string `json:"name"`
				} `json:"platform"`
			} `json:"platforms"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Results) == 0 {
		return nil, errors.New("rawg: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, item := range payload.Results {
		score := titleScore(title, item.Name)
		if score > bestScore {
			bestScore = score
			best = index
		}
	}

	game := payload.Results[best]
	enrichment := &Enrichment{
		Source:      "rawg",
		Title:       game.Name,
		SourceURL:   "https://rawg.io/games/" + game.Slug,
		Cover:       game.BackgroundImage,
		ReleaseDate: game.Released,
		Rating:      game.Rating,
		Website:     game.Website,
	}

	if game.Metacritic != nil {
		enrichment.AggregatedRating = float64(*game.Metacritic)
	}

	if game.Playtime > 0 {
		enrichment.Completion = &Completion{NormallyHours: game.Playtime}
	}

	if game.Released != "" {
		if date, err := time.Parse("2006-01-02", game.Released); err == nil {
			enrichment.Year = date.Year()
		}
	}

	for _, item := range game.Genres {
		enrichment.Genres = append(enrichment.Genres, item.Name)
	}

	for _, item := range game.Platforms {
		enrichment.Platforms = append(enrichment.Platforms, item.Platform.Name)
	}

	detailBody, err := c.get(ctx, fmt.Sprintf(
		"https://api.rawg.io/api/games/%d?key=%s",
		game.ID,
		url.QueryEscape(c.key),
	))
	if err == nil {
		var detail struct {
			DescriptionRaw string `json:"description_raw"`
			Website        string `json:"website"`
			EsrbRating     *struct {
				Name string `json:"name"`
			} `json:"esrb_rating"`
			Developers []struct {
				Name string `json:"name"`
			} `json:"developers"`
			Publishers []struct {
				Name string `json:"name"`
			} `json:"publishers"`
		}

		if json.Unmarshal(detailBody, &detail) == nil {
			enrichment.Description = detail.DescriptionRaw
			enrichment.Website = firstNonEmpty(detail.Website, enrichment.Website)

			if detail.EsrbRating != nil {
				enrichment.AgeRating = detail.EsrbRating.Name
			}

			for _, item := range detail.Developers {
				enrichment.Developers = append(enrichment.Developers, item.Name)
			}

			for _, item := range detail.Publishers {
				enrichment.Publishers = append(enrichment.Publishers, item.Name)
			}
		}
	}

	screenshotsBody, err := c.get(ctx, fmt.Sprintf(
		"https://api.rawg.io/api/games/%d/screenshots?key=%s",
		game.ID,
		url.QueryEscape(c.key),
	))
	if err == nil {
		var shots struct {
			Results []struct {
				Image string `json:"image"`
			} `json:"results"`
		}

		if json.Unmarshal(screenshotsBody, &shots) == nil {
			for _, item := range shots.Results {
				enrichment.Screenshots = append(enrichment.Screenshots, item.Image)
			}
		}
	}

	return enrichment, nil
}

func (c *rawgClient) get(ctx context.Context, target string) ([]byte, error) {
	if err := rawgQuotaBegin(); err != nil {
		return nil, err
	}

	waitRAWG()

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("rawg: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	rawgQuotaObserve(response.StatusCode, body)

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rawg: status %d", response.StatusCode)
	}

	if readErr != nil {
		return nil, errors.New("rawg: read failed")
	}

	return body, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

var mobyGamePattern = regexp.MustCompile(`href="(/game/[^"#?]+)"`)
var mobyImagePattern = regexp.MustCompile(`<meta[^>]+property="og:image"[^>]+content="([^"]+)"`)
var mobyDescriptionPattern = regexp.MustCompile(`<meta[^>]+name="description"[^>]+content="([^"]+)"`)
var mobyTitlePattern = regexp.MustCompile(`<title>([^<]+)</title>`)
var mobyJSONPattern = regexp.MustCompile(`(?s)<script[^>]+type="application/ld\+json"[^>]*>(.*?)</script>`)

func (e *Enricher) fetchMoby(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := "https://www.mobygames.com/search/?q=" + url.QueryEscape(title)

	searchBody, err := e.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	match := mobyGamePattern.FindSubmatch(searchBody)
	if len(match) < 2 {
		return nil, errors.New("mobygames: game not found")
	}

	gameURL := "https://www.mobygames.com" + string(match[1])
	body, err := e.get(ctx, gameURL)
	if err != nil {
		return nil, err
	}

	enrichment := parseMobyHTML(body, gameURL)
	if enrichment == nil {
		return nil, errors.New("mobygames: empty page")
	}

	return enrichment, nil
}

func parseMobyHTML(body []byte, gameURL string) *Enrichment {
	enrichment := &Enrichment{Source: "mobygames", SourceURL: gameURL}

	if jsonMatch := mobyJSONPattern.FindSubmatch(body); len(jsonMatch) > 1 {
		var data struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Image         string `json:"image"`
			DatePublished string `json:"datePublished"`
			Genre         any    `json:"genre"`
			Developer     any    `json:"author"`
			Publisher     any    `json:"publisher"`
			URL           string `json:"url"`
		}

		if json.Unmarshal(jsonMatch[1], &data) == nil {
			enrichment.Title = data.Name
			enrichment.Description = data.Description
			enrichment.Cover = data.Image
			enrichment.ReleaseDate = data.DatePublished
			enrichment.Website = data.URL
			enrichment.Genres = stringList(data.Genre)
			enrichment.Developers = stringList(data.Developer)
			enrichment.Publishers = stringList(data.Publisher)
		}
	}

	if enrichment.Cover == "" {
		if match := mobyImagePattern.FindSubmatch(body); len(match) > 1 {
			enrichment.Cover = string(match[1])
		}
	}

	if enrichment.Description == "" {
		if match := mobyDescriptionPattern.FindSubmatch(body); len(match) > 1 {
			enrichment.Description = string(match[1])
		}
	}

	if enrichment.Title == "" {
		if match := mobyTitlePattern.FindSubmatch(body); len(match) > 1 {
			enrichment.Title = strings.TrimSuffix(strings.TrimSpace(string(match[1])), " - MobyGames")
		}
	}

	if enrichment.ReleaseDate != "" {
		if date, err := time.Parse("2006-01-02", enrichment.ReleaseDate); err == nil {
			enrichment.Year = date.Year()
		}
	}

	return enrichment
}

func (e *Enricher) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

	response, err := e.client.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mobygames: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}

		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))

		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}

		return result
	default:
		return nil
	}
}
