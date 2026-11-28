package music

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/p7x-dev/nameless-streamhata-backend/media"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

// Track — трек, найденный в чате или добавленный вручную.
type Track struct {
	VideoID         string    `json:"videoId"`
	Kind            string    `json:"kind,omitempty"`
	URL             string    `json:"url"`
	Title           string    `json:"title"`
	Author          string    `json:"author"`
	Album           string    `json:"album"`
	Year            int       `json:"year"`
	DurationSeconds int       `json:"durationSeconds"`
	Thumbnail       string    `json:"thumbnail"`
	Links           []string  `json:"links"`
	AddedBy         string    `json:"addedBy"`
	FirstAddedAt    time.Time `json:"firstAddedAt"`
	LastAddedAt     time.Time `json:"lastAddedAt"`
	TimesAdded      int       `json:"timesAdded"`
}

func buildLinks(videoID, url string) []string {
	links := make([]string, 0, 2)

	if url != "" {
		links = append(links, url)
	}

	links = append(links, "https://music.youtube.com/watch?v="+videoID)

	return links
}

type Stats struct {
	TotalTracks   int `json:"totalTracks"`
	TotalDuration int `json:"totalDurationSeconds"`
	UniqueAuthors int `json:"uniqueAuthors"`
}

type StreamerPlaylist struct {
	SourceURL    string  `json:"sourceUrl"`
	Title        string  `json:"title"`
	Tracks       []Track `json:"tracks"`
	CurrentIndex int     `json:"currentIndex"`
}

// Service — трек-лист стрима: парсит ссылки из чата, хранит и экспортирует.
type Service struct {
	mu      sync.RWMutex
	tracks  map[string]*Track
	current string
	enabled bool

	db           *sql.DB
	channelID    string
	streamerList StreamerPlaylist
	favorites    map[string]Track
	fetcher      Fetcher
	telegram     TelegramSender
	telegramPage TelegramPageSender
}

func NewService(db *sql.DB) *Service {
	return NewForChannel(db, "")
}

func NewForChannel(db *sql.DB, channelID string) *Service {
	service := &Service{
		tracks:       make(map[string]*Track),
		enabled:      true,
		db:           db,
		channelID:    channelID,
		streamerList: StreamerPlaylist{Tracks: []Track{}},
		favorites:    make(map[string]Track),
		fetcher:      FetchVideoMeta,
	}

	service.loadFromDB()
	service.loadCurrent()
	service.loadStreamerPlaylist()
	service.loadFavorites()

	return service
}

func (s *Service) StreamerPlaylist() StreamerPlaylist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	playlist := s.streamerList
	playlist.Tracks = append([]Track(nil), playlist.Tracks...)
	return playlist
}

// SetStreamerPlaylistIndex persists the index of the streamer track currently
// playing so a widget reload or a viewer request won't reset the playlist.
func (s *Service) SetStreamerPlaylistIndex(index int) int {
	s.mu.Lock()
	if len(s.streamerList.Tracks) == 0 {
		index = 0
	} else {
		index = index % len(s.streamerList.Tracks)
		if index < 0 {
			index += len(s.streamerList.Tracks)
		}
	}
	s.streamerList.CurrentIndex = index
	snapshot := s.streamerList
	s.mu.Unlock()
	s.persistStreamerPlaylist(snapshot)
	return index
}

