package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

type yandexPlaylistRef struct {
	owner string
	kind  string
}

func parseYandexPlaylistURL(raw string) (yandexPlaylistRef, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return yandexPlaylistRef{}, errors.New("music: нужна публичная HTTPS-ссылка на плейлист Яндекс Музыки")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "music.yandex.ru" && host != "music.yandex.com" {
		return yandexPlaylistRef{}, errors.New("music: поддерживаются публичные плейлисты music.yandex.ru")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var owner, kind string
	switch {
	case len(parts) == 4 && parts[0] == "users" && parts[2] == "playlists":
		owner, kind = parts[1], parts[3]
	case len(parts) == 4 && parts[0] == "iframe" && parts[1] == "playlist":
		owner, kind = parts[2], parts[3]
	default:
		return yandexPlaylistRef{}, errors.New("music: ссылка должна иметь формат /users/{логин}/playlists/{номер} или /iframe/playlist/{логин}/{номер}")
	}
	if owner == "" || kind == "" || len(owner) > 100 || len(kind) > 100 {
		return yandexPlaylistRef{}, errors.New("music: некорректный адрес плейлиста")
	}
	return yandexPlaylistRef{owner: owner, kind: kind}, nil
}

func FetchYandexPlaylist(ctx context.Context, rawURL string) (StreamerPlaylist, error) {
	ref, err := parseYandexPlaylistURL(rawURL)
	if err != nil {
		return StreamerPlaylist{}, err
	}
	endpoint := "https://music.yandex.ru/handlers/playlist.jsx?" + url.Values{
		"owner": {ref.owner}, "kinds": {ref.kind}, "light": {"true"},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return StreamerPlaylist{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; StreamHata/1.0)")
	client := netcfg.NewHTTPClient(18 * time.Second)
	response, err := client.Do(request)
	if err != nil {
		return StreamerPlaylist{}, errors.New("music: не удалось загрузить плейлист Яндекс Музыки")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return StreamerPlaylist{}, fmt.Errorf("music: Яндекс Музыка ответила HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return StreamerPlaylist{}, errors.New("music: не удалось прочитать ответ Яндекс Музыки")
	}
	var payload struct {
		Playlist struct {
			Title  string            `json:"title"`
			Tracks []json.RawMessage `json:"tracks"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return StreamerPlaylist{}, errors.New("music: ответ Яндекс Музыки имеет неизвестный формат")
	}
	if len(payload.Playlist.Tracks) == 0 {
		return StreamerPlaylist{}, errors.New("music: плейлист пуст или закрыт")
	}
	if len(payload.Playlist.Tracks) > 1000 {
		return StreamerPlaylist{}, errors.New("music: в плейлисте больше 1000 треков")
	}
	tracks := make([]Track, 0, len(payload.Playlist.Tracks))
	for _, entry := range payload.Playlist.Tracks {
		var wrapper struct {
			Track json.RawMessage `json:"track"`
		}
		if json.Unmarshal(entry, &wrapper) == nil && len(wrapper.Track) > 0 {
			entry = wrapper.Track
		}
		var value struct {
			ID         json.RawMessage `json:"id"`
			Title      string          `json:"title"`
			DurationMS int             `json:"durationMs"`
			CoverURI   string          `json:"coverUri"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Albums []struct {
				ID       json.RawMessage `json:"id"`
				Title    string          `json:"title"`
				Year     int             `json:"year"`
				CoverURI string          `json:"coverUri"`
			} `json:"albums"`
		}
		if json.Unmarshal(entry, &value) != nil {
			continue
		}
		id := strings.Trim(strings.TrimSpace(string(value.ID)), "\"")
		if id == "" || value.Title == "" {
			continue
		}
		artistNames := make([]string, 0, len(value.Artists))
		for _, artist := range value.Artists {
			if name := strings.TrimSpace(artist.Name); name != "" {
				artistNames = append(artistNames, name)
			}
		}
		album, albumID, year, cover := "", "", 0, value.CoverURI
		if len(value.Albums) > 0 {
			album = value.Albums[0].Title
			albumID = strings.Trim(strings.TrimSpace(string(value.Albums[0].ID)), "\"")
			year = value.Albums[0].Year
			if cover == "" {
				cover = value.Albums[0].CoverURI
			}
		}
		if cover != "" {
			cover = strings.ReplaceAll(cover, "%%", "200x200")
			if strings.HasPrefix(cover, "//") {
				cover = "https:" + cover
			} else if !strings.HasPrefix(cover, "http://") && !strings.HasPrefix(cover, "https://") {
				cover = "https://" + cover
			}
		}
		trackURL := "https://music.yandex.ru/track/" + id
		if albumID != "" {
			trackURL = "https://music.yandex.ru/album/" + albumID + "/track/" + id
		}
		tracks = append(tracks, Track{VideoID: "ym-" + id, Kind: "yandex", URL: trackURL, Title: strings.TrimSpace(value.Title), Author: strings.Join(artistNames, ", "), Album: album, Year: year, DurationSeconds: value.DurationMS / 1000, Thumbnail: cover, Links: []string{trackURL}, FirstAddedAt: time.Now().UTC(), LastAddedAt: time.Now().UTC(), TimesAdded: 1})
	}
	if len(tracks) == 0 {
		return StreamerPlaylist{}, errors.New("music: не удалось прочитать треки плейлиста")
	}
	return StreamerPlaylist{SourceURL: rawURL, Title: payload.Playlist.Title, Tracks: tracks}, nil
}
