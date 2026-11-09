// Package blog — публичные статьи и админский CRUD для блога.
package blog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/microcosm-cc/bluemonday"
	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/media"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Article struct {
	ID          int64      `json:"id"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Excerpt     string     `json:"excerpt"`
	Content     string     `json:"content,omitempty"`
	CoverURL    string     `json:"coverUrl"`
	Status      string     `json:"status"`
	AuthorID    int64      `json:"authorId"`
	AuthorLogin string     `json:"authorLogin"`
	AuthorName  string     `json:"authorName"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type Service struct {
	db     *sql.DB
	access *access.Service
	policy *bluemonday.Policy
}

func New(db *sql.DB, accessService *access.Service) *Service {
	policy := bluemonday.UGCPolicy()
	policy.AllowStyling()
	policy.AllowAttrs("class").Globally()
	policy.RequireNoFollowOnLinks(true)

	return &Service{db: db, access: accessService, policy: policy}
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/blog", s.publicList)
	srv.ServeEndpoint("/rgg-sync/blog/upload", s.upload)
	srv.ServeEndpoint("/rgg-sync/blog/admin", s.adminList)
	srv.ServeEndpoint("/rgg-sync/blog/admin/{id}", s.adminArticle)
	srv.ServeEndpoint("/rgg-sync/blog/{slug}", s.publicArticle)
}

func (s *Service) upload(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorize(response, request); !ok {
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	request.Body = http.MaxBytesReader(response, request.Body, 15<<20)
	if err := request.ParseMultipartForm(15 << 20); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "failed to read file"})

		return
	}

	file, header, err := request.FormFile("file")
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "file is required"})

		return
	}
	defer func() {
		_ = file.Close()
	}()

	url, err := media.SaveImage(file, header.Filename, 2400, map[string]bool{
		".avif": true, ".bmp": true, ".gif": true, ".jpeg": true, ".jpg": true, ".png": true, ".webp": true,
	})
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}

	writeJSON(response, http.StatusCreated, map[string]string{"url": url})
}

func (s *Service) publicList(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	s.list(response, request, false)
}

func (s *Service) adminList(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorize(response, request); !ok {
		return
	}

	switch request.Method {
	case http.MethodGet:
		s.list(response, request, true)
	case http.MethodPost:
		s.create(response, request)
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) publicArticle(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	slug := strings.TrimSpace(request.PathValue("slug"))
	if slug == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "slug is required"})

		return
	}

	item, err := s.bySlug(request.Context(), slug, false)
	if err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "article not found"})

		return
	}

	writeJSON(response, http.StatusOK, item)
}

