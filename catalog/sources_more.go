package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type steamClient struct {
	client *http.Client
}

type vndbClient struct {
	client *http.Client
}

type wikidataClient struct {
	client *http.Client
}

type abandonwareClient struct {
	client *http.Client
}

func newSteamClient(client *http.Client) *steamClient {
	return &steamClient{client: client}
}

func newVNDBClient(client *http.Client) *vndbClient {
	return &vndbClient{client: client}
}

func newWikidataClient(client *http.Client) *wikidataClient {
	return &wikidataClient{client: client}
}

func newAbandonwareClient(client *http.Client) *abandonwareClient {
	return &abandonwareClient{client: client}
}

func (c *steamClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := "https://store.steampowered.com/api/storesearch/?l=russian&cc=RU&term=" + url.QueryEscape(title)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	var search struct {
		Items []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}

	if json.Unmarshal(body, &search) != nil || len(search.Items) == 0 {
		fallback := "https://store.steampowered.com/api/storesearch/?l=english&cc=US&term=" + url.QueryEscape(title)

		if altBody, altErr := c.get(ctx, fallback); altErr == nil {
			_ = json.Unmarshal(altBody, &search)
		}
	}

	if len(search.Items) == 0 {
		return nil, errors.New("steam: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, item := range search.Items {
		if score := titleScore(title, item.Name); score > bestScore {
			bestScore = score
			best = index
		}
	}

	if bestScore < 0.8 {
		return nil, errors.New("steam: no matching page")
	}

	appID := search.Items[best].ID
	detailURL := fmt.Sprintf(
		"https://store.steampowered.com/api/appdetails?appids=%d&l=russian&cc=RU",
		appID,
	)

	detailBody, err := c.get(ctx, detailURL)
	if err != nil {
		return nil, err
	}

	var details map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name                string   `json:"name"`
			ShortDescription    string   `json:"short_description"`
			DetailedDescription string   `json:"detailed_description"`
			HeaderImage         string   `json:"header_image"`
			Website             string   `json:"website"`
			Developers          []string `json:"developers"`
			Publishers          []string `json:"publishers"`
			Genres              []struct {
				Description string `json:"description"`
			} `json:"genres"`
			ReleaseDate struct {
				Date string `json:"date"`
			} `json:"release_date"`
			Screenshots []struct {
				PathFull string `json:"path_full"`
			} `json:"screenshots"`
			DLC           []int `json:"dlc"`
			PackageGroups []struct {
				Subs []struct {
					OptionText string `json:"option_text"`
				} `json:"subs"`
			} `json:"package_groups"`
		} `json:"data"`
	}

	if json.Unmarshal(detailBody, &details) != nil {
		return nil, errors.New("steam: details failed")
	}

	detail, ok := details[fmt.Sprintf("%d", appID)]

	if !ok || !detail.Success {
		fallbackURL := fmt.Sprintf(
			"https://store.steampowered.com/api/appdetails?appids=%d&l=english&cc=US",
			appID,
		)

		altBody, altErr := c.get(ctx, fallbackURL)
		if altErr != nil {
			return nil, errors.New("steam: details missing")
		}

		details = nil
		if json.Unmarshal(altBody, &details) != nil {
			return nil, errors.New("steam: details failed")
		}

		detail, ok = details[fmt.Sprintf("%d", appID)]
		if !ok || !detail.Success {
			return nil, errors.New("steam: details missing")
		}
	}

	enrichment := &Enrichment{
		Source:      "steam",
		Title:       detail.Data.Name,
		TitleRu:     steamTitleRu(detail.Data.Name),
		SourceURL:   fmt.Sprintf("https://store.steampowered.com/app/%d", appID),
		Cover:       detail.Data.HeaderImage,
		Description: firstNonEmpty(detail.Data.ShortDescription, detail.Data.DetailedDescription),
		Website:     detail.Data.Website,
		Developers:  detail.Data.Developers,
		Publishers:  detail.Data.Publishers,
	}

	for _, genre := range detail.Data.Genres {
		enrichment.Genres = append(enrichment.Genres, genre.Description)
	}

	for _, shot := range detail.Data.Screenshots {
		enrichment.Screenshots = append(enrichment.Screenshots, shot.PathFull)
	}

	if detail.Data.ReleaseDate.Date != "" {
		enrichment.Year = extractYear(detail.Data.ReleaseDate.Date)
	}

	for _, group := range detail.Data.PackageGroups {
		for _, sub := range group.Subs {
			if edition := steamEditionName(sub.OptionText); edition != "" {
				enrichment.Editions = appendUnique(enrichment.Editions, edition)
			}
		}
	}

	if len(detail.Data.DLC) > 0 {
		enrichment.DLC = appendUnique(enrichment.DLC, c.dlcNames(ctx, detail.Data.DLC)...)
	}

	return enrichment, nil
}