func (s *Service) ImportStreamerPlaylist(ctx context.Context, playlistURL string) (StreamerPlaylist, error) {
	var playlist StreamerPlaylist
	if _, err := parseYandexPlaylistURL(playlistURL); err == nil {
		playlist, err = FetchYandexPlaylist(ctx, playlistURL)
		if err != nil {
			return StreamerPlaylist{}, err
		}
	} else {
		fetched, fetchErr := media.FetchYouTubePlaylist(ctx, playlistURL)
		if fetchErr != nil {
			return StreamerPlaylist{}, fetchErr
		}
		playlist = StreamerPlaylist{SourceURL: fetched.SourceURL, Title: fetched.Title, Tracks: make([]Track, 0, len(fetched.Tracks))}
		for _, item := range fetched.Tracks {
			playlist.Tracks = append(playlist.Tracks, Track{VideoID: item.VideoID, Kind: "youtube", URL: item.URL, Title: item.Title, Author: item.Author, DurationSeconds: item.DurationSeconds, Thumbnail: item.Thumbnail, Links: buildLinks(item.VideoID, item.URL), FirstAddedAt: time.Now().UTC(), LastAddedAt: time.Now().UTC(), TimesAdded: 1})
		}
	}
	s.mu.Lock()
	known := make(map[string]struct{}, len(s.streamerList.Tracks))
	tracks := append([]Track(nil), s.streamerList.Tracks...)
	for _, track := range tracks {
		known[track.VideoID] = struct{}{}
	}
	for _, track := range playlist.Tracks {
		if _, exists := known[track.VideoID]; exists {
			continue
		}
		known[track.VideoID] = struct{}{}
		tracks = append(tracks, track)
	}
	index := s.streamerList.CurrentIndex
	if len(tracks) == 0 || index >= len(tracks) {
		index = 0
	}
	s.streamerList = StreamerPlaylist{SourceURL: playlist.SourceURL, Title: playlist.Title, Tracks: tracks, CurrentIndex: index}
	snapshot := s.streamerList
	s.mu.Unlock()
	s.persistStreamerPlaylist(snapshot)
	return snapshot, nil
}

func (s *Service) AddStreamerTrack(ctx context.Context, rawURL string) (StreamerPlaylist, error) {
	var track Track
	if videoID, ok := ExtractVideoID(rawURL); ok {
		meta, err := s.fetcher(ctx, videoID)
		if err != nil {
			return StreamerPlaylist{}, err
		}
		track = Track{VideoID: videoID, Kind: "youtube", URL: "https://www.youtube.com/watch?v=" + videoID, Title: meta.Title, Author: meta.Author, Album: meta.Album, Year: meta.Year, DurationSeconds: meta.DurationSeconds, Thumbnail: meta.Thumbnail, Links: buildLinks(videoID, "https://www.youtube.com/watch?v="+videoID), TimesAdded: 1}
	} else if mediaURL, kind, ok := ExtractLink(rawURL); ok && kind != "youtube" {
		sum := sha1.Sum([]byte(mediaURL))
		id := "media-" + hex.EncodeToString(sum[:])[:16]
		track = Track{VideoID: id, Kind: kind, URL: mediaURL, Title: path.Base(strings.Split(mediaURL, "?")[0]), Author: "media", Links: []string{mediaURL}, TimesAdded: 1}
	} else {
		return StreamerPlaylist{}, errors.New("music: добавьте ссылку на YouTube-трек или аудиофайл")
	}
	if track.Title == "" || track.Title == "." || track.Title == "/" {
		track.Title = track.URL
	}
	now := time.Now().UTC()
	track.FirstAddedAt = now
	track.LastAddedAt = now
	s.mu.Lock()
	for _, current := range s.streamerList.Tracks {
		if current.VideoID == track.VideoID {
			snapshot := s.streamerList
			s.mu.Unlock()
			return snapshot, nil
		}
	}
	s.streamerList.Tracks = append(s.streamerList.Tracks, track)
	snapshot := s.streamerList
	s.mu.Unlock()
	s.persistStreamerPlaylist(snapshot)
	return snapshot, nil
}

func (s *Service) RemoveStreamerTrack(videoID string) bool {
	s.mu.Lock()
	tracks := s.streamerList.Tracks
	filtered := make([]Track, 0, len(tracks))
	found := false
	removedIndex := -1
	for index, track := range tracks {
		if track.VideoID == videoID {
			found = true
			removedIndex = index
			continue
		}
		filtered = append(filtered, track)
	}
	if found {
		s.streamerList.Tracks = filtered
		if removedIndex < s.streamerList.CurrentIndex {
			s.streamerList.CurrentIndex--
		}
		if len(filtered) == 0 || s.streamerList.CurrentIndex >= len(filtered) {
			s.streamerList.CurrentIndex = 0
		}
	}
	snapshot := s.streamerList
	s.mu.Unlock()
	if found {
		s.persistStreamerPlaylist(snapshot)
	}
	return found
}

