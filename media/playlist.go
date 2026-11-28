package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

type PlaylistTrack struct {
	VideoID         string
	URL             string
	Title           string
	Author          string
	DurationSeconds int
	Thumbnail       string
}

type Playlist struct {
	SourceURL string
	Title     string
	Tracks    []PlaylistTrack
}

func FetchYouTubePlaylist(ctx context.Context, rawURL string) (Playlist, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" {
		return Playlist{}, errors.New("music: нужна HTTPS-ссылка на публичный YouTube-плейлист")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be":
	default:
		return Playlist{}, errors.New("music: поддерживаются публичные плейлисты YouTube и Яндекс Музыки")
	}
	if parsed.Query().Get("list") == "" && !strings.Contains(parsed.Path, "/playlist/") {
		return Playlist{}, errors.New("music: ссылка не содержит идентификатор плейлиста")
	}
	executable, err := ytDLPExecutable()
	if err != nil {
		return Playlist{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	query := func(proxy string, wait time.Duration) ([]byte, error) {
		attempt, stop := context.WithTimeout(ctx, wait)
		defer stop()
		args := []string{"--no-warnings", "--ignore-errors", "--flat-playlist", "--dump-single-json", "--playlist-end", "500"}
		if proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		args = append(args, rawURL)
		command := exec.CommandContext(attempt, executable, args...)
		if proxy == "" {
			command.Env = withoutProxyEnvironment()
		}
		return command.Output()
	}
	output, err := query("", 18*time.Second)
	if err != nil && ctx.Err() == nil {
		if proxy := mediaProxyURL(); proxy != "" {
			output, err = query(proxy, 10*time.Second)
		}
	}
	if err != nil {
		return Playlist{}, errors.New("music: не удалось прочитать YouTube-плейлист")
	}
	if len(output) > 8<<20 {
		return Playlist{}, errors.New("music: ответ плейлиста слишком большой")
	}
	return parseYouTubePlaylist(rawURL, output)
}

func parseYouTubePlaylist(sourceURL string, output []byte) (Playlist, error) {
	var payload struct {
		Title   string `json:"title"`
		Entries []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			URL        string  `json:"url"`
			WebpageURL string  `json:"webpage_url"`
			Channel    string  `json:"channel"`
			Uploader   string  `json:"uploader"`
			Duration   float64 `json:"duration"`
			Thumbnail  string  `json:"thumbnail"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return Playlist{}, errors.New("music: неизвестный формат YouTube-плейлиста")
	}
	if len(payload.Entries) == 0 {
		return Playlist{}, errors.New("music: YouTube-плейлист пуст или закрыт")
	}
	tracks := make([]PlaylistTrack, 0, len(payload.Entries))
	seen := make(map[string]struct{}, len(payload.Entries))
	for _, entry := range payload.Entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.URL)
		}
		if len(id) != 11 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		author := strings.TrimSpace(entry.Channel)
		if author == "" {
			author = strings.TrimSpace(entry.Uploader)
		}
		link := strings.TrimSpace(entry.WebpageURL)
		if link == "" || !strings.HasPrefix(link, "https://") {
			link = "https://www.youtube.com/watch?v=" + id
		}
		tracks = append(tracks, PlaylistTrack{VideoID: id, URL: link, Title: title, Author: author, DurationSeconds: int(entry.Duration), Thumbnail: entry.Thumbnail})
	}
	if len(tracks) == 0 {
		return Playlist{}, fmt.Errorf("music: не удалось прочитать доступные треки YouTube-плейлиста")
	}
	return Playlist{SourceURL: sourceURL, Title: strings.TrimSpace(payload.Title), Tracks: tracks}, nil
}
