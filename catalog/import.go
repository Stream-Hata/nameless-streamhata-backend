package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"time"
)

var externalPlatformAliases = map[string]string{
	"pc":                                  "windows",
	"pc (microsoft windows)":              "windows",
	"microsoft windows":                   "windows",
	"linux":                               "windows",
	"macos":                               "mac-os",
	"mac":                                 "mac-os",
	"playstation":                         "ps1",
	"playstation 1":                       "ps1",
	"playstation 2":                       "ps2",
	"playstation 3":                       "ps3",
	"playstation 4":                       "ps4",
	"playstation 5":                       "ps5",
	"psp":                                 "psp",
	"ps vita":                             "vita",
	"playstation vita":                    "vita",
	"xbox":                                "xbox",
	"xbox 360":                            "xbox-360",
	"xbox one":                            "xbox-one",
	"xbox series x":                       "xbox-series-x",
	"xbox series s":                       "xbox-series-s",
	"xbox series x|s":                     "xbox-series-x",
	"nintendo switch":                     "switch",
	"nintendo switch 2":                   "switch-2",
	"wii":                                 "wii",
	"wii u":                               "wii-u",
	"nintendo 64":                         "n64",
	"super nintendo":                      "snes",
	"super nintendo entertainment system": "snes",
	"nintendo entertainment system":       "nes",
	"nes":                                 "nes",
	"famicom":                             "famicom",
	"game boy":                            "game-boy",
	"game boy color":                      "gbc",
	"game boy advance":                    "gba",
	"nintendo ds":                         "ds",
	"nintendo 3ds":                        "3ds",
	"gamecube":                            "gamecube",
	"nintendo gamecube":                   "gamecube",
	"sega genesis":                        "genesis",
	"sega mega drive/genesis":             "genesis",
	"mega drive":                          "genesis",
	"sega master system":                  "master-system",
	"sega game gear":                      "game-gear",
	"game gear":                           "game-gear",
	"sega saturn":                         "saturn",
	"sega dreamcast":                      "dreamcast",
	"dreamcast":                           "dreamcast",
	"sega cd":                             "sega-cd",
	"sega 32x":                            "32x",
	"atari 2600":                          "atari-2600",
	"atari 5200":                          "atari-5200",
	"atari 7800":                          "atari-7800",
	"atari lynx":                          "lynx",
	"atari jaguar":                        "jaguar",
	"atari st":                            "atari-st",
	"commodore 64":                        "c64",
	"amiga":                               "amiga",
	"amstrad cpc":                         "amstrad-cpc",
	"zx spectrum":                         "zx-spectrum",
	"msx":                                 "msx",
	"dos":                                 "dos",
	"3do":                                 "3do",
	"neo geo":                             "neo-geo-aes",
	"neo geo pocket":                      "neo-geo-pocket",
	"wonderswan":                          "wonderswan",
	"turbografx-16":                       "turbografx-16",
	"pc engine":                           "pc-engine",
	"sharp x68000":                        "x68000",
	"android":                             "windows",
	"ios":                                 "ios",
}

func externalPlatform(name string) (Platform, bool) {
	key := strings.ToLower(strings.TrimSpace(name))

	if code, ok := externalPlatformAliases[key]; ok {
		platform, found := platformByCode(code)

		return platform, found
	}

	for alias, code := range externalPlatformAliases {
		if strings.Contains(key, alias) {
			platform, found := platformByCode(code)

			return platform, found
		}
	}

	return Platform{}, false
}

func (s *Service) importLoop() {
	if strings.TrimSpace(os.Getenv("CATALOG_IMPORT")) == "0" {
		return
	}

	interval := 10 * time.Minute

	if value := strings.TrimSpace(os.Getenv("CATALOG_IMPORT_INTERVAL")); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			interval = parsed
		}
	}

	s.backgroundImport()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		s.backgroundImport()
	}
}

