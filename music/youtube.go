package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var linkPattern = regexp.MustCompile(
	`(?:youtube\.com/(?:watch\?(?:[^\s"']*&)?v=|shorts/|live/|embed/)|youtu\.be/|music\.youtube\.com/watch\?(?:[^\s"']*&)?v=)([A-Za-z0-9_-]{6,20})`,
)

// ExtractVideoID вытаскивает id ролика YouTube из произвольного текста.
func ExtractVideoID(text string) (string, bool) {
	match := linkPattern.FindStringSubmatch(text)

	if len(match) < 2 {
		return "", false
	}

	parsed, err := url.Parse("https://" + match[0])
	if err != nil {
		return "", false
	}

	host := strings.ToLower(parsed.Hostname())
	allowed := map[string]bool{
		"youtube.com":       true,
		"www.youtube.com":   true,
		"m.youtube.com":     true,
		"music.youtube.com": true,
		"youtu.be":          true,
	}

	if !allowed[host] {
		return "", false
	}

	return match[1], true
}

var mediaPattern = regexp.MustCompile(`https?://[^\s<>"']+\.(?:mp3|m4a|aac|ogg|wav|flac|mp4|webm|mov|mkv)(?:\?[^\s<>"']*)?`)

func ExtractLink(text string) (string, string, bool) {
	if videoID, ok := ExtractVideoID(text); ok {
		return "https://www.youtube.com/watch?v=" + videoID, "youtube", true
	}

	match := mediaPattern.FindString(text)
	if match == "" {
		return "", "", false
	}

	lower := strings.ToLower(match)

	switch {
	case strings.Contains(lower, ".mp4"), strings.Contains(lower, ".webm"), strings.Contains(lower, ".mov"), strings.Contains(lower, ".mkv"):
		return match, "video", true
	default:
		return match, "audio", true
	}
}

// VideoMeta — метаданные трека, которые мы добываем без API-ключей.
type VideoMeta struct {
	VideoID         string `json:"videoId"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	Album           string `json:"album"`
	Year            int    `json:"year"`
	DurationSeconds int    `json:"durationSeconds"`
	Thumbnail       string `json:"thumbnail"`
}

// Fetcher — функция получения метаданных (подменяется в тестах).
type Fetcher func(ctx context.Context, videoID string) (*VideoMeta, error)

// FetchVideoMeta парсит страницу ролика YouTube (ytInitialPlayerResponse).
func FetchVideoMeta(ctx context.Context, videoID string) (*VideoMeta, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://www.youtube.com/watch?v="+videoID+"&hl=ru",
		nil,
	)
	if err != nil {
		return nil, err
	}

	request.Header.Set(
		"User-Agent",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
	)
	request.Header.Set("Accept-Language", "ru,en;q=0.8")

	client := netcfg.NewHTTPClient(15 * time.Second)

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube: watch page returned %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	payload := extractPlayerResponse(string(body))

	if payload == "" {
		return nil, errors.New("youtube: player response not found")
	}

	var parsed struct {
		VideoDetails struct {
			VideoID       string `json:"videoId"`
			Title         string `json:"title"`
			Author        string `json:"author"`
			LengthSeconds string `json:"lengthSeconds"`
			Thumbnail     struct {
				Thumbnails []struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"thumbnail"`
		} `json:"videoDetails"`

		Microformat struct {
			PlayerMicroformatRenderer struct {
				PublishDate string `json:"publishDate"`
			} `json:"playerMicroformatRenderer"`
		} `json:"microformat"`
	}

	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil, err
	}

	details := parsed.VideoDetails

	if details.Title == "" {
		return nil, errors.New("youtube: empty video details")
	}

	meta := &VideoMeta{
		VideoID: videoID,
		Title:   strings.TrimSpace(details.Title),
		Author:  strings.TrimSpace(details.Author),
	}

	if len(details.Thumbnail.Thumbnails) > 0 {
		meta.Thumbnail = details.Thumbnail.Thumbnails[len(details.Thumbnail.Thumbnails)-1].URL
	}

	_, _ = fmt.Sscanf(details.LengthSeconds, "%d", &meta.DurationSeconds)

	meta.Year = parseYear(parsed.Microformat.PlayerMicroformatRenderer.PublishDate)

	// iTunes знает альбом и год релиза; YouTube Music-страница — только fallback.
	if album, year := enrichWithITunes(ctx, meta.Title, meta.Author); album != "" || year != 0 {
		meta.Album = album

		if year != 0 {
			meta.Year = year
		}
	}

	if meta.Album == "" {
		meta.Album = fetchAlbum(ctx, videoID)
	}

	return meta, nil
}

// parseYear выделяет год из publishDate/«Released on».
func parseYear(value string) int {
	year := 0

	for i := 0; i+4 <= len(value); i++ {
		chunk := value[i : i+4]

		if parsed, err := strconv.Atoi(chunk); err == nil && parsed >= 1900 && parsed <= 2100 {
			year = parsed

			break
		}
	}

	return year
}

var albumPattern = regexp.MustCompile(`"album"\s*:\s*\{\s*"name"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// fetchAlbum пытается достать название альбома со страницы YouTube Music.
func fetchAlbum(ctx context.Context, videoID string) string {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://music.youtube.com/watch?v="+videoID,
		nil,
	)
	if err != nil {
		return ""
	}

	request.Header.Set(
		"User-Agent",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
	)
	request.Header.Set("Accept-Language", "ru,en;q=0.8")

	client := netcfg.NewHTTPClient(12 * time.Second)

	response, err := client.Do(request)
	if err != nil {
		return ""
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return ""
	}

	match := albumPattern.FindStringSubmatch(string(body))
	if len(match) < 2 {
		return ""
	}

	album := match[1]

	if decoded, err := strconv.Unquote(`"` + album + `"`); err == nil {
		album = decoded
	}

	return strings.TrimSpace(album)
}

// extractPlayerResponse вырезает JSON-объект ytInitialPlayerResponse,
// отслеживая баланс фигурных скобок вне строковых литералов.
func extractPlayerResponse(page string) string {
	for _, marker := range []string{
		"ytInitialPlayerResponse = ",
		`window["ytInitialPlayerResponse"] = `,
	} {
		index := strings.Index(page, marker)

		if index < 0 {
			continue
		}

		start := index + len(marker)

		if start >= len(page) || page[start] != '{' {
			continue
		}

		return scanBalancedJSON(page[start:])
	}

	return ""
}

func scanBalancedJSON(input string) string {
	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(input); i++ {
		char := input[i]

		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}

			continue
		}

		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--

			if depth == 0 {
				return input[:i+1]
			}
		}
	}

	return ""
}
