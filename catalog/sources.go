package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type wikipediaClient struct {
	client *http.Client
}

type giantbombClient struct {
	client *http.Client
	key    string
}

type thegamesdbClient struct {
	client *http.Client
	key    string
}

type longplayClient struct {
	client *http.Client
	base   string
	key    string
}

type screenscraperClient struct {
	client      *http.Client
	devID       string
	devPassword string
	softname    string
	user        string
	password    string
}

func newWikipediaClient(client *http.Client) *wikipediaClient {
	return &wikipediaClient{client: client}
}

func newGiantbombClient(client *http.Client) *giantbombClient {
	key := strings.TrimSpace(os.Getenv("GIANTBOMB_API_KEY"))
	if key == "" {
		return nil
	}

	return &giantbombClient{client: client, key: key}
}

func newScreenscraperClient(client *http.Client) *screenscraperClient {
	devID := strings.TrimSpace(os.Getenv("SCREENSCRAPER_DEV_ID"))
	devPassword := strings.TrimSpace(os.Getenv("SCREENSCRAPER_DEV_PASSWORD"))

	if devID == "" || devPassword == "" {
		return nil
	}

	softname := strings.TrimSpace(os.Getenv("SCREENSCRAPER_SOFTNAME"))
	if softname == "" {
		softname = "NamelessStreamHata"
	}

	return &screenscraperClient{
		client:      client,
		devID:       devID,
		devPassword: devPassword,
		softname:    softname,
		user:        strings.TrimSpace(os.Getenv("SCREENSCRAPER_USER")),
		password:    strings.TrimSpace(os.Getenv("SCREENSCRAPER_PASSWORD")),
	}
}

func newLongplayClient(client *http.Client) *longplayClient {
	base := strings.TrimSpace(os.Getenv("PIPED_API_URL"))
	if base == "" {
		base = "https://pipedapi.kavin.rocks"
	}

	return &longplayClient{
		client: client,
		base:   strings.TrimRight(base, "/"),
		key:    strings.TrimSpace(os.Getenv("YOUTUBE_API_KEY")),
	}
}

func newTheGamesDBClient(client *http.Client) *thegamesdbClient {
	key := strings.TrimSpace(os.Getenv("THEGAMESDB_API_KEY"))
	if key == "" {
		return nil
	}

	return &thegamesdbClient{client: client, key: key}
}

func (c *wikipediaClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := fmt.Sprintf(
		"https://ru.wikipedia.org/w/api.php?action=query&format=json&list=search&srlimit=5&srsearch=%s",
		url.QueryEscape(title),
	)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Query.Search) == 0 {
		return nil, errors.New("wikipedia: game not found")
	}

	best := ""
	bestScore := 0.0

	for _, item := range payload.Query.Search {
		score := titleScore(title, item.Title)

		if score > bestScore {
			bestScore = score
			best = item.Title
		}
	}

	if best == "" || bestScore < 0.8 {
		return nil, errors.New("wikipedia: no matching page")
	}

	summaryURL := "https://ru.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(best)

	summaryBody, err := c.get(ctx, summaryURL)
	if err != nil {
		return nil, err
	}

	var summary struct {
		Title     string `json:"title"`
		Extract   string `json:"extract"`
		Thumbnail struct {
			Source string `json:"source"`
		} `json:"thumbnail"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}

	if err := json.Unmarshal(summaryBody, &summary); err != nil {
		return nil, err
	}

	return &Enrichment{
		Source:      "wikipedia",
		Title:       summary.Title,
		SourceURL:   summary.ContentURLs.Desktop.Page,
		Cover:       summary.Thumbnail.Source,
		Description: summary.Extract,
	}, nil
}

func (c *wikipediaClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (+https://github.com/p7x-dev)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("wikipedia: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikipedia: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}

func (c *giantbombClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	endpoint := fmt.Sprintf(
		"https://www.giantbomb.com/api/search/?api_key=%s&format=json&resources=game&limit=5&query=%s",
		url.QueryEscape(c.key),
		url.QueryEscape(title),
	)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Results []struct {
			Name          string `json:"name"`
			Deck          string `json:"deck"`
			Description   string `json:"description"`
			OriginalDate  string `json:"original_release_date"`
			SiteDetailURL string `json:"site_detail_url"`
			Image         struct {
				OriginalURL string `json:"original_url"`
			} `json:"image"`
		} `json:"results"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Results) == 0 {
		return nil, errors.New("giantbomb: game not found")
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

	if bestScore < 0.8 {
		return nil, errors.New("giantbomb: no matching page")
	}

	game := payload.Results[best]
	enrichment := &Enrichment{
		Source:      "giantbomb",
		Title:       game.Name,
		SourceURL:   game.SiteDetailURL,
		Cover:       game.Image.OriginalURL,
		Description: firstNonEmpty(game.Deck, game.Description),
	}

	if game.OriginalDate != "" {
		enrichment.ReleaseDate = game.OriginalDate

		if date, err := parseDate(game.OriginalDate); err == nil {
			enrichment.Year = date.Year()
		}
	}

	return enrichment, nil
}

