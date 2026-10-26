// Package suggestions принимает заявки на добавление игр в каталог.
package suggestions

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/p7x-dev/nameless-streamhata-backend/access"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Suggestion struct {
	ID               int64     `json:"id"`
	UserLogin        string    `json:"userLogin"`
	UserDisplayName  string    `json:"userDisplayName"`
	Title            string    `json:"title"`
	Platform         string    `json:"platform"`
	Year             int       `json:"year"`
	Links            string    `json:"links"`
	Comment          string    `json:"comment"`
	CoverURL         string    `json:"coverUrl"`
	Screenshots      string    `json:"screenshots"`
	Description      string    `json:"description"`
	Genres           string    `json:"genres"`
	Developer        string    `json:"developer"`
	Publisher        string    `json:"publisher"`
	Website          string    `json:"website"`
	Services         []string  `json:"services"`
	Status           string    `json:"status"`
	ModeratorLogin   string    `json:"moderatorLogin"`
	ModeratorComment string    `json:"moderatorComment"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type Service struct {
	db     *sql.DB
	access *access.Service
}

func New(db *sql.DB, accessService *access.Service) *Service {
	return &Service{db: db, access: accessService}
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/game-suggestions", s.create)
	srv.ServeEndpoint("/rgg-sync/game-suggestions/mine", s.mine)
	srv.ServeEndpoint("/rgg-sync/game-suggestions/admin", s.list)
	srv.ServeEndpoint("/rgg-sync/game-suggestions/admin/{id}", s.review)
}

func (s *Service) create(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})

		return
	}

	login, displayName := s.user(request)
	if login == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	var body struct {
		Title       string   `json:"title"`
		Platform    string   `json:"platform"`
		Year        int      `json:"year"`
		Links       string   `json:"links"`
		Comment     string   `json:"comment"`
		CoverURL    string   `json:"coverUrl"`
		Screenshots string   `json:"screenshots"`
		Description string   `json:"description"`
		Genres      string   `json:"genres"`
		Developer   string   `json:"developer"`
		Publisher   string   `json:"publisher"`
		Website     string   `json:"website"`
		Services    []string `json:"services"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}

	title := strings.TrimSpace(body.Title)
	if len([]rune(title)) < 2 || len([]rune(title)) > 160 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "укажите название игры (2–160 символов)"})

		return
	}

	platform := strings.TrimSpace(body.Platform)
	if len([]rune(platform)) > 80 {
		platform = string([]rune(platform)[:80])
	}

	links := strings.TrimSpace(body.Links)
	if len([]rune(links)) > 1200 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "слишком много ссылок"})

		return
	}

	comment := strings.TrimSpace(body.Comment)
	if len([]rune(comment)) > 2000 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "комментарий слишком длинный"})

		return
	}

	year := body.Year
	if year < 1950 || year > time.Now().Year()+2 {
		year = 0
	}

	coverURL := strings.TrimSpace(body.CoverURL)
	if len([]rune(coverURL)) > 2000 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "ссылка на обложку слишком длинная"})

		return
	}

	screenshots := strings.TrimSpace(body.Screenshots)
	if len([]rune(screenshots)) > 6000 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "слишком много скриншотов"})

		return
	}

	description := strings.TrimSpace(body.Description)
	if len([]rune(description)) > 5000 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "описание слишком длинное"})

		return
	}

	genres := strings.TrimSpace(body.Genres)
	developer := strings.TrimSpace(body.Developer)
	publisher := strings.TrimSpace(body.Publisher)
	website := strings.TrimSpace(body.Website)
	if len([]rune(genres)) > 300 || len([]rune(developer)) > 300 || len([]rune(publisher)) > 300 || len([]rune(website)) > 2000 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "слишком длинное поле"})

		return
	}

	services := cleanServices(body.Services)

	var suggestion Suggestion

	err := s.db.QueryRowContext(
		request.Context(),
		`INSERT INTO game_suggestions
			(user_login, user_display_name, title, platform, year, links, comment,
			 cover_url, screenshots, description, genres, developer, publisher, website, services)
		 VALUES (lower($1), $2, $3, $4, NULLIF($5, 0), $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		 RETURNING id, user_login, user_display_name, title, platform, COALESCE(year, 0), links, comment,
		           cover_url, screenshots, description, genres, developer, publisher, website, services,
		           status, moderator_login, moderator_comment, created_at, updated_at`,
		login, displayName, title, platform, year, links, comment,
		coverURL, screenshots, description, genres, developer, publisher, website, pq.Array(services),
	).Scan(
		&suggestion.ID, &suggestion.UserLogin, &suggestion.UserDisplayName, &suggestion.Title,
		&suggestion.Platform, &suggestion.Year, &suggestion.Links, &suggestion.Comment,
		&suggestion.CoverURL, &suggestion.Screenshots, &suggestion.Description, &suggestion.Genres,
		&suggestion.Developer, &suggestion.Publisher, &suggestion.Website, pq.Array(&suggestion.Services),
		&suggestion.Status, &suggestion.ModeratorLogin, &suggestion.ModeratorComment,
		&suggestion.CreatedAt, &suggestion.UpdatedAt,
	)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить заявку"})

		return
	}

	writeJSON(response, http.StatusCreated, suggestion)
}

