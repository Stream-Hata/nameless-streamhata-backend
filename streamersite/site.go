// Package streamersite stores streamer-owned public profile pages.
package streamersite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/media"
	"github.com/p7x-dev/nameless-streamhata-backend/security"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Site struct {
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	Published   bool      `json:"published"`
	Schedule    Schedule  `json:"schedule"`
	Design      Design    `json:"design"`
	OwnerLogin  string    `json:"ownerLogin"`
	OwnerName   string    `json:"ownerName"`
	AvatarURL   string    `json:"avatarUrl"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Design struct {
	Preset          string `json:"preset"`
	AccentColor     string `json:"accentColor"`
	BackgroundColor string `json:"backgroundColor"`
	BackgroundImage string `json:"backgroundImage"`
	ThemeSwitcher   bool   `json:"themeSwitcher"`
}

func defaultDesign() Design {
	return Design{Preset: "midnight", AccentColor: "#9147ff", BackgroundColor: "#120d1c"}
}

func normalizeDesign(value Design) Design {
	allowedPresets := map[string]bool{"midnight": true, "neon": true, "paper": true, "custom": true}
	if !allowedPresets[value.Preset] {
		value.Preset = "midnight"
	}
	colorPattern := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	defaults := defaultDesign()
	if !colorPattern.MatchString(value.AccentColor) {
		value.AccentColor = defaults.AccentColor
	}
	if !colorPattern.MatchString(value.BackgroundColor) {
		value.BackgroundColor = defaults.BackgroundColor
	}
	if value.BackgroundImage != "" && !strings.HasPrefix(value.BackgroundImage, "/rgg-sync/media/file/") {
		value.BackgroundImage = ""
	}
	return value
}

type Schedule struct {
	Enabled         bool   `json:"enabled"`
	DaysOfWeek      []int  `json:"daysOfWeek"`
	StartTime       string `json:"startTime"`
	ReminderMinutes int    `json:"reminderMinutes"`
}

func defaultSchedule() Schedule {
	return Schedule{DaysOfWeek: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "18:00", ReminderMinutes: 60}
}

func normalizeSchedule(value Schedule) Schedule {
	if value.DaysOfWeek == nil {
		value.DaysOfWeek = defaultSchedule().DaysOfWeek
	}
	seen := make(map[int]bool, len(value.DaysOfWeek))
	days := make([]int, 0, len(value.DaysOfWeek))
	for _, day := range value.DaysOfWeek {
		if day >= 0 && day <= 6 && !seen[day] {
			seen[day] = true
			days = append(days, day)
		}
	}
	value.DaysOfWeek = days
	if !regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`).MatchString(value.StartTime) {
		value.StartTime = "18:00"
	}
	if value.ReminderMinutes != 10 && value.ReminderMinutes != 30 && value.ReminderMinutes != 60 {
		value.ReminderMinutes = 60
	}
	return value
}

type Service struct {
	db       *sql.DB
	access   *access.Service
	security *security.Service
	policy   *bluemonday.Policy
}