func (s *Service) persistImported(games []Game) {
	if s.db == nil || len(games) == 0 {
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		return
	}

	statement, err := tx.Prepare(`
		INSERT INTO catalog_game (slug, title, source, payload, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (slug) DO NOTHING
	`)
	if err != nil {
		_ = tx.Rollback()

		return
	}

	for _, game := range games {
		raw, err := json.Marshal(game)
		if err != nil {
			continue
		}

		source := ""

		if len(game.Sources) > 0 {
			source = game.Sources[0].ID
		}

		_, _ = statement.Exec(game.Slug, game.Title, source, string(raw))
	}

	_ = statement.Close()
	_ = tx.Commit()
}

type importCursor struct {
	Year int `json:"year"`
	Page int `json:"page"`
}

func (s *Service) loadCursor() importCursor {
	cursor := importCursor{Year: 1950, Page: 1}

	if s.db == nil {
		return cursor
	}

	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = 'catalog_import_cursor'`,
	).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &cursor)
	}

	if cursor.Year < 1950 {
		cursor.Year = 1950
	}

	if cursor.Page < 1 {
		cursor.Page = 1
	}

	return cursor
}

func (s *Service) saveCursor(cursor importCursor) {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(cursor)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('catalog_import_cursor', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		string(raw),
	)
}

type igdbCursor struct {
	Offset int  `json:"offset"`
	Done   bool `json:"done"`
}

func (s *Service) loadIGDBCursor() igdbCursor {
	cursor := igdbCursor{}

	if s.db == nil {
		return cursor
	}

	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = 'catalog_import_igdb_cursor'`,
	).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &cursor)
	}

	if cursor.Offset < 0 {
		cursor.Offset = 0
	}

	return cursor
}

func (s *Service) saveIGDBCursor(cursor igdbCursor) {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(cursor)
	if err != nil {
		return
	}

	_, _ = s.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('catalog_import_igdb_cursor', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		string(raw),
	)
}

func (s *Service) backgroundImportIGDB(ctx context.Context, batch int) (ImportResult, error) {
	cursor := s.loadIGDBCursor()

	result, err := s.Import(ctx, ImportOptions{
		Source:      "igdb",
		Limit:       batch,
		Order:       "popular",
		OnlyMissing: true,
		Offset:      cursor.Offset,
		UseOffset:   true,
	})
	if err != nil {
		return result, err
	}

	if result.Done || result.Imported == 0 {
		s.saveIGDBCursor(igdbCursor{Offset: 0, Done: true})
	} else {
		s.saveIGDBCursor(igdbCursor{Offset: result.NextOffset})
	}

	return result, nil
}