func steamTitleRu(value string) string {
	if containsCyrillic(value) {
		return value
	}

	return ""
}

func steamEditionName(value string) string {
	cleaned := strings.TrimSpace(value)

	if index := strings.LastIndex(cleaned, " - "); index > 0 {
		rest := strings.TrimSpace(cleaned[index+3:])

		if rest != "" && (rest[0] >= '0' && rest[0] <= '9' || strings.ContainsAny(rest[:1], "$€£₽")) {
			cleaned = strings.TrimSpace(cleaned[:index])
		}
	}

	return cleaned
}

func (c *steamClient) dlcNames(ctx context.Context, ids []int) []string {
	if len(ids) > 12 {
		ids = ids[:12]
	}

	parts := make([]string, 0, len(ids))

	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}

	target := fmt.Sprintf(
		"https://store.steampowered.com/api/appdetails?appids=%s&l=russian&cc=RU",
		strings.Join(parts, ","),
	)

	body, err := c.get(ctx, target)
	if err != nil {
		return nil
	}

	var payload map[string]struct {
		Success bool `json:"success"`
		Data    struct {
			Name string `json:"name"`
		} `json:"data"`
	}

	if json.Unmarshal(body, &payload) != nil {
		payload = nil
	}

	if len(payload) == 0 {
		fallback := fmt.Sprintf(
			"https://store.steampowered.com/api/appdetails?appids=%s&l=english&cc=US",
			strings.Join(parts, ","),
		)

		if altBody, altErr := c.get(ctx, fallback); altErr == nil {
			_ = json.Unmarshal(altBody, &payload)
		}
	}

	if len(payload) == 0 {
		return nil
	}

	names := make([]string, 0, len(payload))

	for _, item := range payload {
		if item.Success && item.Data.Name != "" {
			names = append(names, item.Data.Name)
		}
	}

	return names
}

