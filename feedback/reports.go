package feedback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ReportSummary struct {
	Prefix    string    `json:"prefix"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	IssueURL  string    `json:"issueUrl"`
	CreatedAt time.Time `json:"createdAt"`
	Source    string    `json:"source"`
}

func (s *Service) mineHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	user, err := s.sessionUser(request)
	if err != nil {
		status := http.StatusUnauthorized
		if err == errForbidden {
			status = http.StatusForbidden
		}
		writeJSON(response, status, map[string]string{"error": "войдите через Twitch"})

		return
	}
	if s.db == nil {
		writeJSON(response, http.StatusOK, map[string]any{"items": []ReportSummary{}})

		return
	}
	rows, err := s.db.QueryContext(request.Context(), `SELECT prefix, number, title, status, issue_url, created_at
		FROM feedback_reports WHERE author = $1 ORDER BY created_at DESC LIMIT 50`, user.Username)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось загрузить обращения"})

		return
	}
	defer rows.Close()
	items := make([]ReportSummary, 0)
	for rows.Next() {
		var item ReportSummary
		if rows.Scan(&item.Prefix, &item.Number, &item.Title, &item.Status, &item.IssueURL, &item.CreatedAt) == nil {
			item.Source = "local"
			items = append(items, item)
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) similarHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	if _, err := s.sessionUser(request); err != nil {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "войдите через Twitch"})

		return
	}
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	if len([]rune(query)) < 3 {
		writeJSON(response, http.StatusOK, map[string]any{"items": []ReportSummary{}})

		return
	}
	if len([]rune(query)) > 120 {
		query = string([]rune(query)[:120])
	}
	items := make([]ReportSummary, 0, 8)
	if s.db != nil {
		rows, err := s.db.QueryContext(request.Context(), `SELECT prefix, number, title, status, issue_url, created_at
			FROM feedback_reports
			WHERE title ILIKE '%' || $1 || '%' OR body ILIKE '%' || $1 || '%'
			ORDER BY created_at DESC LIMIT 5`, query)
		if err == nil {
			for rows.Next() {
				var item ReportSummary
				if rows.Scan(&item.Prefix, &item.Number, &item.Title, &item.Status, &item.IssueURL, &item.CreatedAt) == nil {
					item.Source = "local"
					items = append(items, item)
				}
			}
			_ = rows.Close()
		}
	}
	items = append(items, s.githubSearch(request.Context(), query)...)
	seen := make(map[string]bool, len(items))
	result := make([]ReportSummary, 0, len(items))
	for _, item := range items {
		key := item.IssueURL
		if key == "" {
			key = item.Title
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
		if len(result) >= 8 {
			break
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": result})
}

func (s *Service) githubSearch(ctx context.Context, query string) []ReportSummary {
	path := "/search/issues?q=" + url.QueryEscape("org:Stream-Hata is:issue "+query) + "&per_page=5"
	body, err := s.githubRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil
	}
	var payload struct {
		Items []struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			HTMLURL       string `json:"html_url"`
			State         string `json:"state"`
			RepositoryURL string `json:"repository_url"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	items := make([]ReportSummary, 0, len(payload.Items))
	for _, issue := range payload.Items {
		items = append(items, ReportSummary{
			Prefix:   repositoryPrefix(issue.RepositoryURL),
			Number:   issue.Number,
			Title:    issue.Title,
			Status:   issue.State,
			IssueURL: issue.HTMLURL,
			Source:   "github",
		})
	}

	return items
}

func repositoryPrefix(repositoryURL string) string {
	name := strings.ToLower(repositoryURL)
	switch {
	case strings.Contains(name, "backend"):
		return "BE"
	case strings.Contains(name, "desktop"):
		return "DT"
	case strings.Contains(name, "frontend"):
		return "FE"
	default:
		return ""
	}
}