func (s *Service) Favorites() []Track {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Track, 0, len(s.favorites))
	for _, track := range s.favorites {
		result = append(result, track)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Title < result[j].Title })
	return result
}

func (s *Service) SetFavorite(track Track, favorite bool) error {
	if track.VideoID == "" {
		return errors.New("music: invalid favorite track")
	}
	s.mu.Lock()
	if favorite {
		s.favorites[track.VideoID] = track
	} else {
		delete(s.favorites, track.VideoID)
	}
	s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	if !favorite {
		_, err := s.db.ExecContext(context.Background(), `DELETE FROM streamer_music_favorite WHERE channel_id=$1 AND video_id=$2`, s.channelID, track.VideoID)
		return err
	}
	raw, err := json.Marshal(track)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(context.Background(), `INSERT INTO streamer_music_favorite(channel_id,video_id,track) VALUES($1,$2,$3) ON CONFLICT(channel_id,video_id) DO UPDATE SET track=EXCLUDED.track`, s.channelID, track.VideoID, raw)
	return err
}

func (s *Service) SetFavoriteByID(videoID string, favorite bool) error {
	if videoID == "" {
		return errors.New("music: invalid favorite track")
	}
	if !favorite {
		s.mu.Lock()
		delete(s.favorites, videoID)
		s.mu.Unlock()
		if s.db == nil {
			return nil
		}
		_, err := s.db.ExecContext(context.Background(), `DELETE FROM streamer_music_favorite WHERE channel_id=$1 AND video_id=$2`, s.channelID, videoID)
		return err
	}
	s.mu.RLock()
	track, ok := s.tracks[videoID]
	if !ok {
		for _, item := range s.streamerList.Tracks {
			if item.VideoID == videoID {
				track = &item
				ok = true
				break
			}
		}
	}
	var snapshot Track
	if ok {
		snapshot = *track
	}
	s.mu.RUnlock()
	if !ok {
		return errors.New("music: track not found")
	}
	return s.SetFavorite(snapshot, favorite)
}

func (s *Service) IsFavorite(videoID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.favorites[videoID]
	return ok
}

func (s *Service) persistStreamerPlaylist(playlist StreamerPlaylist) {
	if s.db == nil {
		return
	}
	raw, err := json.Marshal(playlist.Tracks)
	if err != nil {
		return
	}
	_, err = s.db.ExecContext(context.Background(), `INSERT INTO streamer_music_playlist(channel_id,source_url,title,tracks,current_index) VALUES($1,$2,$3,$4,$5) ON CONFLICT(channel_id) DO UPDATE SET source_url=EXCLUDED.source_url,title=EXCLUDED.title,tracks=EXCLUDED.tracks,current_index=EXCLUDED.current_index,updated_at=now()`, s.channelID, playlist.SourceURL, playlist.Title, raw, playlist.CurrentIndex)
	if err != nil {
		log.Printf("[Music] streamer playlist persist failed: %v", err)
	}
}

func (s *Service) loadStreamerPlaylist() {
	if s.db == nil {
		return
	}
	var sourceURL, title string
	var raw []byte
	var currentIndex int
	if err := s.db.QueryRowContext(context.Background(), `SELECT source_url,title,tracks,current_index FROM streamer_music_playlist WHERE channel_id=$1`, s.channelID).Scan(&sourceURL, &title, &raw, &currentIndex); err != nil {
		return
	}
	var tracks []Track
	if json.Unmarshal(raw, &tracks) != nil {
		return
	}
	s.mu.Lock()
	if len(tracks) == 0 || currentIndex < 0 || currentIndex >= len(tracks) {
		currentIndex = 0
	}
	s.streamerList = StreamerPlaylist{SourceURL: sourceURL, Title: title, Tracks: tracks, CurrentIndex: currentIndex}
	s.mu.Unlock()
}

func (s *Service) loadFavorites() {
	if s.db == nil {
		return
	}
	rows, err := s.db.QueryContext(context.Background(), `SELECT track FROM streamer_music_favorite WHERE channel_id=$1`, s.channelID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var track Track
		if rows.Scan(&raw) == nil && json.Unmarshal(raw, &track) == nil {
			s.favorites[track.VideoID] = track
		}
	}
}

func (s *Service) ID() string {
	return "music"
}