func (s *Service) mine(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})

		return
	}

	login, _ := s.user(request)
	if login == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "twitch authorization required"})

		return
	}

	rows, err := s.db.QueryContext(
		request.Context(),
		`SELECT id, user_login, user_display_name, title, platform, COALESCE(year, 0), links, comment,
		        cover_url, screenshots, description, genres, developer, publisher, website, services,
		        status, moderator_login, moderator_comment, created_at, updated_at
		   FROM game_suggestions
		  WHERE lower(user_login) = lower($1)
		  ORDER BY created_at DESC
		  LIMIT 30`,
		login,
	)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось загрузить заявки"})

		return
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]Suggestion, 0)

	for rows.Next() {
		var item Suggestion
		if err := rows.Scan(
			&item.ID, &item.UserLogin, &item.UserDisplayName, &item.Title, &item.Platform,
			&item.Year, &item.Links, &item.Comment, &item.CoverURL, &item.Screenshots,
			&item.Description, &item.Genres, &item.Developer, &item.Publisher, &item.Website,
			pq.Array(&item.Services), &item.Status,
			&item.ModeratorLogin, &item.ModeratorComment, &item.CreatedAt, &item.UpdatedAt,
		); err == nil {
			items = append(items, item)
		}
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Service) list(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})

		return
	}

	login, _ := s.user(request)
	if !s.admin(login) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "admin access required"})

		return
	}

	status := strings.TrimSpace(request.URL.Query().Get("status"))
	rows, err := s.db.QueryContext(
		request.Context(),
		`SELECT id, user_login, user_display_name, title, platform, COALESCE(year, 0), links, comment,
		        cover_url, screenshots, description, genres, developer, publisher, website, services,
		        status, moderator_login, moderator_comment, created_at, updated_at
		   FROM game_suggestions
		  WHERE ($1 = '' OR status = $1)
		  ORDER BY created_at DESC
		  LIMIT 200`,
		status,
	)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось загрузить заявки"})

		return
	}
	defer func() {
		_ = rows.Close()
	}()

	items := make([]Suggestion, 0)

	for rows.Next() {
		var item Suggestion
		if err := rows.Scan(
			&item.ID, &item.UserLogin, &item.UserDisplayName, &item.Title, &item.Platform,
			&item.Year, &item.Links, &item.Comment, &item.CoverURL, &item.Screenshots,
			&item.Description, &item.Genres, &item.Developer, &item.Publisher, &item.Website,
			pq.Array(&item.Services), &item.Status,
			&item.ModeratorLogin, &item.ModeratorComment, &item.CreatedAt, &item.UpdatedAt,
		); err == nil {
			items = append(items, item)
		}
	}

	writeJSON(response, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (s *Service) review(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPatch {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})

		return
	}

	login, _ := s.user(request)
	if !s.admin(login) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "admin access required"})

		return
	}

	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid id"})

		return
	}

	var body struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}

	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "approved" && status != "rejected" && status != "pending" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid status"})

		return
	}

	comment := strings.TrimSpace(body.Comment)
	result, err := s.db.ExecContext(
		request.Context(),
		`UPDATE game_suggestions
		    SET status = $1, moderator_login = lower($2), moderator_comment = $3, updated_at = now()
		  WHERE id = $4`,
		status, login, comment, id,
	)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось обновить заявку"})

		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "заявка не найдена"})

		return
	}

	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "id": id, "status": status})
}

func (s *Service) user(request *http.Request) (string, string) {
	if s.db == nil {
		return "", ""
	}

	cookie, err := request.Cookie("e-hub-refresh-token")
	if err != nil || cookie.Value == "" {
		return "", ""
	}

	var login, displayName string

	err = s.db.QueryRowContext(
		request.Context(),
		`SELECT u.username, COALESCE(NULLIF(u.display_name, ''), u.username)
		   FROM tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.refresh_token = $1 AND u.banned = false
		  LIMIT 1`,
		cookie.Value,
	).Scan(&login, &displayName)
	if err != nil {
		return "", ""
	}

	return login, displayName
}

func (s *Service) admin(login string) bool {
	if login == "" || s.access == nil {
		return false
	}

	role := s.access.Role(login)

	return role == "owner" || role == "admin"
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func cleanServices(values []string) []string {
	allowed := map[string]bool{
		"steam": true, "igdb": true, "rawg": true, "mobygames": true, "rgg": true,
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)

	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if !allowed[key] || seen[key] {
			continue
		}

		seen[key] = true
		result = append(result, key)
	}

	if len(result) > 8 {
		result = result[:8]
	}

	return result
}
