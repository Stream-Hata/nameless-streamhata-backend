package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type gamebananaClient struct {
	client *http.Client
}

type gamebananaGame struct {
	ID   int    `json:"_idRow"`
	Name string `json:"_sName"`
}

func newGameBananaClient(client *http.Client) *gamebananaClient {
	return &gamebananaClient{client: client}
}

func (c *gamebananaClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	var game gamebananaGame
	found := false

	for _, candidate := range searchCandidates(title) {
		searchURL := "https://gamebanana.com/apiv11/Util/Game/NameMatch?_sName=" + url.QueryEscape(candidate)

		body, err := c.get(ctx, searchURL)
		if err != nil {
			continue
		}

		var search struct {
			Records []gamebananaGame `json:"_aRecords"`
		}

		if json.Unmarshal(body, &search) != nil || len(search.Records) == 0 {
			continue
		}

		best := -1
		bestScore := 0.0

		for index, item := range search.Records {
			if score := titleScore(title, item.Name); score > bestScore {
				bestScore = score
				best = index
			}
		}

		if best >= 0 && bestScore >= 0.8 {
			game = search.Records[best]
			found = true

			break
		}
	}

	if !found {
		return nil, errors.New("gamebanana: no matching page")
	}
	mods, err := c.mods(ctx, game.ID)
	if err != nil {
		return nil, err
	}

	if len(mods) == 0 {
		return nil, errors.New("gamebanana: no mods")
	}

	return &Enrichment{
		Source:      "gamebanana",
		Title:       game.Name,
		SourceURL:   fmt.Sprintf("https://gamebanana.com/games/%d", game.ID),
		PopularMods: mods,
	}, nil
}

func (c *gamebananaClient) mods(ctx context.Context, gameID int) ([]PopularMod, error) {
	if mods, err := c.topMods(ctx, gameID); err == nil && len(mods) > 0 {
		return mods, nil
	}

	target := fmt.Sprintf(
		"https://gamebanana.com/apiv11/Game/%d/Subfeed?_nPage=1&_sSort=default",
		gameID,
	)

	body, err := c.get(ctx, target)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Records []struct {
			ID        int    `json:"_idRow"`
			Name      string `json:"_sName"`
			URL       string `json:"_sProfileUrl"`
			Views     int64  `json:"_nViewCount"`
			Category  string `json:"_sSingularTitle"`
			Submitter struct {
				Name string `json:"_sName"`
			} `json:"_aSubmitter"`
			Preview struct {
				Images []struct {
					BaseURL string `json:"_sBaseUrl"`
					File    string `json:"_sFile"`
				} `json:"_aImages"`
			} `json:"_aPreviewMedia"`
		} `json:"_aRecords"`
	}

	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("gamebanana: mods parse failed")
	}

	mods := make([]PopularMod, 0, 6)

	for _, item := range payload.Records {
		if item.Name == "" || item.URL == "" || len(mods) >= 6 {
			continue
		}

		mod := PopularMod{
			Name:      item.Name,
			Author:    item.Submitter.Name,
			URL:       item.URL,
			Downloads: item.Views,
		}

		for _, image := range item.Preview.Images {
			if image.BaseURL != "" && image.File != "" {
				mod.Image = strings.TrimRight(image.BaseURL, "/") + "/" + image.File

				break
			}
		}

		mods = append(mods, mod)
	}

	return mods, nil
}

func (c *gamebananaClient) topMods(ctx context.Context, gameID int) ([]PopularMod, error) {
	target := fmt.Sprintf("https://gamebanana.com/apiv11/Game/%d/TopSubs", gameID)

	body, err := c.get(ctx, target)
	if err != nil {
		return nil, err
	}

	var payload []struct {
		Name      string `json:"_sName"`
		URL       string `json:"_sProfileUrl"`
		Image     string `json:"_sImageUrl"`
		Thumbnail string `json:"_sThumbnailUrl"`
		Likes     int64  `json:"_nLikeCount"`
		Submitter struct {
			Name string `json:"_sName"`
		} `json:"_aSubmitter"`
	}

	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("gamebanana: top mods parse failed")
	}

	mods := make([]PopularMod, 0, 6)

	for _, item := range payload {
		if item.Name == "" || item.URL == "" || len(mods) >= 6 {
			continue
		}

		mods = append(mods, PopularMod{
			Name:         item.Name,
			Author:       item.Submitter.Name,
			Image:        firstNonEmpty(item.Image, item.Thumbnail),
			URL:          item.URL,
			Endorsements: item.Likes,
		})
	}

	return mods, nil
}

func (c *gamebananaClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "NamelessStreamHata/1.0")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("gamebanana: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gamebanana: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}
