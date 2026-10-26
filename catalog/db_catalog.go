package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/lib/pq"
)

func (s *Service) Total() int {
	if s.db == nil {
		return 0
	}

	var total int

	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM catalog_game`).Scan(&total); err != nil {
		return 0
	}

	return total
}

func (s *Service) buildWhere(filters Filters) (string, []any) {
	conditions := make([]string, 0, 8)
	args := make([]any, 0, 8)

	add := func(format string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(format, len(args)))
	}

	status := filters.Status
	if status == "" {
		status = "seed"
	}

	add(`status = $%d`, status)

	if query := strings.ToLower(strings.TrimSpace(filters.Query)); query != "" {
		add(`search_text LIKE $%d`, "%"+query+"%")
	}

	if filters.Platform != "" {
		add(`$%d = ANY(platforms)`, filters.Platform)
	}

	if filters.Tier != "" {
		add(`tier = $%d`, filters.Tier)
	}

	if filters.Age != "" {
		add(`age = $%d`, filters.Age)
	}

	if filters.Emulator != nil {
		add(`COALESCE((payload->>'emulatorsRequired')::boolean, false) = $%d`, *filters.Emulator)
	}

	if filters.YearFrom > 0 {
		add(`year >= $%d`, filters.YearFrom)
	}

	if filters.YearTo > 0 {
		add(`year <= $%d`, filters.YearTo)
	}

	if filters.Verified != nil {
		verified := `(source <> 'rgg-cat' OR EXISTS (SELECT 1 FROM game_enrichment e WHERE e.slug = catalog_game.slug))`

		if *filters.Verified {
			conditions = append(conditions, verified)
		} else {
			conditions = append(conditions, "NOT "+verified)
		}
	}

	return strings.Join(conditions, " AND "), args
}

func (s *Service) orderBy(filters Filters) string {
	field := strings.TrimSpace(filters.Sort)
	descending := strings.EqualFold(filters.Order, "desc")
	direction := "ASC"

	if descending {
		direction = "DESC"
	}

	switch field {
	case "year":
		return "year " + direction + " NULLS LAST"
	case "tier":
		return "CASE tier WHEN 'platinum' THEN 3 WHEN 'diamond' THEN 2 WHEN 'gold' THEN 1 ELSE 0 END " + direction
	case "rating":
		return "COALESCE((payload->>'rating')::float, 0) " + direction
	case "updated":
		return "updated_at " + direction
	default:
		return "lower(title) " + direction
	}
}

func (s *Service) List(filters Filters) ListResponse {
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

	where, args := s.buildWhere(filters)
	total := 0

	_ = s.db.QueryRowContext(
		context.Background(),
		`SELECT count(*) FROM catalog_game WHERE `+where,
		args...,
	).Scan(&total)

	query := fmt.Sprintf(
		`SELECT payload FROM catalog_game WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		where,
		s.orderBy(filters),
		len(args)+1,
		len(args)+2,
	)
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)

	rows, err := s.db.QueryContext(context.Background(), query, queryArgs...)
	if err != nil {
		return ListResponse{Items: []Game{}, Total: total, Page: page, PageSize: pageSize}
	}

	defer func() {
		_ = rows.Close()
	}()

	items := make([]Game, 0, pageSize)

	for rows.Next() {
		var raw []byte

		if err := rows.Scan(&raw); err != nil {
			continue
		}

		var game Game

		if json.Unmarshal(raw, &game) == nil {
			items = append(items, game)
		}
	}

	if !filters.SkipEnrich {
		items = s.enricher.EnrichMany(context.Background(), items, false, false)
		s.enricher.Start(items)
	}

	facets := Facets{Tiers: []FacetValue{}, Ages: []FacetValue{}, Platforms: []PlatformFacet{}}

	if !filters.SkipFacets {
		facets = s.facetsDB(where, args)
	}

	return ListResponse{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Facets:   facets,
	}
}