func New(db *sql.DB, accessService *access.Service, securityService *security.Service) *Service {
	policy := bluemonday.UGCPolicy()
	policy.AllowStyling()
	policy.AllowTables()
	policy.AllowAttrs("class").Globally()
	policy.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	policy.AllowAttrs("class", "data-kind").OnElements("div", "section", "figure")
	policy.AllowAttrs("data-site-block").OnElements("p")
	policy.RequireNoFollowOnLinks(true)

	return &Service{db: db, access: accessService, security: securityService, policy: policy}
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/site", s.editorRoute)
	srv.ServeEndpoint("/rgg-sync/site/upload", s.upload)
	srv.ServeEndpoint("/rgg-sync/site/{slug}", s.publicRoute)
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	if s.access.LoginFromRequest(r) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authorization required"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	if err := r.ParseMultipartForm(15 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read file"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file is required"})
		return
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 15<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read file"})
		return
	}
	if s.security != nil {
		if scanner := s.security.FileScanner(); scanner != nil && scanner.Enabled() {
			report, scanErr := scanner.Scan(r.Context(), data, security.ScanMeta{FileName: header.Filename, Mime: header.Header.Get("Content-Type")})
			if errors.Is(scanErr, security.ErrScanUnavailable) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "security scan is unavailable", "scan": report})
				return
			}
			if scanErr != nil && !errors.Is(scanErr, security.ErrScannerDisabled) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security scan failed"})
				return
			}
			if scanErr == nil && report.Verdict == security.ScanBlocked {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "file blocked by security scan", "scan": report})
				return
			}
		}
	}
	url, err := media.SaveImage(bytes.NewReader(data), header.Filename, 2400, map[string]bool{
		".avif": true, ".bmp": true, ".gif": true, ".jpeg": true, ".jpg": true, ".png": true, ".webp": true,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": url})
}

func (s *Service) editorRoute(w http.ResponseWriter, r *http.Request) {
	login := s.access.LoginFromRequest(r)
	if login == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authorization required"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		site, err := s.editorSite(r, login)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load site"})
			return
		}
		writeJSON(w, http.StatusOK, site)
	case http.MethodPut, http.MethodPost:
		s.save(w, r, login)
	default:
		w.Header().Set("Allow", "GET, PUT, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) publicRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := cleanSlug(r.PathValue("slug"))
	if slug == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "site not found"})
		return
	}

	preview := r.URL.Query().Get("preview") == "1"
	login := ""
	if preview {
		login = s.access.LoginFromRequest(r)
		if login == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authorization required"})
			return
		}
	}
	site, err := s.find(r, slug, !preview)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "site not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load site"})
		return
	}
	if preview && !strings.EqualFold(site.OwnerLogin, login) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "site not found"})
		return
	}
	writeJSON(w, http.StatusOK, site)
}

