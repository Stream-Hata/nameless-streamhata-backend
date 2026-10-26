package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

type PopularMod struct {
	Name         string `json:"name"`
	Author       string `json:"author,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Image        string `json:"image,omitempty"`
	URL          string `json:"url"`
	Downloads    int64  `json:"downloads,omitempty"`
	Endorsements int64  `json:"endorsements,omitempty"`
}

type PopularCollection struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Image   string `json:"image,omitempty"`
	URL     string `json:"url"`
	Mods    int    `json:"mods,omitempty"`
}

type nexusClient struct {
	client *http.Client
	mu     sync.Mutex
	games  []nexusGame
	loaded bool
}

type nexusGame struct {
	Name       string `json:"name"`
	DomainName string `json:"domain_name"`
}

func newNexusClient(client *http.Client) *nexusClient {
	return &nexusClient{client: client}
}

func (c *nexusClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	key := strings.TrimSpace(os.Getenv("NEXUS_API_KEY"))
	if key == "" {
		return nil, errors.New("nexus: NEXUS_API_KEY is empty")
	}

	games, err := c.gamesList(ctx, key)
	if err != nil {
		return nil, err
	}

	best := -1
	bestScore := 0.0

	for index, game := range games {
		if game.DomainName == "" {
			continue
		}

		if score := titleScore(title, game.Name); score > bestScore {
			bestScore = score
			best = index
		}
	}

	if best < 0 || bestScore < 0.8 {
		return nil, errors.New("nexus: game page not found")
	}

	game := games[best]
	mods, err := c.trending(ctx, key, game.DomainName)
	if err != nil {
		return nil, err
	}

	collections := c.collections(ctx, key, game.DomainName)

	if len(mods) == 0 && len(collections) == 0 {
		return nil, errors.New("nexus: no mods")
	}

	return &Enrichment{
		Source:             "nexusmods",
		Title:              game.Name,
		SourceURL:          "https://www.nexusmods.com/" + game.DomainName,
		PopularMods:        mods,
		PopularCollections: collections,
	}, nil
}

func (c *nexusClient) gamesList(ctx context.Context, key string) ([]nexusGame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded {
		return c.games, nil
	}

	body, err := c.get(ctx, "https://api.nexusmods.com/v1/games.json", key)
	if err != nil {
		return nil, err
	}

	var games []nexusGame

	if json.Unmarshal(body, &games) != nil {
		return nil, errors.New("nexus: games parse failed")
	}

	c.games = games
	c.loaded = true

	return games, nil
}

func (c *nexusClient) trending(ctx context.Context, key string, domain string) ([]PopularMod, error) {
	target := fmt.Sprintf(
		"https://api.nexusmods.com/v1/games/%s/mods/trending.json?count=6",
		domain,
	)

	body, err := c.get(ctx, target, key)
	if err != nil {
		return nil, err
	}

	var payload []struct {
		ModID        int64  `json:"mod_id"`
		Name         string `json:"name"`
		Summary      string `json:"summary"`
		Author       string `json:"author"`
		PictureURL   string `json:"picture_url"`
		Downloads    int64  `json:"mod_downloads"`
		Endorsements int64  `json:"endorsement_count"`
	}

	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("nexus: mods parse failed")
	}

	mods := make([]PopularMod, 0, len(payload))

	for _, item := range payload {
		if item.Name == "" {
			continue
		}

		mods = append(mods, PopularMod{
			Name:         item.Name,
			Author:       item.Author,
			Summary:      item.Summary,
			Image:        item.PictureURL,
			URL:          fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", domain, item.ModID),
			Downloads:    item.Downloads,
			Endorsements: item.Endorsements,
		})
	}

	return mods, nil
}

func (c *nexusClient) collections(ctx context.Context, key string, domain string) []PopularCollection {
	endpoints := []string{
		fmt.Sprintf("https://api.nexusmods.com/v1/games/%s/collections.json", domain),
		fmt.Sprintf("https://api.nexusmods.com/v1/collections/%s.json", domain),
	}

	for _, target := range endpoints {
		body, err := c.get(ctx, target, key)
		if err != nil {
			continue
		}

		collections := parseCollections(body, domain)
		if len(collections) > 0 {
			return collections
		}
	}

	return nil
}

func parseCollections(body []byte, domain string) []PopularCollection {
	var payload struct {
		Collections []struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
			Image   string `json:"image"`
			URL     string `json:"url"`
			Mods    int    `json:"mods_count"`
			ID      int64  `json:"id"`
			Slug    string `json:"slug"`
		} `json:"collections"`
	}

	if json.Unmarshal(body, &payload) == nil && len(payload.Collections) > 0 {
		result := make([]PopularCollection, 0, 4)

		for _, item := range payload.Collections {
			if item.Name == "" || len(result) >= 4 {
				continue
			}

			url := item.URL

			if url == "" {
				slug := item.Slug

				if slug == "" && item.ID > 0 {
					slug = fmt.Sprintf("%d", item.ID)
				}

				if slug != "" {
					url = fmt.Sprintf("https://www.nexusmods.com/%s/collections/%s", domain, slug)
				}
			}

			result = append(result, PopularCollection{
				Name:    item.Name,
				Summary: item.Summary,
				Image:   item.Image,
				URL:     url,
				Mods:    item.Mods,
			})
		}

		return result
	}

	var list []struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		Image   string `json:"image"`
		URL     string `json:"url"`
		Mods    int    `json:"mods_count"`
	}

	if json.Unmarshal(body, &list) != nil {
		return nil
	}

	result := make([]PopularCollection, 0, 4)

	for _, item := range list {
		if item.Name == "" || len(result) >= 4 {
			continue
		}

		result = append(result, PopularCollection{
			Name:    item.Name,
			Summary: item.Summary,
			Image:   item.Image,
			URL:     item.URL,
			Mods:    item.Mods,
		})
	}

	return result
}

func (c *nexusClient) get(ctx context.Context, target string, key string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("apikey", key)
	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("nexus: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nexus: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 16<<20))
}