func (s *Service) facetsDB(where string, args []any) Facets {
	result := Facets{Tiers: []FacetValue{}, Ages: []FacetValue{}}
	names := make(map[string]string)

	for _, platform := range PlatformList() {
		names[platform.Code] = platform.Name
	}

	platformRows, err := s.db.QueryContext(
		context.Background(),
		`SELECT platform, count(*) FROM catalog_game, unnest(platforms) AS platform
		 WHERE `+where+` GROUP BY platform ORDER BY count(*) DESC LIMIT 50`,
		args...,
	)
	if err == nil {
		for platformRows.Next() {
			var code string
			var count int

			if platformRows.Scan(&code, &count) == nil {
				result.Platforms = append(result.Platforms, PlatformFacet{
					Code:  code,
					Name:  names[code],
					Count: count,
				})
			}
		}

		_ = platformRows.Close()
	}

	tiers := make(map[string]int)
	tierRows, err := s.db.QueryContext(
		context.Background(),
		`SELECT tier, count(*) FROM catalog_game WHERE `+where+` GROUP BY tier`,
		args...,
	)
	if err == nil {
		for tierRows.Next() {
			var tier string
			var count int

			if tierRows.Scan(&tier, &count) == nil {
				tiers[tier] = count
			}
		}

		_ = tierRows.Close()
	}

	result.Tiers = []FacetValue{
		{Value: "platinum", Count: tiers["platinum"]},
		{Value: "diamond", Count: tiers["diamond"]},
		{Value: "gold", Count: tiers["gold"]},
		{Value: "none", Count: tiers["none"]},
	}

	ages := make(map[string]int)
	ageRows, err := s.db.QueryContext(
		context.Background(),
		`SELECT age, count(*) FROM catalog_game WHERE `+where+` GROUP BY age`,
		args...,
	)
	if err == nil {
		for ageRows.Next() {
			var age string
			var count int

			if ageRows.Scan(&age, &count) == nil {
				ages[age] = count
			}
		}

		_ = ageRows.Close()
	}

	result.Ages = []FacetValue{
		{Value: "classic", Count: ages["classic"]},
		{Value: "retro", Count: ages["retro"]},
		{Value: "modern", Count: ages["modern"]},
		{Value: "new", Count: ages["new"]},
	}

	return result
}

var searchSynonyms = map[string][]string{
	"ведьмак":     {"witcher"},
	"ведьмак 3":   {"witcher 3"},
	"скайрим":     {"skyrim"},
	"скайрим 5":   {"skyrim"},
	"скайрим v":   {"skyrim"},
	"морровинд":   {"morrowind"},
	"обливион":    {"oblivion"},
	"варкрафт":    {"warcraft"},
	"старкрафт":   {"starcraft"},
	"диабло":      {"diablo"},
	"гта":         {"gta"},
	"дум":         {"doom"},
	"фоллаут":     {"fallout"},
	"сталкер":     {"stalker"},
	"метро":       {"metro"},
	"майнкрафт":   {"minecraft"},
	"террария":    {"terraria"},
	"симс":        {"sims"},
	"цивилизация": {"civilization"},
	"герои":       {"heroes"},
	"казаки":      {"cossacks"},
	"мортал":      {"mortal"},
	"комбат":      {"kombat"},
	"резидент":    {"resident"},
	"сайлент":     {"silent"},
	"хитман":      {"hitman"},
	"томб":        {"tomb"},
	"райдер":      {"raider"},
	"ассасин":     {"assassin"},
	"крид":        {"creed"},
	"готик":       {"gothic"},
	"ризен":       {"risen"},
}

func normalizeSearchQuery(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "ё", "е")
	var builder strings.Builder
	space := false

	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' {
			builder.WriteRune(r)
			space = false

			continue
		}

		if !space {
			builder.WriteByte(' ')
			space = true
		}
	}

	return strings.TrimSpace(builder.String())
}

