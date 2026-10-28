package music

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
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

// Service — трек-лист стрима: парсит ссылки из чата, хранит и экспортирует.
type Service struct {
	mu      sync.RWMutex
	tracks  map[string]*Track
	current string
	enabled bool

	db       *sql.DB
	fetcher  Fetcher
	telegram TelegramSender
}

func NewService(db *sql.DB) *Service {
	service := &Service{
		tracks:  make(map[string]*Track),
		enabled: true,
		db:      db,
		fetcher: FetchVideoMeta,
	}

	service.loadFromDB()

	return service
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

		return
	}

	if _, ok := s.tracks[videoID]; ok {
		s.current = videoID
	}
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
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM track WHERE video_id = $1`, videoID)
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
			first_added_at, last_added_at, times_added
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (video_id) DO UPDATE SET
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
		 FROM track ORDER BY last_added_at DESC`,
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

func (s *Service) SetTelegramSender(sender TelegramSender) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.telegram = sender
}

// ExportToTelegram выгружает трек-лист в Telegram (HTML, по 20 треков).
func (s *Service) ExportToTelegram(ctx context.Context) (int, error) {
	s.mu.RLock()
	sender := s.telegram
	s.mu.RUnlock()

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

	body.WriteString("<b>🎵 Трек-лист Nameless StreamHata</b>\n")
	body.WriteString(fmt.Sprintf("<i>%d треков · %s · стр. %d/%d</i>\n\n", len(tracks), formatDuration(total), page+1, pages))

	start := page * perPage
	end := start + perPage
	if end > len(tracks) {
		end = len(tracks)
	}

	for index := start; index < end; index++ {
		track := tracks[index]
		meta := make([]string, 0, 4)

		if track.Author != "" {
			meta = append(meta, escapeHTML(track.Author))
		}
		if track.Album != "" {
			meta = append(meta, escapeHTML(track.Album))
		}
		if track.Year > 0 {
			meta = append(meta, strconv.Itoa(track.Year))
		}
		meta = append(meta, formatDuration(track.DurationSeconds))

		body.WriteString(fmt.Sprintf(
			"%d. <a href=\"%s\">%s</a>\n   <i>%s</i>\n",
			index+1,
			escapeHTML(track.URL),
			escapeHTML(track.Title),
			strings.Join(meta, " · "),
		))
	}

	return strings.TrimSpace(body.String()), pages
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