func (s *Service) Name() string {
	return "Парсер музыки"
}

func (s *Service) Kind() string {
	return "music"
}

func (s *Service) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.enabled = enabled

	return nil
}

func (s *Service) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return fmt.Sprintf("треков: %d", len(s.tracks))
}

// HandleChatMessage ищет YouTube-ссылку в сообщении и добавляет трек.
func (s *Service) HandleChatMessage(ctx context.Context, message twitch.TwitchChatMessage) {
	if !s.Enabled() {
		return
	}

	addedBy := message.Author.DisplayName

	if addedBy == "" {
		addedBy = message.Author.Login
	}

	videoID, ok := ExtractVideoID(message.Text)

	if !ok {
		s.addMediaFromText(message.Text, addedBy)

		return
	}

	if _, err := s.Add(ctx, videoID, "https://www.youtube.com/watch?v="+videoID, addedBy); err != nil {
		log.Printf("[Music] failed to add %s: %v", videoID, err)
	}

	return
}

func (s *Service) addMediaFromText(text, addedBy string) {
	url, kind, ok := ExtractLink(text)
	if !ok || kind == "youtube" {
		return
	}

	if _, err := s.AddMedia(url, kind, addedBy); err != nil {
		log.Printf("[Music] failed to add media: %v", err)
	}
}

func (s *Service) AddMedia(url, kind, addedBy string) (*Track, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("music: empty media url")
	}

	sum := sha1.Sum([]byte(url))
	videoID := "media-" + hex.EncodeToString(sum[:])[:16]
	title := path.Base(strings.Split(url, "?")[0])
	if title == "" || title == "." || title == "/" {
		title = url
	}

	now := time.Now().UTC()

	s.mu.Lock()
	if track, exists := s.tracks[videoID]; exists {
		track.TimesAdded++
		track.LastAddedAt = now
		snapshot := *track
		s.mu.Unlock()
		s.persist(snapshot)

		return &snapshot, nil
	}

	track := &Track{
		VideoID:         videoID,
		Kind:            kind,
		URL:             url,
		Title:           title,
		Author:          "media",
		DurationSeconds: 0,
		Thumbnail:       "",
		Links:           []string{url},
		AddedBy:         addedBy,
		FirstAddedAt:    now,
		LastAddedAt:     now,
		TimesAdded:      1,
	}
	s.tracks[videoID] = track
	snapshot := *track
	s.mu.Unlock()
	s.persist(snapshot)

	return &snapshot, nil
}

// SetCurrent запоминает трек, который сейчас играет в оверлее.
func (s *Service) SetCurrent(videoID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if videoID == "" {
		s.current = ""
		s.persistCurrentLocked()

		return
	}

	if _, ok := s.tracks[videoID]; ok {
		s.current = videoID
		s.persistCurrentLocked()
	}
}

func (s *Service) currentKey() string {
	if s.channelID == "" {
		return "music_current"
	}
	return "music_current:" + s.channelID
}

