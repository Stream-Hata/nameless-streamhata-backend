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

type gogClient struct {
	client *http.Client
}

func newGOGClient(client *http.Client) *gogClient {
	return &gogClient{client: client}
}

func (c *gogClient) Search(ctx context.Context, title string) (*Enrichment, error) {
	searchURL := "https://catalog.gog.com/v1/catalog?limit=20&query=like:" + url.QueryEscape(title) +
		"&countryCode=US&locale=en-US&currencyCode=USD"

	body, err := c.get(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	var result struct {
		Products []struct {
			Title           string   `json:"title"`
			Slug            string   `json:"slug"`
			StoreLink       string   `json:"storeLink"`
			CoverVertical   string   `json:"coverVertical"`
			CoverHorizontal string   `json:"coverHorizontal"`
			Developers      []string `json:"developers"`
			Publishers      []string `json:"publishers"`
			Genres          []struct {
				Name string `json:"name"`
			} `json:"genres"`
			ReleaseDate string `json:"releaseDate"`
			ProductType string `json:"productType"`
		} `json:"products"`
	}

	if json.Unmarshal(body, &result) != nil || len(result.Products) == 0 {
		return nil, errors.New("gog: game not found")
	}

	best := -1
	bestScore := 0.0

	for index, product := range result.Products {
		if product.ProductType != "game" && product.ProductType != "pack" {
			continue
		}

		if score := titleScore(title, strings.TrimSpace(product.Title)); score > bestScore {
			bestScore = score
			best = index
		}
	}

	if best < 0 || bestScore < 0.8 {
		return nil, errors.New("gog: no matching page")
	}

	product := result.Products[best]
	enrichment := &Enrichment{
		Source: "gog",
		Title:  strings.TrimSpace(product.Title),
	}

	if product.StoreLink != "" {
		if strings.HasPrefix(product.StoreLink, "http") {
			enrichment.SourceURL = product.StoreLink
		} else {
			enrichment.SourceURL = "https://www.gog.com" + product.StoreLink
		}
	} else {
		enrichment.SourceURL = "https://www.gog.com/game/" + product.Slug
	}

	enrichment.Cover = firstNonEmpty(product.CoverVertical, product.CoverHorizontal)

	if enrichment.Cover == "" {
		return nil, errors.New("gog: no cover")
	}

	enrichment.Developers = product.Developers
	enrichment.Publishers = product.Publishers

	for _, genre := range product.Genres {
		enrichment.Genres = append(enrichment.Genres, genre.Name)
	}

	enrichment.Year = extractYear(product.ReleaseDate)

	return enrichment, nil
}

func (c *gogClient) get(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/131.0 Safari/537.36")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, errors.New("gog: request failed")
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gog: status %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 8<<20))
}