func (c *steamClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("steam: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func (c *vndbClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	payload := map[string]any{
		"filters": []any{"search", "=", title},
		"fields":  "title,image.url,released,description,developers.name,rating",
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://api.vndb.org/kana/vn",
		strings.NewReader(string(raw)),
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("vndb: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vndb: status %d", response.StatusCode)
	}

	var result struct {
		Results []struct {
			ID          string  `json:"id"`
			Title       string  `json:"title"`
			Released    string  `json:"released"`
			Description string  `json:"description"`
			Rating      float64 `json:"rating"`
			Developers  []struct {
				Name string `json:"name"`
			} `json:"developers"`
			Image *struct {
				URL string `json:"url"`
			} `json:"image"`
		} `json:"results"`
	}

	if json.NewDecoder(response.Body).Decode(&result) != nil || len(result.Results) == 0 {
		return nil, errors.New("vndb: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, item := range result.Results {
		if score := titleScore(title, item.Title); score > bestScore {
			bestScore = score
			best = index
		}
	}

	if bestScore < 0.8 {
		return nil, errors.New("vndb: no matching page")
	}

	game := result.Results[best]
	enrichment := &Enrichment{
		Source:      "vndb",
		Title:       game.Title,
		SourceURL:   "https://vndb.org/" + game.ID,
		Description: game.Description,
		Rating:      game.Rating,
		Year:        extractYear(game.Released),
	}

	if game.Image != nil {
		enrichment.Cover = game.Image.URL
	}

	for _, developer := range game.Developers {
		enrichment.Developers = append(enrichment.Developers, developer.Name)
	}

	return enrichment, nil
}

func (c *wikidataClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := "https://www.wikidata.org/w/api.php?action=wbsearchentities&format=json&limit=5&language=ru&uselang=ru&search=" +
		url.QueryEscape(title)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	var search struct {
		Search []struct {
			ID          string `json:"id"`
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"search"`
	}

	if json.Unmarshal(body, &search) != nil || len(search.Search) == 0 {
		return nil, errors.New("wikidata: not found")
	}

	best := -1
	bestScore := 0.0

	for index, item := range search.Search {
		description := strings.ToLower(item.Description)

		if !strings.Contains(description, "игр") && !strings.Contains(description, "game") &&
			!strings.Contains(description, "visual novel") {
			continue
		}

		if score := titleScore(title, item.Label); score > bestScore {
			bestScore = score
			best = index
		}
	}

	if best < 0 {
		return nil, errors.New("wikidata: no matching entity")
	}

	if bestScore < 0.8 {
		return nil, errors.New("wikidata: no matching entity")
	}

	entityID := search.Search[best].ID
	entityURL := "https://www.wikidata.org/w/api.php?action=wbgetentities&format=json&props=claims|labels&languages=ru|en&ids=" +
		url.QueryEscape(entityID)

	entityBody, err := c.get(ctx, entityURL)
	if err != nil {
		return nil, err
	}

	var entities struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Value string `json:"value"`
			} `json:"labels"`
			Claims map[string][]struct {
				Mainsnak struct {
					Datavalue struct {
						Value any `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}

	if json.Unmarshal(entityBody, &entities) != nil {
		return nil, errors.New("wikidata: entity parse failed")
	}

	entity, ok := entities.Entities[entityID]
	if !ok {
		return nil, errors.New("wikidata: entity missing")
	}

	enrichment := &Enrichment{
		Source:    "wikidata",
		Title:     firstNonEmpty(entity.Labels["ru"].Value, entity.Labels["en"].Value, title),
		SourceURL: "https://www.wikidata.org/wiki/" + entityID,
	}

	if label := entity.Labels["ru"].Value; label != "" && containsCyrillic(label) {
		enrichment.TitleRu = label
	}

	if claims := entity.Claims["P18"]; len(claims) > 0 {
		if file, ok := claims[0].Mainsnak.Datavalue.Value.(string); ok && file != "" {
			enrichment.Cover = "https://commons.wikimedia.org/wiki/Special:FilePath/" + url.PathEscape(file)
		}
	}

	if claims := entity.Claims["P577"]; len(claims) > 0 {
		if timeValue, ok := claims[0].Mainsnak.Datavalue.Value.(map[string]any); ok {
			if date, ok := timeValue["time"].(string); ok {
				enrichment.Year = extractYear(date)
			}
		}
	}

	if claims := entity.Claims["P856"]; len(claims) > 0 {
		if website, ok := claims[0].Mainsnak.Datavalue.Value.(string); ok {
			enrichment.Website = website
		}
	}

	return enrichment, nil
}

func (c *wikidataClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (+https://github.com/p7x-dev)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("wikidata: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikidata: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

var abandonwareLinkPattern = regexp.MustCompile(`href="(/game/[^"#?]+)"[^>]*>([^<]{2,120})<`)
var abandonwareImagePattern = regexp.MustCompile(`<meta[^>]+property="og:image"[^>]+content="([^"]+)"`)
var abandonwareDescriptionPattern = regexp.MustCompile(`<meta[^>]+name="description"[^>]+content="([^"]+)"`)
var abandonwareTitlePattern = regexp.MustCompile(`<title>([^<]+)</title>`)

func (c *abandonwareClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := "https://www.myabandonware.com/search/?q=" + url.QueryEscape(title)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	matches := abandonwareLinkPattern.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, errors.New("abandonware: not found")
	}

	bestPath := ""
	bestScore := 0.0

	for _, match := range matches {
		name := strings.TrimSpace(string(match[2]))
		score := titleScore(title, name)

		if score > bestScore {
			bestScore = score
			bestPath = string(match[1])
		}
	}

	if bestPath == "" || bestScore < 0.8 {
		return nil, errors.New("abandonware: no matching page")
	}

	pageURL := "https://www.myabandonware.com" + bestPath
	page, err := c.get(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	enrichment := &Enrichment{Source: "abandonware", SourceURL: pageURL}

	if match := abandonwareImagePattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Cover = string(match[1])
	}

	if match := abandonwareDescriptionPattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Description = string(match[1])
	}

	if match := abandonwareTitlePattern.FindSubmatch(page); len(match) > 1 {
		enrichment.Title = strings.TrimSuffix(strings.TrimSpace(string(match[1])), " - My Abandonware")
		enrichment.Year = extractYear(enrichment.Title)
	}

	if enrichment.Title == "" {
		enrichment.Title = title
	}

	return enrichment, nil
}

func (c *abandonwareClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("abandonware: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("abandonware: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}