func (s *Service) loadCurrent() {
	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = $1`, s.currentKey()).Scan(&raw); err != nil {
		return
	}

	var videoID string
	if json.Unmarshal(raw, &videoID) != nil {
		videoID = strings.Trim(string(raw), "\"")
	}

	if _, ok := s.tracks[videoID]; ok {
		s.current = videoID
	}
}

func (s *Service) persistCurrentLocked() {
	if s.db == nil {
		return
	}

	raw, err := json.Marshal(s.current)
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, s.currentKey(), string(raw))
}

// Current возвращает текущий трек, если он ещё есть в списке.
func (s *Service) Current() *Track {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.current == "" {
		return nil
	}

	track, ok := s.tracks[s.current]
	if !ok {
		return nil
	}

	snapshot := *track

	return &snapshot
}

// Add получает метаданные трека и сохраняет его в список (upsert по videoId).
func (s *Service) Add(ctx context.Context, videoID, url string, addedBy string) (*Track, error) {
	if videoID == "" {
		return nil, errors.New("music: empty video id")
	}

	meta, err := s.fetcher(ctx, videoID)
	if err != nil {
		return nil, err
	}

	if url == "" {
		url = "https://www.youtube.com/watch?v=" + videoID
	}

	now := time.Now().UTC()

	s.mu.Lock()
	track, exists := s.tracks[videoID]

	if exists {
		track.TimesAdded++
		track.LastAddedAt = now

		if addedBy != "" {
			track.AddedBy = addedBy
		}
	} else {
		track = &Track{
			VideoID:         meta.VideoID,
			URL:             url,
			Title:           meta.Title,
			Author:          meta.Author,
			Album:           meta.Album,
			Year:            meta.Year,
			DurationSeconds: meta.DurationSeconds,
			Thumbnail:       meta.Thumbnail,
			Links:           buildLinks(meta.VideoID, url),
			AddedBy:         addedBy,
			FirstAddedAt:    now,
			LastAddedAt:     now,
			TimesAdded:      1,
		}

		s.tracks[videoID] = track
	}

	snapshot := *track
	s.mu.Unlock()

	s.persist(snapshot)

	return &snapshot, nil
}

// QueueByTitle places a known song title into today's playback queue.
// A track already queued today keeps its position instead of being duplicated.
func (s *Service) QueueByTitle(title, addedBy string) (Track, int, bool) {
	wanted := normalizeTrackTitle(title)
	if len([]rune(wanted)) < 4 {
		return Track{}, 0, false
	}

	now := time.Now().UTC()
	s.mu.Lock()
	var selected *Track
	for _, track := range s.tracks {
		if normalizeTrackTitle(track.Title) == wanted {
			selected = track
			break
		}
	}
	if selected == nil {
		s.mu.Unlock()
		return Track{}, 0, false
	}

	if selected.LastAddedAt.In(time.Local).Format("2006-01-02") != now.In(time.Local).Format("2006-01-02") {
		selected.LastAddedAt = now
		selected.TimesAdded++
		if addedBy != "" {
			selected.AddedBy = addedBy
		}
	}

	queued := make([]*Track, 0, len(s.tracks))
	today := now.In(time.Local).Format("2006-01-02")
	for _, track := range s.tracks {
		if track.LastAddedAt.In(time.Local).Format("2006-01-02") == today {
			queued = append(queued, track)
		}
	}
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].LastAddedAt.Equal(queued[j].LastAddedAt) {
			return queued[i].VideoID < queued[j].VideoID
		}
		return queued[i].LastAddedAt.Before(queued[j].LastAddedAt)
	})
	position := 0
	for index, track := range queued {
		if track.VideoID == selected.VideoID {
			position = index + 1
			break
		}
	}
	snapshot := *selected
	s.mu.Unlock()

	s.persist(snapshot)
	return snapshot, position, true
}

// SearchAndQueue looks up a requested song title and adds its first playable
// YouTube result to today's queue.
func (s *Service) SearchAndQueue(ctx context.Context, query, addedBy string) (Track, int, error) {
	sources, err := media.SearchAudio(ctx, query, "", "")
	if err != nil {
		return Track{}, 0, err
	}
	for _, source := range sources {
		if track, position, matched := s.QueueByTitle(source.Title, addedBy); matched {
			return track, position, nil
		}
		videoID, ok := ExtractVideoID(source.URL)
		if !ok {
			continue
		}
		track, err := s.Add(ctx, videoID, source.URL, addedBy)
		if err != nil {
			continue
		}
		if queuedTrack, position, matched := s.QueueByTitle(track.Title, addedBy); matched {
			return queuedTrack, position, nil
		}
	}
	return Track{}, 0, errors.New("не удалось добавить найденную песню в очередь")
}

func normalizeTrackTitle(title string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func (s *Service) List() []Track {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tracks := make([]Track, 0, len(s.tracks))

	for _, track := range s.tracks {
		tracks = append(tracks, *track)
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].LastAddedAt.After(tracks[j].LastAddedAt)
	})

	return tracks
}

func (s *Service) Delete(videoID string) bool {
	s.mu.Lock()

	if _, ok := s.tracks[videoID]; !ok {
		s.mu.Unlock()

		return false
	}

	delete(s.tracks, videoID)
	s.mu.Unlock()

	if s.db != nil {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM track WHERE channel_id = $1 AND video_id = $2`, s.channelID, videoID)
	}

	return true
}