func (s *Service) backgroundImportRAWG(ctx context.Context, options ImportOptions) (ImportResult, error) {
	key := strings.TrimSpace(os.Getenv("RAWG_API_KEY"))
	if key == "" {
		return ImportResult{}, errors.New("catalog: RAWG_API_KEY is empty")
	}

	if rawgQuotaStatus().paused() {
		return ImportResult{Total: s.Total()}, errors.New("rawg: quota paused")
	}

	cursor := s.loadCursor()

	if year := rawgQuotaTakeRewind(); year > 0 {
		cursor = importCursor{Year: year, Page: 1}
		s.saveCursor(cursor)
		log.Printf("[import-loop] rawg: quota reset, rewinding cursor to %d", year)
	}

	added := 0
	skipped := 0
	pageSize := 40
	currentYear := time.Now().Year()

	for ctx.Err() == nil {
		if cursor.Year > currentYear {
			cursor = importCursor{Year: 1950, Page: 1}
			s.saveCursor(cursor)
		}

		endpoint := fmt.Sprintf(
			"https://api.rawg.io/api/games?key=%s&dates=%d-01-01,%d-12-31&page=%d&page_size=%d&ordering=-added",
			url.QueryEscape(key),
			cursor.Year,
			cursor.Year,
			cursor.Page,
			pageSize,
		)

		var body []byte
		var err error

		for attempt := 0; attempt < 3; attempt++ {
			waitRAWG()

			body, err = s.fetchJSON(ctx, endpoint)
			if err == nil {
				break
			}

			log.Printf("[import-loop] rawg %d page %d attempt %d: %v", cursor.Year, cursor.Page, attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}

		if err != nil {
			if rawgQuotaStatus().paused() || strings.Contains(err.Error(), "limit reached") || strings.Contains(err.Error(), "quota") {
				log.Printf("[import-loop] rawg paused: %v", err)

				return ImportResult{Total: s.Total()}, err
			}

			log.Printf("[import-loop] rawg %d page %d failed, skipping year: %v", cursor.Year, cursor.Page, err)
			cursor.Year++
			cursor.Page = 1
			s.saveCursor(cursor)
			time.Sleep(2 * time.Second)

			continue
		}

		var payload struct {
			Count   int `json:"count"`
			Results []struct {
				Slug            string  `json:"slug"`
				Name            string  `json:"name"`
				Released        string  `json:"released"`
				BackgroundImage string  `json:"background_image"`
				Rating          float64 `json:"rating"`
				Metacritic      *int    `json:"metacritic"`
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

		if err := json.Unmarshal(body, &payload); err != nil {
			return ImportResult{Imported: added, Skipped: skipped, Total: s.Total()}, err
		}

		if len(payload.Results) == 0 {
			cursor.Year++
			cursor.Page = 1
			s.saveCursor(cursor)

			continue
		}

		pageNew := make([]Game, 0, len(payload.Results))

		for _, item := range payload.Results {
			game, ok := s.gameFromRAWG(item)
			if !ok {
				continue
			}

			if !s.isKnown(game.Title, game.Slug) {
				pageNew = append(pageNew, game)
			}
		}

		pageAdded, pageSkipped := s.mergeImported(pageNew)

		added += pageAdded
		skipped += pageSkipped

		if pageAdded > 0 {
			log.Printf("[import-loop] rawg %d page %d: +%d (total %d)", cursor.Year, cursor.Page, pageAdded, s.Total())
		}

		cursor.Page++
		s.saveCursor(cursor)

		totalPages := (payload.Count + pageSize - 1) / pageSize

		if cursor.Page > totalPages {
			cursor.Year++
			cursor.Page = 1
			s.saveCursor(cursor)
		}

		if added >= options.Limit {
			return ImportResult{Imported: added, Skipped: skipped, Total: s.Total()}, nil
		}
	}

	return ImportResult{Imported: added, Skipped: skipped, Total: s.Total()}, ctx.Err()
}

func (s *Service) ImportByQuery(ctx context.Context, query string) (ImportResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ImportResult{}, errors.New("catalog: empty query")
	}

	key := strings.TrimSpace(os.Getenv("RAWG_API_KEY"))
	if key == "" {
		return ImportResult{}, errors.New("catalog: RAWG_API_KEY is empty")
	}

	waitRAWG()

	endpoint := fmt.Sprintf(
		"https://api.rawg.io/api/games?key=%s&search=%s&page_size=20",
		url.QueryEscape(key),
		url.QueryEscape(query),
	)

	var body []byte

	var err error

	for attempt := 0; attempt < 3; attempt++ {
		waitRAWG()

		body, err = s.fetchJSON(ctx, endpoint)
		if err == nil {
			break
		}

		time.Sleep(time.Duration(attempt+1) * time.Second)
	}

	if err != nil {
		return ImportResult{}, err
	}

	var payload struct {
		Results []struct {
			Slug            string  `json:"slug"`
			Name            string  `json:"name"`
			Released        string  `json:"released"`
			BackgroundImage string  `json:"background_image"`
			Rating          float64 `json:"rating"`
			Metacritic      *int    `json:"metacritic"`
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

	if err := json.Unmarshal(body, &payload); err != nil {
		return ImportResult{}, err
	}

	imported := make([]Game, 0, len(payload.Results))

	for _, item := range payload.Results {
		game, ok := s.gameFromRAWG(item)
		if !ok {
			continue
		}

		if s.isKnown(game.Title, game.Slug) {
			continue
		}

		imported = append(imported, game)
	}

	added, skipped := s.mergeImported(imported)

	return ImportResult{Imported: added, Skipped: skipped, Total: s.Total(), Items: imported}, nil
}

func (s *Service) ImportSteamByQuery(ctx context.Context, query string) (ImportResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ImportResult{}, errors.New("catalog: empty query")
	}

	if s.enricher == nil || s.enricher.steam == nil {
		return ImportResult{}, errors.New("catalog: Steam client is not configured")
	}

	meta, err := s.enricher.steam.Search(ctx, query)
	if err != nil {
		return ImportResult{}, err
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" {
		return ImportResult{}, errors.New("steam: empty title")
	}

	platform := Platform{Code: "steam", Name: "Steam", Icon: "steam.svg", Maintainer: "Valve"}
	slug := slugify(title) + "-steam"
	year := meta.Year

	game := Game{
		ID:          shortID(slug),
		Slug:        slug,
		Title:       title,
		TitleRu:     meta.TitleRu,
		Year:        year,
		ReleaseDate: meta.ReleaseDate,
		Platforms:   []Platform{platform},
		Description: meta.Description,
		Cover:       meta.Cover,
		Genres:      meta.Genres,
		Developers:  meta.Developers,
		Publishers:  meta.Publishers,
		Screenshots: meta.Screenshots,
		Website:     meta.Website,
		Rating:      meta.Rating,
		Tier:        tierFor(title, platform, year),
		Age:         ageFor(year, platform),
		Status:      "seed",
		Sources: []GameSource{{
			ID:         "steam",
			Name:       "Steam",
			URL:        meta.SourceURL,
			ImportedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	added, skipped := s.mergeImported([]Game{game})

	return ImportResult{Imported: added, Skipped: skipped, Total: s.Total(), Items: []Game{game}}, nil
}

func (s *Service) ImportIGDBByQuery(ctx context.Context, query string) (ImportResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ImportResult{}, errors.New("catalog: empty query")
	}

	if s.enricher == nil || s.enricher.igdb == nil {
		return ImportResult{}, errors.New("catalog: IGDB client is not configured")
	}

	token, err := s.enricher.igdb.tokenValue(ctx)
	if err != nil {
		return ImportResult{}, err
	}

	safe := strings.NewReplacer(`"`, " ", `\`, " ").Replace(query)
	body := fmt.Sprintf(
		`search "%s"; fields name,first_release_date,cover.url,summary,url,rating,aggregated_rating,genres.name,platforms.name,involved_companies.company.name,involved_companies.developer,involved_companies.publisher,game_modes.name,player_perspectives.name,themes.name,alternative_names.name,dlcs.name,expansions.name,bundles.name,age_ratings.category,age_ratings.rating; limit 20;`,
		safe,
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.igdb.com/v4/games", strings.NewReader(body))
	if err != nil {
		return ImportResult{}, err
	}

	request.Header.Set("Client-ID", os.Getenv("TWITCH_CLIENT_KEY"))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "text/plain")

	response, err := s.enricher.client.Do(request)
	if err != nil {
		return ImportResult{}, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return ImportResult{}, fmt.Errorf("igdb search: status %d", response.StatusCode)
	}

	var items []igdbImportItem

	if err := json.NewDecoder(response.Body).Decode(&items); err != nil {
		return ImportResult{}, err
	}

	imported := make([]Game, 0, len(items))

	for _, item := range items {
		game, ok := s.gameFromIGDB(item)
		if ok && !s.isKnown(game.Title, game.Slug) {
			imported = append(imported, game)
		}
	}

	added, skipped := s.mergeImported(imported)

	return ImportResult{Imported: added, Skipped: skipped, Total: s.Total(), Items: imported}, nil
}

func (s *Service) backgroundImport() {
	batch := 20000

	if value := strings.TrimSpace(os.Getenv("CATALOG_IMPORT_BATCH")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			batch = parsed
		}
	}

	sources := strings.Split(strings.TrimSpace(os.Getenv("CATALOG_IMPORT_SOURCES")), ",")
	if len(sources) == 1 && sources[0] == "" {
		sources = []string{"rawg", "igdb"}
	}

	for _, source := range sources {
		source = strings.TrimSpace(source)

		if source == "" {
			continue
		}

		order := "new"

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)

		var result ImportResult
		var err error

		if source == "rawg" {
			result, err = s.backgroundImportRAWG(ctx, ImportOptions{Limit: batch, OnlyMissing: true})
		} else {
			result, err = s.Import(ctx, ImportOptions{
				Source:      source,
				Limit:       batch,
				Order:       order,
				OnlyMissing: true,
			})
		}
		cancel()

		if err != nil {
			log.Printf("[import-loop] %s: %v", source, err)

			continue
		}

		log.Printf("[import-loop] %s: +%d (skipped %d, total %d)", source, result.Imported, result.Skipped, result.Total)

		if result.Imported == 0 {
			time.Sleep(2 * time.Second)
		}
	}
}

type ImportOptions struct {
	Source      string
	Limit       int
	MinRating   float64
	Order       string
	YearFrom    int
	YearTo      int
	OnlyMissing bool
	Offset      int
	UseOffset   bool
}

type ImportResult struct {
	Imported   int    `json:"imported"`
	Skipped    int    `json:"skipped"`
	Total      int    `json:"total"`
	Running    bool   `json:"running"`
	Items      []Game `json:"items,omitempty"`
	NextOffset int    `json:"nextOffset,omitempty"`
	Done       bool   `json:"done,omitempty"`
}

func (s *Service) Import(ctx context.Context, options ImportOptions) (ImportResult, error) {
	if !s.importing.CompareAndSwap(false, true) {
		return ImportResult{Total: s.Total(), Running: true}, nil
	}

	defer s.importing.Store(false)

	if options.Limit <= 0 {
		options.Limit = 20000
	}

	if options.Limit > 50000 {
		options.Limit = 50000
	}

	switch options.Source {
	case "rawg":
		return s.importRAWG(ctx, options)
	case "igdb":
		return s.importIGDB(ctx, options)
	default:
		return ImportResult{}, fmt.Errorf("catalog: unknown import source %q", options.Source)
	}
}

func rawgOrder(order string) string {
	switch order {
	case "new":
		return "-released"
	case "popular":
		return "-added"
	default:
		return "-rating"
	}
}

func (s *Service) importRAWG(ctx context.Context, options ImportOptions) (ImportResult, error) {
	key := strings.TrimSpace(os.Getenv("RAWG_API_KEY"))
	if key == "" {
		return ImportResult{}, errors.New("catalog: RAWG_API_KEY is empty")
	}

	limit := options.Limit
	imported := make([]Game, 0, limit)
	page := 1
	pageSize := 40
	var err error
	added := 0
	skipped := 0

	if os.Getenv("CATALOG_IMPORT_RANDOM") != "0" {
		page = 1 + rand.IntN(500)
	}

	for len(imported) < limit {
		endpoint := fmt.Sprintf(
			"https://api.rawg.io/api/games?key=%s&page=%d&page_size=%d&ordering=%s",
			url.QueryEscape(key),
			page,
			pageSize,
			rawgOrder(options.Order),
		)

		var body []byte

		for attempt := 0; attempt < 3; attempt++ {
			waitRAWG()

			body, err = s.fetchJSON(ctx, endpoint)
			if err == nil {
				break
			}

			log.Printf("[import] rawg page %d attempt %d: %v", page, attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}

		if err != nil {
			break
		}

		log.Printf("[import] rawg page %d: %d bytes", page, len(body))

		var payload struct {
			Results []struct {
				Slug            string  `json:"slug"`
				Name            string  `json:"name"`
				Released        string  `json:"released"`
				BackgroundImage string  `json:"background_image"`
				Rating          float64 `json:"rating"`
				Metacritic      *int    `json:"metacritic"`
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
			break
		}

		pageNew := make([]Game, 0, len(payload.Results))

		for _, item := range payload.Results {
			if len(imported) >= limit {
				break
			}

			if options.MinRating > 0 && item.Rating < options.MinRating {
				continue
			}

			game, ok := s.gameFromRAWG(item)
			if !ok {
				continue
			}

			if !s.isKnown(game.Title, game.Slug) {
				imported = append(imported, game)
				pageNew = append(pageNew, game)
			}
		}

		pageAdded, pageSkipped := s.mergeImported(pageNew)

		added += pageAdded
		skipped += pageSkipped

		if pageAdded > 0 {
			log.Printf("[import] rawg page %d: +%d (new total %d)", page, pageAdded, len(imported))
		}

		page++
	}

	return ImportResult{Imported: added, Skipped: skipped, Total: s.Total()}, nil
}

func (s *Service) gameFromRAWG(item struct {
	Slug            string  `json:"slug"`
	Name            string  `json:"name"`
	Released        string  `json:"released"`
	BackgroundImage string  `json:"background_image"`
	Rating          float64 `json:"rating"`
	Metacritic      *int    `json:"metacritic"`
	Genres          []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Platforms []struct {
		Platform struct {
			Name string `json:"name"`
		} `json:"platform"`
	} `json:"platforms"`
}) (Game, bool) {
	title := strings.TrimSpace(item.Name)

	if title == "" || len(item.Platforms) == 0 {
		return Game{}, false
	}

	platform, ok := externalPlatform(item.Platforms[0].Platform.Name)
	if !ok {
		return Game{}, false
	}

	slug := slugify(title) + "-" + platform.Code
	year := extractYear(item.Released)

	game := Game{
		ID:                shortID(slug),
		Slug:              slug,
		Title:             title,
		Year:              year,
		ReleaseDate:       item.Released,
		Platforms:         []Platform{platform},
		Cover:             item.BackgroundImage,
		Rating:            item.Rating,
		Age:               ageFor(year, platform),
		Tier:              tierFor(title, platform, year),
		Status:            "seed",
		EmulatorsRequired: false,
		Emulators:         emulatorsForPlatform(platform.Code),
		Downloads:         defaultDownloads(title),
		Sources: []GameSource{{
			ID:         "rawg",
			Name:       "RAWG",
			URL:        "https://rawg.io/games/" + item.Slug,
			ExternalID: item.Slug,
			ImportedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	game.EmulatorsRequired = len(game.Emulators) > 0

	if item.Metacritic != nil {
		game.AggregatedRating = float64(*item.Metacritic)
	}

	for _, genre := range item.Genres {
		game.Genres = append(game.Genres, genre.Name)
	}

	game.GenresRU = translateGenres(game.Genres)

	return game, true
}

func (s *Service) importIGDB(ctx context.Context, options ImportOptions) (ImportResult, error) {
	if s.enricher == nil || s.enricher.igdb == nil {
		return ImportResult{}, errors.New("catalog: IGDB client is not configured")
	}

	token, err := s.enricher.igdb.tokenValue(ctx)
	if err != nil {
		return ImportResult{}, err
	}

	limit := options.Limit
	imported := make([]Game, 0, limit)
	offset := 0
	batch := 500
	added := 0
	skipped := 0

	if options.UseOffset {
		offset = options.Offset
	} else if os.Getenv("CATALOG_IMPORT_RANDOM") != "0" {
		offset = rand.IntN(200) * batch
	}

	sort := "total_rating desc"

	switch options.Order {
	case "new":
		sort = "first_release_date desc"
	case "popular":
		sort = "total_rating_count desc"
	}

	where := ""

	if options.MinRating > 0 || options.YearFrom > 0 || options.YearTo > 0 {
		parts := make([]string, 0, 3)

		if options.MinRating > 0 {
			parts = append(parts, fmt.Sprintf("rating >= %.0f", options.MinRating))
		}

		if options.YearFrom > 0 {
			parts = append(parts, fmt.Sprintf("first_release_date >= %d", time.Date(options.YearFrom, 1, 1, 0, 0, 0, 0, time.UTC).Unix()))
		}

		if options.YearTo > 0 {
			parts = append(parts, fmt.Sprintf("first_release_date <= %d", time.Date(options.YearTo, 12, 31, 23, 59, 59, 0, time.UTC).Unix()))
		}

		where = " where " + strings.Join(parts, " & ") + ";"
	}

	for len(imported) < limit {
		query := fmt.Sprintf(
			`fields name,first_release_date,cover.url,summary,url,rating,aggregated_rating,genres.name,platforms.name,involved_companies.company.name,involved_companies.developer,involved_companies.publisher,game_modes.name,player_perspectives.name,themes.name,alternative_names.name,dlcs.name,expansions.name,bundles.name,age_ratings.category,age_ratings.rating;%s sort %s; limit %d; offset %d;`,
			where,
			sort,
			batch,
			offset,
		)

		request, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			"https://api.igdb.com/v4/games",
			strings.NewReader(query),
		)
		if err != nil {
			return ImportResult{Imported: len(imported), Total: s.Total()}, err
		}

		request.Header.Set("Client-ID", os.Getenv("TWITCH_CLIENT_KEY"))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "text/plain")

		var response *http.Response

		for attempt := 0; attempt < 3; attempt++ {
			response, err = s.enricher.client.Do(request)
			if err == nil {
				break
			}

			log.Printf("[import] igdb offset %d attempt %d: %v", offset, attempt+1, err)
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}

		if err != nil {
			break
		}

		var items []igdbImportItem

		decodeErr := json.NewDecoder(response.Body).Decode(&items)

		_ = response.Body.Close()

		if decodeErr != nil {
			log.Printf("[import] igdb offset %d: decode failed: %v", offset, decodeErr)

			break
		}

		if len(items) == 0 {
			log.Printf("[import] igdb offset %d: empty response (status %d)", offset, response.StatusCode)

			break
		}

		pageNew := make([]Game, 0, len(items))

		for _, item := range items {
			if len(imported) >= limit {
				break
			}

			game, ok := s.gameFromIGDB(item)
			if ok && !s.isKnown(game.Title, game.Slug) {
				imported = append(imported, game)
				pageNew = append(pageNew, game)
			}
		}

		pageAdded, pageSkipped := s.mergeImported(pageNew)

		added += pageAdded
		skipped += pageSkipped

		offset += batch
	}

	done := len(imported) < limit

	return ImportResult{
		Imported:   added,
		Skipped:    skipped,
		Total:      s.Total(),
		NextOffset: offset,
		Done:       done,
	}, nil
}

type igdbImportItem struct {
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
	Platforms []struct {
		Name string `json:"name"`
	} `json:"platforms"`
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
}

func (s *Service) gameFromIGDB(item igdbImportItem) (Game, bool) {
	title := strings.TrimSpace(item.Name)

	if title == "" || len(item.Platforms) == 0 {
		return Game{}, false
	}

	platform, ok := externalPlatform(item.Platforms[0].Name)
	if !ok {
		return Game{}, false
	}

	slug := slugify(title) + "-" + platform.Code
	year := 0
	releaseDate := ""

	if item.FirstReleaseDate > 0 {
		date := time.Unix(item.FirstReleaseDate, 0).UTC()
		year = date.Year()
		releaseDate = date.Format("2006-01-02")
	}

	game := Game{
		ID:                shortID(slug),
		Slug:              slug,
		Title:             title,
		Year:              year,
		ReleaseDate:       releaseDate,
		Platforms:         []Platform{platform},
		Description:       item.Summary,
		Rating:            item.Rating,
		AggregatedRating:  item.AggregatedRating,
		Age:               ageFor(year, platform),
		Tier:              tierFor(title, platform, year),
		Status:            "seed",
		Emulators:         emulatorsForPlatform(platform.Code),
		EmulatorsRequired: false,
		Downloads:         defaultDownloads(title),
		Sources: []GameSource{{
			ID:         "igdb",
			Name:       "IGDB",
			URL:        item.URL,
			ExternalID: fmt.Sprintf("%d", item.ID),
			ImportedAt: time.Now().UTC().Format(time.RFC3339),
		}},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	game.EmulatorsRequired = len(game.Emulators) > 0

	if item.Cover != nil {
		game.Cover = imageURL(item.Cover.URL)
	}

	for _, genre := range item.Genres {
		game.Genres = append(game.Genres, genre.Name)
	}

	game.GenresRU = translateGenres(game.Genres)

	for _, mode := range item.GameModes {
		game.Modes = append(game.Modes, mode.Name)
	}

	for _, perspective := range item.PlayerPerspectives {
		game.Perspectives = append(game.Perspectives, perspective.Name)
	}

	for _, theme := range item.Themes {
		game.Themes = append(game.Themes, theme.Name)
	}

	for _, alias := range item.AlternativeNames {
		game.Aliases = append(game.Aliases, alias.Name)
	}

	for _, dlc := range item.DLCs {
		game.DLC = append(game.DLC, dlc.Name)
	}

	for _, edition := range item.Expansions {
		game.Editions = append(game.Editions, edition.Name)
	}

	for _, bundle := range item.Bundles {
		game.Editions = append(game.Editions, bundle.Name)
	}

	for _, company := range item.InvolvedCompanies {
		if company.Developer {
			game.Developers = append(game.Developers, company.Company.Name)
		}

		if company.Publisher {
			game.Publishers = append(game.Publishers, company.Company.Name)
		}
	}

	for _, rating := range item.AgeRatings {
		if value := ageRatingLabel(rating.Category, rating.Rating); value != "" {
			game.AgeRating = value

			if rating.Category == 1 {
				break
			}
		}
	}

	return game, true
}

func (s *Service) isKnown(title string, slug string) bool {
	if s.db == nil {
		return false
	}

	var exists bool

	_ = s.db.QueryRowContext(
		context.Background(),
		`SELECT EXISTS(SELECT 1 FROM catalog_game WHERE slug = $1 OR lower(title) = lower($2))`,
		slug,
		title,
	).Scan(&exists)

	return exists
}

func (s *Service) mergeImported(games []Game) (int, int) {
	if len(games) == 0 || s.db == nil {
		return 0, 0
	}

	unique := make(map[string]Game, len(games))

	for _, game := range games {
		if _, exists := unique[game.Slug]; !exists {
			unique[game.Slug] = game
		}
	}

	slugs := make([]string, 0, len(unique))

	for slug := range unique {
		slugs = append(slugs, slug)
	}

	if len(slugs) > 0 {
		rows, err := s.db.QueryContext(
			context.Background(),
			`SELECT slug FROM catalog_game WHERE slug = ANY($1)`,
			pq.Array(slugs),
		)
		if err == nil {
			for rows.Next() {
				var slug string

				if rows.Scan(&slug) == nil {
					delete(unique, slug)
				}
			}

			_ = rows.Close()
		}
	}

	if len(unique) == 0 {
		return 0, len(games)
	}

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, len(games)
	}

	statement, err := tx.PrepareContext(context.Background(), `
		INSERT INTO catalog_game (slug, title, source, payload, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (slug) DO NOTHING
	`)
	if err != nil {
		_ = tx.Rollback()

		return 0, len(games)
	}

	added := 0

	for _, game := range unique {
		raw, err := json.Marshal(game)
		if err != nil {
			continue
		}

		source := ""

		if len(game.Sources) > 0 {
			source = game.Sources[0].ID
		}

		result, err := statement.ExecContext(context.Background(), game.Slug, game.Title, source, raw)
		if err != nil {
			continue
		}

		if count, err := result.RowsAffected(); err == nil {
			added += int(count)
		}
	}

	_ = statement.Close()

	if err := tx.Commit(); err != nil {
		return 0, len(games)
	}

	return added, len(games) - added
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit))
}

func (s *Service) fetchJSON(ctx context.Context, target string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	client := s.client
	isRAWG := strings.Contains(target, "rawg.io")

	if isRAWG {
		if err := rawgQuotaBegin(); err != nil {
			return nil, err
		}

		client = rawgHTTPClient()
	}

	response, err := client.Do(request)
	if err != nil {
		if urlErr, ok := err.(*url.Error); ok {
			return nil, fmt.Errorf("catalog import: %v", urlErr.Err)
		}

		return nil, errors.New("catalog import: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	body, readErr := readLimited(response.Body, 16<<20)

	if isRAWG {
		rawgQuotaObserve(response.StatusCode, body)
	}

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog import: status %d", response.StatusCode)
	}

	return body, readErr
}

func (s *Service) ImportStatus() map[string]any {
	rawgCursor := s.loadCursor()
	igdbCursor := s.loadIGDBCursor()
	quota := rawgQuotaStatus()

	s.mu.RLock()
	total := s.Total()
	s.mu.RUnlock()

	return map[string]any{
		"total": total,
		"rawg": map[string]any{
			"cursor": rawgCursor,
			"quota":  quota,
		},
		"igdb": map[string]any{
			"cursor": igdbCursor,
		},
		"sources": strings.TrimSpace(os.Getenv("CATALOG_IMPORT_SOURCES")),
	}
}