func (s *Service) save(w http.ResponseWriter, r *http.Request, login string) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var input struct {
		Slug        string   `json:"slug"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Content     string   `json:"content"`
		Published   bool     `json:"published"`
		Schedule    Schedule `json:"schedule"`
		Design      Design   `json:"design"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	slug := cleanSlug(input.Slug)
	title := strings.TrimSpace(input.Title)
	description := strings.TrimSpace(input.Description)
	if slug == "" || title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug and title are required"})
		return
	}
	if len([]rune(title)) > 160 || len([]rune(description)) > 500 || len(input.Content) > 1<<20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "site content is too large"})
		return
	}

	var ownerID int64
	var ownerName, avatarURL string
	if err := s.db.QueryRowContext(r.Context(), `SELECT id, COALESCE(NULLIF(display_name, ''), username), COALESCE(avatar, '')
		FROM users WHERE lower(username) = lower($1) LIMIT 1`, login).Scan(&ownerID, &ownerName, &avatarURL); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "streamer account not found"})
		return
	}

	var occupied bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM streamer_site WHERE slug = $1 AND owner_id <> $2)`, slug, ownerID).Scan(&occupied); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to validate site address"})
		return
	}
	if occupied {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "site address is already in use"})
		return
	}

	content := s.policy.Sanitize(input.Content)
	scheduleJSON, err := json.Marshal(normalizeSchedule(input.Schedule))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid schedule"})
		return
	}
	designJSON, err := json.Marshal(normalizeDesign(input.Design))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid design"})
		return
	}
	site, err := scanSite(s.db.QueryRowContext(r.Context(), `
		INSERT INTO streamer_site (owner_id, slug, title, description, content, published, schedule, design)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb)
		ON CONFLICT (owner_id) DO UPDATE SET slug = EXCLUDED.slug, title = EXCLUDED.title,
			description = EXCLUDED.description, content = EXCLUDED.content, published = EXCLUDED.published,
			schedule = EXCLUDED.schedule, design = EXCLUDED.design, updated_at = now()
		RETURNING owner_id, slug, title, description, content, published, schedule, design, updated_at`,
		ownerID, slug, title, description, content, input.Published, string(scheduleJSON), string(designJSON)))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save site"})
		return
	}
	site.OwnerLogin = login
	site.OwnerName = ownerName
	site.AvatarURL = avatarURL
	writeJSON(w, http.StatusOK, site)
}

func (s *Service) editorSite(r *http.Request, login string) (Site, error) {
	var ownerID int64
	var ownerName, avatarURL string
	err := s.db.QueryRowContext(r.Context(), `SELECT id, COALESCE(NULLIF(display_name, ''), username), COALESCE(avatar, '')
		FROM users WHERE lower(username) = lower($1) LIMIT 1`, login).Scan(&ownerID, &ownerName, &avatarURL)
	if err != nil {
		return Site{}, err
	}
	row := s.db.QueryRowContext(r.Context(), `SELECT owner_id, slug, title, description, content, published, schedule, design, updated_at
		FROM streamer_site WHERE owner_id = $1`, ownerID)
	site, err := scanSite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Site{Slug: defaultSlug(login), Title: ownerName, OwnerName: ownerName, OwnerLogin: login, AvatarURL: avatarURL, Schedule: defaultSchedule(), Design: defaultDesign()}, nil
	}
	if err != nil {
		return Site{}, err
	}
	site.OwnerName = ownerName
	site.OwnerLogin = login
	site.AvatarURL = avatarURL
	return site, nil
}

func (s *Service) find(r *http.Request, slug string, publishedOnly bool) (Site, error) {
	query := `SELECT ss.owner_id, ss.slug, ss.title, ss.description, ss.content, ss.published, ss.schedule, ss.design, ss.updated_at,
		u.username, COALESCE(NULLIF(u.display_name, ''), u.username), COALESCE(u.avatar, '')
		FROM streamer_site ss JOIN users u ON u.id = ss.owner_id WHERE ss.slug = $1`
	if publishedOnly {
		query += ` AND ss.published = true`
	}
	return scanPublicSite(s.db.QueryRowContext(r.Context(), query, slug))
}

type scanner interface{ Scan(...any) error }

func scanSite(row scanner) (Site, error) {
	var site Site
	var ignoredOwnerID int64
	var scheduleJSON []byte
	var designJSON []byte
	err := row.Scan(&ignoredOwnerID, &site.Slug, &site.Title, &site.Description, &site.Content, &site.Published, &scheduleJSON, &designJSON, &site.UpdatedAt)
	if err == nil {
		if json.Unmarshal(scheduleJSON, &site.Schedule) != nil {
			site.Schedule = defaultSchedule()
		} else {
			site.Schedule = normalizeSchedule(site.Schedule)
		}
		if json.Unmarshal(designJSON, &site.Design) != nil {
			site.Design = defaultDesign()
		} else {
			site.Design = normalizeDesign(site.Design)
		}
	}
	return site, err
}

func scanPublicSite(row scanner) (Site, error) {
	var site Site
	var ignoredOwnerID int64
	var scheduleJSON []byte
	var designJSON []byte
	err := row.Scan(&ignoredOwnerID, &site.Slug, &site.Title, &site.Description, &site.Content, &site.Published,
		&scheduleJSON, &designJSON, &site.UpdatedAt, &site.OwnerLogin, &site.OwnerName, &site.AvatarURL)
	if err == nil {
		if json.Unmarshal(scheduleJSON, &site.Schedule) != nil {
			site.Schedule = defaultSchedule()
		} else {
			site.Schedule = normalizeSchedule(site.Schedule)
		}
		if json.Unmarshal(designJSON, &site.Design) != nil {
			site.Design = defaultDesign()
		} else {
			site.Design = normalizeDesign(site.Design)
		}
	}
	return site, err
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,78}[a-z0-9])?$`)

func cleanSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !slugPattern.MatchString(value) || strings.Contains(value, "--") {
		return ""
	}
	return value
}

func defaultSlug(login string) string {
	return cleanSlug(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(login)), "_", "-"))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