func (s *Service) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	authors := make(map[string]struct{})
	stats := Stats{TotalTracks: len(s.tracks)}

	for _, track := range s.tracks {
		stats.TotalDuration += track.DurationSeconds

		if track.Author != "" {
			authors[track.Author] = struct{}{}
		}
	}

	stats.UniqueAuthors = len(authors)

	return stats
}

// WriteCSV выгружает трек-лист для анализа/архива.
func (s *Service) WriteCSV(writer io.Writer) error {
	csvWriter := csv.NewWriter(writer)
	csvWriter.Comma = ';'

	header := []string{"id", "title", "author", "album", "duration_seconds", "year", "links"}

	if err := csvWriter.Write(header); err != nil {
		return err
	}

	for _, track := range s.List() {
		row := []string{
			track.VideoID,
			track.Title,
			track.Author,
			track.Album,
			strconv.Itoa(track.DurationSeconds),
			strconv.Itoa(track.Year),
			strings.Join(track.Links, " | "),
		}

		if err := csvWriter.Write(row); err != nil {
			return err
		}
	}

	csvWriter.Flush()

	return csvWriter.Error()
}

func (s *Service) persist(track Track) {
	if s.db == nil {
		return
	}

	query := `
		INSERT INTO track (
			video_id, url, title, author, album, year, duration_seconds, thumbnail, added_by,
			first_added_at, last_added_at, times_added, channel_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (channel_id, video_id) DO UPDATE SET
			title = EXCLUDED.title,
			author = EXCLUDED.author,
			album = EXCLUDED.album,
			year = EXCLUDED.year,
			duration_seconds = EXCLUDED.duration_seconds,
			thumbnail = EXCLUDED.thumbnail,
			added_by = EXCLUDED.added_by,
			last_added_at = EXCLUDED.last_added_at,
			times_added = track.times_added + 1
	`

	_, err := s.db.ExecContext(context.Background(), query,
		track.VideoID,
		track.URL,
		track.Title,
		track.Author,
		track.Album,
		track.Year,
		track.DurationSeconds,
		track.Thumbnail,
		track.AddedBy,
		track.FirstAddedAt,
		track.LastAddedAt,
		track.TimesAdded,
		s.channelID,
	)
	if err != nil {
		log.Printf("[Music] persist failed: %v", err)
	}
}

func (s *Service) loadFromDB() {
	if s.db == nil {
		return
	}

	rows, err := s.db.QueryContext(
		context.Background(),
		`SELECT video_id, url, title, author, album, year, duration_seconds, thumbnail, added_by,
		        first_added_at, last_added_at, times_added
		 FROM track WHERE channel_id = $1 ORDER BY last_added_at DESC`, s.channelID,
	)
	if err != nil {
		log.Printf("[Music] load failed: %v", err)

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var track Track

		if err := rows.Scan(
			&track.VideoID,
			&track.URL,
			&track.Title,
			&track.Author,
			&track.Album,
			&track.Year,
			&track.DurationSeconds,
			&track.Thumbnail,
			&track.AddedBy,
			&track.FirstAddedAt,
			&track.LastAddedAt,
			&track.TimesAdded,
		); err != nil {
			log.Printf("[Music] scan failed: %v", err)

			continue
		}

		track.Links = buildLinks(track.VideoID, track.URL)
		s.tracks[track.VideoID] = &track
	}
}

/* ── Telegram-экспорт ─────────────────────────────────────────────── */

// TelegramSender отправляет текст в Telegram (реализуется notify).
type TelegramSender func(ctx context.Context, text string) error

type TelegramPageSender func(ctx context.Context, text string, page, pages int) error

func (s *Service) SetTelegramPageSender(sender TelegramPageSender) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.telegramPage = sender
}

func (s *Service) SetTelegramSender(sender TelegramSender) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.telegram = sender
}