func (s *Service) aliasMap() map[string]string {
	result := make(map[string]string, len(searchSynonyms))

	for term, variants := range searchSynonyms {
		if len(variants) > 0 {
			result[term] = variants[0]
		}
	}

	if s.db == nil {
		return result
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT normalized_term, normalized_replacement
		 FROM search_synonyms
		 WHERE game_slug IS NULL AND enabled AND replacement IS NOT NULL`,
	)
	if err != nil {
		return result
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var term string
		var replacement string

		if rows.Scan(&term, &replacement) == nil {
			result[term] = replacement
		}
	}

	return result
}

func romanNormalizeQuery(query string) string {
	tokens := strings.Fields(query)
	roman := map[string]string{"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6", "vii": "7", "viii": "8", "ix": "9", "x": "10"}
	changed := false

	for index, token := range tokens {
		if value, ok := roman[token]; ok {
			tokens[index] = value
			changed = true
		}
	}

	if !changed {
		return query
	}

	return strings.Join(tokens, " ")
}

func (s *Service) searchVariants(query string) ([]string, []string) {
	aliases := s.aliasMap()
	variants := []string{query}
	seen := map[string]bool{query: true}

	add := func(value string) {
		value = normalizeSearchQuery(value)
		if value == "" || seen[value] {
			return
		}

		seen[value] = true
		variants = append(variants, value)
	}

	if replacement, ok := aliases[query]; ok {
		add(replacement)
	}

	tokens := strings.Fields(query)
	tokenChanged := false

	for index, token := range tokens {
		if replacement, ok := aliases[token]; ok {
			tokens[index] = replacement
			tokenChanged = true
		}
	}

	if tokenChanged {
		add(strings.Join(tokens, " "))
	}

	add(romanNormalizeQuery(query))

	if containsCyrillic(query) {
		add(normalizeSearchQuery(transliterateRu(query)))
	}

	patterns := make([]string, 0, len(variants))

	for _, variant := range variants {
		patterns = append(patterns, "%"+variant+"%")
	}

	return patterns, variants
}

func (s *Service) Search(query string, limit int) []Game {
	if limit < 1 {
		limit = 8
	}

	if limit > 50 {
		limit = 50
	}

	query = normalizeSearchQuery(query)
	if query == "" {
		return []Game{}
	}

	patterns, _ := s.searchVariants(query)
	directSlugs := s.gameSynonymSlugs(query)
	games := make(map[string]Game, limit*3)

	addRows := func(where string, args ...any) {
		rows, err := s.db.QueryContext(
			context.Background(),
			`SELECT payload FROM catalog_game WHERE `+where+` LIMIT 24`,
			args...,
		)
		if err != nil {
			return
		}

		defer func() {
			_ = rows.Close()
		}()

		for rows.Next() {
			var raw []byte

			if rows.Scan(&raw) != nil {
				continue
			}

			var game Game

			if json.Unmarshal(raw, &game) == nil {
				games[game.Slug] = game
			}
		}
	}

	if len(directSlugs) > 0 {
		addRows("status = 'seed' AND slug = ANY($1)", pq.Array(directSlugs))
	}

	for _, pattern := range patterns {
		addRows("status = 'seed' AND search_text LIKE $1", pattern)
	}

	if len(games) == 0 {
		addRows("status = 'seed' AND (title ILIKE $1 OR payload->>'titleRu' ILIKE $1)", "%"+query+"%")
	}

	items := make([]Game, 0, len(games))
	direct := make(map[string]bool, len(directSlugs))

	for _, slug := range directSlugs {
		direct[slug] = true
	}

	for _, game := range games {
		items = append(items, game)
	}

	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		leftScore := searchRank(left, query, direct[left.Slug])
		rightScore := searchRank(right, query, direct[right.Slug])

		if leftScore != rightScore {
			return leftScore > rightScore
		}

		return strings.ToLower(left.Title) < strings.ToLower(right.Title)
	})

	if len(items) > limit {
		items = items[:limit]
	}

	return s.enricher.EnrichMany(context.Background(), items, false, false)
}

func searchRank(game Game, query string, direct bool) int {
	if direct {
		return 500
	}

	title := strings.ToLower(game.Title)

	switch {
	case title == query:
		return 400
	case strings.HasPrefix(title, query):
		return 300
	case strings.Contains(title, query):
		return 200
	case strings.Contains(strings.ToLower(game.TitleRu), query):
		return 150
	default:
		return 100
	}
}

func (s *Service) UpdateGame(slug string, patch map[string]any) (Game, bool) {
	if s.db == nil {
		return Game{}, false
	}

	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT payload FROM catalog_game WHERE slug = $1`,
		slug,
	).Scan(&raw); err != nil {
		return Game{}, false
	}

	var payload map[string]any

	if json.Unmarshal(raw, &payload) != nil {
		return Game{}, false
	}

	allowed := map[string]bool{
		"title": true, "titleRu": true, "originalTitle": true, "description": true,
		"cover": true, "year": true, "releaseDate": true, "genres": true,
		"genresRu": true, "modes": true, "perspectives": true, "themes": true,
		"developers": true, "publishers": true, "website": true, "ageRating": true,
		"screenshots": true, "aliases": true, "searchTags": true, "tier": true,
		"age": true, "status": true, "emulatorsRequired": true, "platforms": true,
		"emulators": true, "downloads": true, "sources": true, "rating": true,
		"aggregatedRating": true, "completion": true, "dlc": true, "editions": true,
		"mods": true, "popularMods": true, "popularCollections": true,
	}

	for key, value := range patch {
		if allowed[key] {
			payload[key] = value
		}
	}

	title, _ := payload["title"].(string)

	if strings.TrimSpace(title) == "" {
		return Game{}, false
	}

	updated, err := json.Marshal(payload)
	if err != nil {
		return Game{}, false
	}

	tier, _ := payload["tier"].(string)
	age, _ := payload["age"].(string)
	status, _ := payload["status"].(string)

	var result []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`UPDATE catalog_game
		 SET title = $1,
		     payload = $2,
		     tier = COALESCE(NULLIF($4, ''), tier),
		     age = COALESCE(NULLIF($5, ''), age),
		     status = COALESCE(NULLIF($6, ''), status),
		     search_text = lower(concat_ws(' ',
		         $1,
		         payload->>'titleRu',
		         payload->>'originalTitle',
		         payload->>'description',
		         payload->>'genres',
		         payload->>'genresRu',
		         payload->>'modes',
		         payload->>'perspectives',
		         payload->>'themes',
		         payload->>'developers',
		         payload->>'publishers',
		         payload->>'aliases',
		         payload->>'searchTags'
		     )),
		     updated_at = now()
		 WHERE slug = $3
		 RETURNING payload`,
		title,
		updated,
		slug,
		tier,
		age,
		status,
	).Scan(&result); err != nil {
		return Game{}, false
	}

	var game Game
	if json.Unmarshal(result, &game) != nil {
		return Game{}, false
	}

	return game, true
}