func (c *giantbombClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0 (+https://github.com/p7x-dev)")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("giantbomb: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("giantbomb: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func (c *thegamesdbClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	endpoint := fmt.Sprintf(
		"https://api.thegamesdb.net/v1/Games/ByGameName?apikey=%s&name=%s&include=boxart",
		url.QueryEscape(c.key),
		url.QueryEscape(title),
	)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Include struct {
			Boxart struct {
				BaseURL struct {
					Original string `json:"original"`
				} `json:"base_url"`
			} `json:"boxart"`
		} `json:"include"`
		Data struct {
			Games []struct {
				ID          int    `json:"id"`
				GameTitle   string `json:"game_title"`
				ReleaseDate string `json:"release_date"`
				Overview    string `json:"overview"`
				Boxart      struct {
					Front string `json:"front"`
				} `json:"boxart"`
			} `json:"games"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data.Games) == 0 {
		return nil, errors.New("thegamesdb: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, item := range payload.Data.Games {
		score := titleScore(title, item.GameTitle)

		if score > bestScore {
			bestScore = score
			best = index
		}
	}

	if bestScore < 0.8 {
		return nil, errors.New("thegamesdb: no matching page")
	}

	game := payload.Data.Games[best]
	enrichment := &Enrichment{
		Source:      "thegamesdb",
		Title:       game.GameTitle,
		SourceURL:   fmt.Sprintf("https://thegamesdb.net/game.php?id=%d", game.ID),
		Description: game.Overview,
	}

	if game.Boxart.Front != "" && payload.Include.Boxart.BaseURL.Original != "" {
		enrichment.Cover = payload.Include.Boxart.BaseURL.Original + game.Boxart.Front
	}

	if images, err := c.images(ctx, game.ID); err == nil {
		for _, image := range images {
			if image.Type == "screenshot" {
				enrichment.Screenshots = append(enrichment.Screenshots, image.URL)
			}

			if image.Type == "boxart" && enrichment.Cover == "" {
				enrichment.Cover = image.URL
			}
		}
	}

	if game.ReleaseDate != "" {
		enrichment.ReleaseDate = game.ReleaseDate

		if date, err := parseDate(game.ReleaseDate); err == nil {
			enrichment.Year = date.Year()
		}
	}

	return enrichment, nil
}

type thegamesdbImage struct {
	Type string
	URL  string
}

func (c *thegamesdbClient) images(ctx context.Context, gameID int) ([]thegamesdbImage, error) {
	endpoint := fmt.Sprintf(
		"https://api.thegamesdb.net/v1/Games/Images?apikey=%s&games_id=%d",
		url.QueryEscape(c.key),
		gameID,
	)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Include struct {
			BaseURL struct {
				Original string `json:"original"`
			} `json:"base_url"`
		} `json:"include"`
		Data struct {
			Images map[string][]struct {
				Type     string `json:"type"`
				Filename string `json:"filename"`
				Side     string `json:"side"`
			} `json:"images"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	result := make([]thegamesdbImage, 0)

	for _, list := range payload.Data.Images {
		for _, item := range list {
			if item.Filename == "" {
				continue
			}

			result = append(result, thegamesdbImage{
				Type: item.Type,
				URL:  payload.Include.BaseURL.Original + item.Filename,
			})
		}
	}

	return result, nil
}

func (c *thegamesdbClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("thegamesdb: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thegamesdb: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func parseDate(value string) (time.Time, error) {
	layouts := []string{"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05Z", "2006-01-02T15:04:05.000Z"}

	for _, layout := range layouts {
		if date, err := time.Parse(layout, value); err == nil {
			return date, nil
		}
	}

	return time.Time{}, errors.New("unsupported date format")
}

func (c *longplayClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	var (
		durations []int
		bestURL   string
	)

	if c.key != "" {
		items, err := c.searchYouTube(ctx, title)
		if err == nil {
			durations = items.durations
			bestURL = items.bestURL
		}
	}

	if len(durations) == 0 {
		query := url.QueryEscape(title + " longplay")

		body, err := c.get(ctx, c.base+"/search?q="+query+"&filter=videos")
		if err != nil {
			return nil, err
		}

		var payload struct {
			Items []struct {
				URL      string `json:"url"`
				Title    string `json:"title"`
				Duration int    `json:"duration"`
			} `json:"items"`
		}

		if err := json.Unmarshal(body, &payload); err != nil || len(payload.Items) == 0 {
			return nil, errors.New("longplay: nothing found")
		}

		for _, item := range payload.Items {
			if item.Duration < 600 || item.Duration > 86400 {
				continue
			}

			durations = append(durations, item.Duration)

			if bestURL == "" {
				bestURL = item.URL
			}
		}
	}

	if len(durations) == 0 || bestURL == "" {
		return nil, errors.New("longplay: no suitable videos")
	}

	sort.Ints(durations)

	hastily := float64(durations[0]) / 3600
	normally := float64(durations[len(durations)/2]) / 3600
	completely := float64(durations[len(durations)-1]) / 3600

	return &Enrichment{
		Source:    "longplay",
		Title:     title,
		SourceURL: "https://www.youtube.com" + strings.TrimPrefix(bestURL, "https://pipedapi.kavin.rocks"),
		Completion: &Completion{
			HastilyHours:    math.Round(hastily*10) / 10,
			NormallyHours:   math.Round(normally*10) / 10,
			CompletelyHours: math.Round(completely*10) / 10,
		},
	}, nil
}

func (c *longplayClient) searchYouTube(ctx context.Context, title string) (struct {
	durations []int
	bestURL   string
}, error) {
	var result struct {
		durations []int
		bestURL   string
	}

	searchURL := fmt.Sprintf(
		"https://www.googleapis.com/youtube/v3/search?part=snippet&type=video&maxResults=5&q=%s&key=%s",
		url.QueryEscape(title+" longplay"),
		url.QueryEscape(c.key),
	)

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return result, err
	}

	var search struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &search); err != nil || len(search.Items) == 0 {
		return result, errors.New("longplay: youtube search failed")
	}

	ids := make([]string, 0, len(search.Items))

	for _, item := range search.Items {
		if item.ID.VideoID != "" {
			ids = append(ids, item.ID.VideoID)
		}
	}

	if len(ids) == 0 {
		return result, errors.New("longplay: youtube search empty")
	}

	videosURL := fmt.Sprintf(
		"https://www.googleapis.com/youtube/v3/videos?part=contentDetails&id=%s&key=%s",
		strings.Join(ids, ","),
		url.QueryEscape(c.key),
	)

	body, err = c.get(ctx, videosURL)
	if err != nil {
		return result, err
	}

	var videos struct {
		Items []struct {
			ID             string `json:"id"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &videos); err != nil || len(videos.Items) == 0 {
		return result, errors.New("longplay: youtube durations failed")
	}

	for _, item := range videos.Items {
		seconds := parseISODuration(item.ContentDetails.Duration)

		if seconds < 600 || seconds > 86400 {
			continue
		}

		result.durations = append(result.durations, seconds)

		if result.bestURL == "" {
			result.bestURL = "https://www.youtube.com/watch?v=" + item.ID
		}
	}

	return result, nil
}

func parseISODuration(value string) int {
	if value == "" || value[0] != 'P' {
		return 0
	}

	hours := 0
	minutes := 0
	seconds := 0
	current := 0

	for _, r := range value[1:] {
		switch {
		case r >= '0' && r <= '9':
			current = current*10 + int(r-'0')
		case r == 'H':
			hours = current
			current = 0
		case r == 'M':
			minutes = current
			current = 0
		case r == 'S':
			seconds = current
			current = 0
		case r == 'T':
			current = 0
		}
	}

	return hours*3600 + minutes*60 + seconds
}

func (c *longplayClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("longplay: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("longplay: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}

func (c *screenscraperClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	endpoint := fmt.Sprintf(
		"https://api.screenscraper.fr/api2/jeuRecherche.php?devid=%s&devpassword=%s&softname=%s&output=json&recherche=%s",
		url.QueryEscape(c.devID),
		url.QueryEscape(c.devPassword),
		url.QueryEscape(c.softname),
		url.QueryEscape(title),
	)

	if c.user != "" {
		endpoint += "&ssid=" + url.QueryEscape(c.user) + "&sspassword=" + url.QueryEscape(c.password)
	}

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Response struct {
			Jeux []struct {
				ID   string `json:"id"`
				Noms []struct {
					Region string `json:"region"`
					Text   string `json:"text"`
				} `json:"noms"`
				Medias []struct {
					Type   string `json:"type"`
					URL    string `json:"url"`
					Region string `json:"region"`
				} `json:"medias"`
			} `json:"jeux"`
		} `json:"response"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Response.Jeux) == 0 {
		return nil, errors.New("screenscraper: game not found")
	}

	best := 0
	bestScore := 0.0

	for index, game := range payload.Response.Jeux {
		score := 0.0

		for _, name := range game.Noms {
			if value := titleScore(title, name.Text); value > score {
				score = value
			}
		}

		if score > bestScore {
			bestScore = score
			best = index
		}
	}

	if bestScore < 0.8 {
		return nil, errors.New("screenscraper: no matching page")
	}

	game := payload.Response.Jeux[best]
	enrichment := &Enrichment{
		Source:    "screenscraper",
		Title:     title,
		SourceURL: "https://www.screenscraper.fr/gameinfos.php?gameid=" + game.ID,
	}

	coverRank := 100
	screenshotSeen := false

	for _, media := range game.Medias {
		rank := regionRank(media.Region)

		if (media.Type == "box-2D" || media.Type == "box-2D-front") && rank < coverRank {
			coverRank = rank
			enrichment.Cover = media.URL
		}

		if media.Type == "ss" {
			if !screenshotSeen {
				enrichment.Screenshots = append(enrichment.Screenshots, media.URL)
				screenshotSeen = true
			} else if len(enrichment.Screenshots) < 8 {
				enrichment.Screenshots = append(enrichment.Screenshots, media.URL)
			}
		}
	}

	return enrichment, nil
}

func regionRank(region string) int {
	switch region {
	case "wor":
		return 0
	case "ru":
		return 1
	case "eu":
		return 2
	case "us":
		return 3
	case "jp":
		return 4
	default:
		return 10
	}
}

func (c *screenscraperClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("screenscraper: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("screenscraper: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}