func (s *Service) adminArticle(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorize(response, request); !ok {
		return
	}

	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid id"})

		return
	}

	switch request.Method {
	case http.MethodGet:
		item, err := s.byID(request.Context(), id)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "article not found"})

			return
		}
		writeJSON(response, http.StatusOK, item)
	case http.MethodPut, http.MethodPatch:
		s.update(response, request, id)
	case http.MethodDelete:
		result, err := s.db.ExecContext(request.Context(), `DELETE FROM blog_articles WHERE id = $1`, id)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to delete article"})

			return
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "article not found"})

			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) list(response http.ResponseWriter, request *http.Request, admin bool) {
	limit := clampInt(request.URL.Query().Get("limit"), 1, 100, 20)
	offset := clampInt(request.URL.Query().Get("offset"), 0, 100000, 0)
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	status := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("status")))

	where := []string{"1=1"}
	args := make([]any, 0, 4)

	if !admin {
		where = append(where, "status = 'published'")
	} else if status == "draft" || status == "published" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}

	if query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		where = append(where, fmt.Sprintf("(lower(title) LIKE $%d OR lower(excerpt) LIKE $%d)", len(args), len(args)))
	}

	whereSQL := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRowContext(
		request.Context(),
		"SELECT count(*) FROM blog_articles WHERE "+whereSQL,
		args...,
	).Scan(&total); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to count articles"})

		return
	}

	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(
		request.Context(),
		`SELECT id, slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name,
			published_at, created_at, updated_at
		 FROM blog_articles WHERE `+whereSQL+`
		 ORDER BY COALESCE(published_at, created_at) DESC
		 LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...,
	)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load articles"})

		return
	}

	defer func() {
		_ = rows.Close()
	}()

	items := make([]Article, 0, limit)
	for rows.Next() {
		item, err := scanArticle(rows)
		if err == nil {
			item.Content = ""
			items = append(items, item)
		}
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Service) bySlug(ctx context.Context, slug string, admin bool) (Article, error) {
	query := `SELECT id, slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name,
		published_at, created_at, updated_at FROM blog_articles WHERE slug = $1`
	args := []any{slug}
	if !admin {
		query += " AND status = 'published'"
	}

	row := s.db.QueryRowContext(ctx, query, args...)
	item, err := scanArticle(row)
	if err != nil {
		return Article{}, err
	}
	item.Content = s.policy.Sanitize(item.Content)

	return item, nil
}

func (s *Service) byID(ctx context.Context, id int64) (Article, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name,
		published_at, created_at, updated_at FROM blog_articles WHERE id = $1`, id)
	item, err := scanArticle(row)
	if err != nil {
		return Article{}, err
	}
	item.Content = s.policy.Sanitize(item.Content)

	return item, nil
}

func (s *Service) create(response http.ResponseWriter, request *http.Request) {
	login, ok := s.authorize(response, request)
	if !ok {
		return
	}

	authorID, authorName, err := s.author(request.Context(), login)
	if err != nil {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "author not found"})

		return
	}

	input, err := decodeInput(response, request)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "title is required"})

		return
	}
	if len([]rune(title)) > 200 {
		title = string([]rune(title)[:200])
	}

	status := normalizeStatus(input.Status)
	content := s.policy.Sanitize(input.Content)
	excerpt := strings.TrimSpace(input.Excerpt)
	if excerpt == "" {
		excerpt = makeExcerpt(content)
	}

	slug := s.uniqueSlug(request.Context(), firstNonEmpty(normalizeSlug(input.Slug), normalizeSlug(title), "article"), 0)
	publishedAt := any(nil)
	if status == "published" {
		publishedAt = time.Now().UTC()
	}

	var item Article
	err = s.db.QueryRowContext(request.Context(), `
		INSERT INTO blog_articles (slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name, published_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name,
			published_at, created_at, updated_at`,
		slug, title, excerpt, content, strings.TrimSpace(input.CoverURL), status, authorID, login, authorName, publishedAt,
	).Scan(&item.ID, &item.Slug, &item.Title, &item.Excerpt, &item.Content, &item.CoverURL, &item.Status,
		&item.AuthorID, &item.AuthorLogin, &item.AuthorName, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		log.Printf("[blog] create failed: %v", err)
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to create article"})

		return
	}

	item.Content = s.policy.Sanitize(item.Content)
	writeJSON(response, http.StatusCreated, item)
}

