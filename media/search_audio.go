package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type AudioSearchSource struct {
	URL             string `json:"url"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	DurationSeconds int    `json:"durationSeconds"`
}

type audioSearchCacheEntry struct {
	sources []AudioSearchSource
	expires time.Time
}

var (
	audioSearchTokenPattern = regexp.MustCompile(`[\pL\pN]+`)
	audioSearchCacheMu      sync.Mutex
	audioSearchCache        = make(map[string]audioSearchCacheEntry)
)

// SearchAudio finds playable YouTube alternatives matching an audio title.
// It returns the stable watch URL; the media/play route resolves a fresh stream
// URL when the browser starts playback.
func SearchAudio(ctx context.Context, title, author, excludeURL string) ([]AudioSearchSource, error) {
	title = strings.TrimSpace(title)
	author = strings.TrimSpace(author)
	if title == "" || len([]rune(title)) > 180 {
		return nil, fmt.Errorf("нужно название аудио длиной до 180 символов")
	}

	query := title
	if author != "" && !strings.EqualFold(author, "media") && !strings.EqualFold(author, "unknown") {
		query += " " + author
	}
	if len([]rune(query)) > 240 {
		query = string([]rune(query)[:240])
	}
	cacheKey := strings.ToLower(strings.Join(strings.Fields(query), " ")) + "|" + youtubeIDFromURL(excludeURL)
	if sources, ok := cachedAudioSearch(cacheKey); ok {
		return sources, nil
	}

	ytdlp, err := ytDLPExecutable()
	if err != nil {
		return nil, err
	}
	searchCtx, cancel := context.WithTimeout(ctx, 24*time.Second)
	defer cancel()

	search := func(proxy string) ([]byte, error) {
		attemptCtx := searchCtx
		cancel := func() {}
		attemptLimit := 16 * time.Second
		if proxy != "" {
			attemptLimit = 8 * time.Second
		}
		attemptCtx, cancel = context.WithTimeout(searchCtx, attemptLimit)
		defer cancel()

		args := []string{"--no-warnings", "--flat-playlist", "--dump-single-json", "--playlist-end", "8"}
		if proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		args = append(args, "ytsearch8:"+query)
		command := exec.CommandContext(attemptCtx, ytdlp, args...)
		if proxy == "" {
			command.Env = withoutProxyEnvironment()
		}
		return command.Output()
	}
	proxy := mediaProxyURL()
	output, err := search("")
	if err != nil {
		log.Printf("[Music] direct title search failed: %v", err)
	}
	if err != nil && proxy != "" && searchCtx.Err() == nil {
		output, err = search(proxy)
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось найти аудио по названию")
	}
	if len(output) > 4<<20 {
		return nil, fmt.Errorf("ответ поиска аудио слишком большой")
	}

	var response struct {
		Entries []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			URL      string  `json:"url"`
			Webpage  string  `json:"webpage_url"`
			Channel  string  `json:"channel"`
			Uploader string  `json:"uploader"`
			Duration float64 `json:"duration"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("не удалось разобрать результаты поиска аудио")
	}

	queryTokens := audioTitleTokens(title)
	if len(queryTokens) == 0 {
		return nil, fmt.Errorf("в названии нет слов для поиска")
	}
	excludedID := youtubeIDFromURL(excludeURL)
	type rankedSource struct {
		source AudioSearchSource
		score  float64
	}
	ranked := make([]rankedSource, 0, len(response.Entries))
	for _, entry := range response.Entries {
		id := strings.TrimSpace(entry.ID)
		if !validYouTubeID(id) || id == excludedID || strings.TrimSpace(entry.Title) == "" {
			continue
		}
		score := audioTitleSimilarity(queryTokens, audioTitleTokens(entry.Title))
		if score < 0.35 {
			continue
		}
		author := strings.TrimSpace(entry.Channel)
		if author == "" {
			author = strings.TrimSpace(entry.Uploader)
		}
		ranked = append(ranked, rankedSource{
			source: AudioSearchSource{
				URL:             "https://www.youtube.com/watch?v=" + id,
				Title:           strings.TrimSpace(entry.Title),
				Author:          author,
				DurationSeconds: int(entry.Duration),
			},
			score: score,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	sources := make([]AudioSearchSource, 0, 3)
	for _, candidate := range ranked {
		checkCtx, checkCancel := context.WithTimeout(searchCtx, 15*time.Second)
		_, resolveErr := resolveWithYTDLP(checkCtx, candidate.source.URL)
		checkCancel()
		if resolveErr != nil {
			if searchCtx.Err() != nil {
				break
			}
			continue
		}
		sources = append(sources, candidate.source)
		if len(sources) == 3 {
			break
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("не нашёл рабочую аудиоссылку с похожим названием")
	}
	rememberAudioSearch(cacheKey, sources)
	return cloneAudioSources(sources), nil
}

func cachedAudioSearch(key string) ([]AudioSearchSource, bool) {
	audioSearchCacheMu.Lock()
	defer audioSearchCacheMu.Unlock()
	entry, ok := audioSearchCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(audioSearchCache, key)
		return nil, false
	}
	return cloneAudioSources(entry.sources), true
}

func rememberAudioSearch(key string, sources []AudioSearchSource) {
	audioSearchCacheMu.Lock()
	defer audioSearchCacheMu.Unlock()
	if len(audioSearchCache) >= 256 {
		for oldKey, entry := range audioSearchCache {
			if time.Now().After(entry.expires) || len(audioSearchCache) >= 192 {
				delete(audioSearchCache, oldKey)
			}
			if len(audioSearchCache) < 192 {
				break
			}
		}
	}
	audioSearchCache[key] = audioSearchCacheEntry{sources: cloneAudioSources(sources), expires: time.Now().Add(10 * time.Minute)}
}

func cloneAudioSources(sources []AudioSearchSource) []AudioSearchSource {
	return append([]AudioSearchSource(nil), sources...)
}

func audioTitleTokens(value string) map[string]struct{} {
	tokens := audioSearchTokenPattern.FindAllString(strings.ToLower(value), -1)
	result := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if len([]rune(token)) >= 2 {
			result[token] = struct{}{}
		}
	}
	return result
}

func audioTitleSimilarity(wanted, candidate map[string]struct{}) float64 {
	if len(wanted) == 0 || len(candidate) == 0 {
		return 0
	}
	matched := 0
	for token := range wanted {
		if _, ok := candidate[token]; ok {
			matched++
			continue
		}
		for candidateToken := range candidate {
			if len([]rune(token)) >= 5 && len([]rune(candidateToken)) >= 5 &&
				(strings.Contains(token, candidateToken) || strings.Contains(candidateToken, token)) {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(len(wanted))
}

func youtubeIDFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "youtu.be":
		return strings.Trim(parsed.Path, "/")
	case "youtube.com", "www.youtube.com", "music.youtube.com", "m.youtube.com":
		return parsed.Query().Get("v")
	default:
		return ""
	}
}

func validYouTubeID(id string) bool {
	if len(id) < 6 || len(id) > 20 {
		return false
	}
	for _, char := range id {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