func (s *Service) Get(slug string) (Game, bool) {
	var raw []byte

	if err := s.db.QueryRowContext(
		context.Background(),
		`SELECT payload FROM catalog_game WHERE slug = $1`,
		slug,
	).Scan(&raw); err != nil {
		return Game{}, false
	}

	var game Game

	if json.Unmarshal(raw, &game) != nil {
		return Game{}, false
	}

	for _, edition := range s.relatedEditionsDB(game) {
		game.Editions = appendUnique(game.Editions, edition)
	}

	return game, true
}

func (s *Service) relatedEditionsDB(game Game) []string {
	key := editionKey(game.Title)

	if len(key) < 6 || len(strings.Fields(key)) < 2 {
		return nil
	}

	token := ""

	for _, candidate := range titleTokens(game.Title) {
		if len(candidate) > len(token) {
			token = candidate
		}
	}

	if token == "" {
		return nil
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT payload FROM catalog_game
		 WHERE status IN ('seed', 'approved') AND slug <> $1 AND lower(title) LIKE '%' || $2 || '%'
		 LIMIT 200`,
		game.Slug,
		token,
	)
	if err != nil {
		return nil
	}

	defer func() {
		_ = rows.Close()
	}()

	result := make([]string, 0, 8)
	seen := map[string]bool{strings.ToLower(game.Title): true}

	for rows.Next() {
		var raw []byte

		if err := rows.Scan(&raw); err != nil {
			continue
		}

		var candidate Game

		if json.Unmarshal(raw, &candidate) != nil {
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
	}

	sort.Strings(result)

	if len(result) > 10 {
		result = result[:10]
	}

	return result
}

func (s *Service) Pool(filters Filters) []Game {
	where, args := s.buildWhere(filters)
	query := fmt.Sprintf(
		`SELECT payload FROM catalog_game WHERE %s ORDER BY lower(title) LIMIT 5000`,
		where,
	)

	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return []Game{}
	}

	defer func() {
		_ = rows.Close()
	}()

	items := make([]Game, 0, 64)

	for rows.Next() {
		var raw []byte

		if err := rows.Scan(&raw); err != nil {
			continue
		}

		var game Game

		if json.Unmarshal(raw, &game) == nil {
			items = append(items, game)
		}
	}

	return items
}

var _ = sql.ErrNoRows

// DeleteGame removes a game from the catalog and its derived search data.
func (s *Service) DeleteGame(slug string) bool {
	if s.db == nil || slug == "" {
		return false
	}

	result, err := s.db.ExecContext(context.Background(), `DELETE FROM catalog_game WHERE slug = $1`, slug)
	if err != nil {
		log.Printf("catalog: delete game %q: %v", slug, err)

		return false
	}

	deleted, _ := result.RowsAffected()

	return deleted > 0
}