func (s *Service) update(response http.ResponseWriter, request *http.Request, id int64) {
	if _, ok := s.authorize(response, request); !ok {
		return
	}

	input, err := decodeInput(response, request)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "title is required"})

		return
	}
	if len([]rune(title)) > 200 {
		title = string([]rune(title)[:200])
	}

	status := normalizeStatus(input.Status)
	content := s.policy.Sanitize(input.Content)
	excerpt := strings.TrimSpace(input.Excerpt)
	if excerpt == "" {
		excerpt = makeExcerpt(content)
	}

	existing, err := s.byID(request.Context(), id)
	if err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "article not found"})

		return
	}

	slug := firstNonEmpty(normalizeSlug(input.Slug), existing.Slug, normalizeSlug(title))
	slug = s.uniqueSlug(request.Context(), slug, id)
	publishedAt := existing.PublishedAt
	if status == "published" && publishedAt == nil {
		now := time.Now().UTC()
		publishedAt = &now
	}

	var item Article
	err = s.db.QueryRowContext(request.Context(), `
		UPDATE blog_articles SET slug = $1, title = $2, excerpt = $3, content = $4, cover_url = $5,
			status = $6, published_at = $7, updated_at = now()
		WHERE id = $8
		RETURNING id, slug, title, excerpt, content, cover_url, status, author_id, author_login, author_name,
			published_at, created_at, updated_at`,
		slug, title, excerpt, content, strings.TrimSpace(input.CoverURL), status, publishedAt, id,
	).Scan(&item.ID, &item.Slug, &item.Title, &item.Excerpt, &item.Content, &item.CoverURL, &item.Status,
		&item.AuthorID, &item.AuthorLogin, &item.AuthorName, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		log.Printf("[blog] update failed: %v", err)
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to update article"})

		return
	}

	item.Content = s.policy.Sanitize(item.Content)
	writeJSON(response, http.StatusOK, item)
}

func (s *Service) authorize(response http.ResponseWriter, request *http.Request) (string, bool) {
	if s.access == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "access service unavailable"})

		return "", false
	}

	login := s.access.LoginFromRequest(request)
	if login == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "authorization required"})

		return "", false
	}
	if !s.access.IsAdmin(login) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return "", false
	}

	return login, true
}

func (s *Service) author(ctx context.Context, login string) (int64, string, error) {
	var id int64
	var name string

	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(NULLIF(display_name, ''), username)
		FROM users WHERE lower(username) = lower($1) LIMIT 1`, login).Scan(&id, &name)
	if err != nil {
		return 0, "", err
	}

	return id, name, nil
}

func (s *Service) uniqueSlug(ctx context.Context, base string, exceptID int64) string {
	base = normalizeSlug(base)
	if base == "" {
		base = "article"
	}

	for index := 0; index < 100; index++ {
		candidate := base
		if index > 0 {
			candidate = fmt.Sprintf("%s-%d", base, index+1)
		}

		var exists bool
		_ = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blog_articles WHERE slug = $1 AND id <> $2)`,
			candidate, exceptID).Scan(&exists)
		if !exists {
			return candidate
		}
	}

	return fmt.Sprintf("%s-%d", base, time.Now().Unix())
}

type input struct {
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Excerpt  string `json:"excerpt"`
	Content  string `json:"content"`
	CoverURL string `json:"coverUrl"`
	Status   string `json:"status"`
}

func decodeInput(response http.ResponseWriter, request *http.Request) (input, error) {
	var body input
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 3<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, fmt.Errorf("invalid json")
	}

	if len(body.Content) > 2<<20 {
		return body, fmt.Errorf("content is too large")
	}

	return body, nil
}

func scanArticle(scanner interface{ Scan(dest ...any) error }) (Article, error) {
	var item Article
	var publishedAt sql.NullTime

	err := scanner.Scan(&item.ID, &item.Slug, &item.Title, &item.Excerpt, &item.Content, &item.CoverURL,
		&item.Status, &item.AuthorID, &item.AuthorLogin, &item.AuthorName, &publishedAt,
		&item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return item, err
	}
	if publishedAt.Valid {
		item.PublishedAt = &publishedAt.Time
	}

	return item, nil
}

func normalizeStatus(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "published" {
		return "published"
	}

	return "draft"
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

func makeExcerpt(content string) string {
	text := html.UnescapeString(tagPattern.ReplaceAllString(content, " "))
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 240 {
		text = strings.TrimSpace(string(runes[:240]))
	}

	return text
}

func normalizeSlug(value string) string {
	var builder strings.Builder
	lastDash := false

	for _, symbol := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) {
			builder.WriteRune(symbol)
			lastDash = false
		} else if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func clampInt(value string, minValue, maxValue, fallback int) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	if number < minValue {
		return minValue
	}
	if number > maxValue {
		return maxValue
	}

	return number
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