// ExportToTelegram выгружает трек-лист в Telegram одной страницей.
func (s *Service) ExportToTelegram(ctx context.Context) (int, error) {
	s.mu.RLock()
	pageSender := s.telegramPage
	sender := s.telegram
	s.mu.RUnlock()

	if pageSender != nil {
		text, pages := s.TelegramPage(0, 10)
		if err := pageSender(ctx, text, 0, pages); err != nil {
			return 0, err
		}

		return 1, nil
	}

	if sender == nil {
		return 0, errors.New("Telegram не настроен: заполните токен и chat id в интеграциях")
	}

	chunks := buildTelegramChunks(s.List())

	for _, chunk := range chunks {
		if err := sender(ctx, chunk); err != nil {
			return 0, err
		}
	}

	return len(chunks), nil
}

// buildTelegramChunks формирует сообщения для Telegram без превышения лимита.
func buildTelegramChunks(tracks []Track) []string {
	const perMessage = 20

	if len(tracks) == 0 {
		return []string{"🎵 Трек-лист пуст — кидайте ссылки на YouTube в чат."}
	}

	total := 0

	for _, track := range tracks {
		total += track.DurationSeconds
	}

	header := fmt.Sprintf(
		"<b>🎵 Трек-лист Nameless StreamHata</b>\n<i>%d треков · %s</i>",
		len(tracks),
		formatDuration(total),
	)

	var chunks []string
	var current strings.Builder

	current.WriteString(header + "\n\n")

	for index, track := range tracks {
		position := index + 1

		if position%perMessage == 1 && position > 1 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}

		line := fmt.Sprintf(
			"%d. <a href=\"%s\">%s</a> — %s",
			position,
			escapeHTML(track.URL),
			escapeHTML(track.Title),
			escapeHTML(orDash(track.Author)),
		)

		metaParts := make([]string, 0, 3)

		if track.Album != "" {
			metaParts = append(metaParts, escapeHTML(track.Album))
		}

		if track.Year > 0 {
			metaParts = append(metaParts, strconv.Itoa(track.Year))
		}

		metaParts = append(metaParts, formatDuration(track.DurationSeconds))

		current.WriteString(line + "\n")
		current.WriteString("   <i>" + strings.Join(metaParts, " · ") + "</i>\n")

		if track.VideoID != "" {
			current.WriteString("   <a href=\"https://music.youtube.com/watch?v=" + escapeHTML(track.VideoID) + "\">YouTube Music</a>\n")
		}
	}

	if strings.TrimSpace(current.String()) != "" {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}

	return chunks
}

// TelegramPage возвращает одну страницу трек-листа для Telegram и число страниц.
func (s *Service) TelegramPage(page, perPage int) (string, int) {
	tracks := s.List()

	if perPage <= 0 {
		perPage = 10
	}

	pages := (len(tracks) + perPage - 1) / perPage
	if pages == 0 {
		pages = 1
	}

	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}

	total := 0
	for _, track := range tracks {
		total += track.DurationSeconds
	}

	var body strings.Builder

	body.WriteString("*🎵 Трек\\-лист Nameless StreamHata*\n")
	body.WriteString(fmt.Sprintf("_%d треков · %s · стр %d/%d_\n\n", len(tracks), formatDuration(total), page+1, pages))

	start := page * perPage
	end := start + perPage
	if end > len(tracks) {
		end = len(tracks)
	}

	for index := start; index < end; index++ {
		track := tracks[index]
		title := track.Title
		if title == "" {
			title = track.URL
		}
		author := track.Author
		if author == "" {
			author = "—"
		}
		body.WriteString(fmt.Sprintf(
			"%d\\. [%s](%s) — %s · %s\n",
			index+1,
			escapeMarkdownV2(clipRunes(title, 80)),
			escapeMarkdownURL(track.URL),
			escapeMarkdownV2(clipRunes(author, 40)),
			formatDuration(track.DurationSeconds),
		))
	}

	return strings.TrimSpace(body.String()), pages
}

func escapeMarkdownV2(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)

	return replacer.Replace(value)
}

func escapeMarkdownURL(value string) string {
	return strings.NewReplacer("\\", "\\\\", ")", "\\)", "(", "\\(").Replace(value)
}

func clipRunes(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return string(runes)
	}

	if limit <= 1 {
		return string(runes[:limit])
	}

	return string(runes[:limit-1]) + "…"
}

func formatDuration(total int) string {
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60

	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}

	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

func escapeHTML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

	return replacer.Replace(value)
}

func orDash(value string) string {
	if value == "" {
		return "—"
	}

	return value
}
