package music

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

// iTunesBaseURL вынесен в переменную, чтобы тесты могли подменить сервер.
var iTunesBaseURL = "https://itunes.apple.com"

var nonWordPattern = regexp.MustCompile(`[^\p{L}\p{N}]+`)

type iTunesResponse struct {
	ResultCount int `json:"resultCount"`
	Results     []struct {
		TrackName      string `json:"trackName"`
		ArtistName     string `json:"artistName"`
		CollectionName string `json:"collectionName"`
		ReleaseDate    string `json:"releaseDate"`
	} `json:"results"`
}

// enrichWithITunes ищет альбом и год релиза в каталоге iTunes.
// Это надёжнее, чем вытаскивать альбом со страницы YouTube Music.
func enrichWithITunes(ctx context.Context, title, author string) (string, int) {
	title = strings.TrimSpace(title)
	author = strings.TrimSpace(author)

	if title == "" {
		return "", 0
	}

	query := title

	if author != "" {
		query = author + " " + title
	}

	endpoint := fmt.Sprintf(
		"%s/search?term=%s&entity=song&limit=5&country=US",
		strings.TrimRight(iTunesBaseURL, "/"),
		url.QueryEscape(query),
	)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0
	}

	request.Header.Set("Accept", "application/json")

	response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
	if err != nil {
		return "", 0
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return "", 0
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", 0
	}

	var payload iTunesResponse

	if err := json.Unmarshal(body, &payload); err != nil {
		return "", 0
	}

	bestScore := 0
	bestAlbum := ""
	bestYear := 0

	for _, result := range payload.Results {
		score := matchScore(title, author, result.TrackName, result.ArtistName)

		if score < bestScore {
			continue
		}

		bestScore = score
		bestAlbum = strings.TrimSpace(result.CollectionName)
		bestYear = parseReleaseYear(result.ReleaseDate)
	}

	if bestScore == 0 {
		return "", 0
	}

	return bestAlbum, bestYear
}

func matchScore(title, author, candidateTitle, candidateArtist string) int {
	score := 0
	normalizedTitle := normalizeName(title)
	normalizedCandidate := normalizeName(candidateTitle)

	if normalizedTitle != "" && normalizedCandidate != "" {
		switch {
		case normalizedCandidate == normalizedTitle:
			score += 3
		case strings.Contains(normalizedCandidate, normalizedTitle) ||
			strings.Contains(normalizedTitle, normalizedCandidate):
			score += 2
		}
	}

	normalizedAuthor := normalizeName(author)
	normalizedArtist := normalizeName(candidateArtist)

	if normalizedAuthor != "" && normalizedArtist != "" {
		if strings.Contains(normalizedArtist, normalizedAuthor) ||
			strings.Contains(normalizedAuthor, normalizedArtist) {
			score += 2
		}
	}

	return score
}

func normalizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonWordPattern.ReplaceAllString(value, " ")

	return strings.Join(strings.Fields(value), " ")
}

func parseReleaseYear(value string) int {
	if len(value) < 4 {
		return 0
	}

	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 1900 || year > 2100 {
		return 0
	}

	return year
}
