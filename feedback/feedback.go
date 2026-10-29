package feedback

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/vaultsecrets"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Config struct {
	GitHubToken          string            `json:"githubToken"`
	GitHubAppID          string            `json:"githubAppId"`
	GitHubInstallationID string            `json:"githubInstallationId"`
	GitHubPrivateKey     string            `json:"githubPrivateKey"`
	TelegramToken        string            `json:"telegramToken"`
	TelegramChat         string            `json:"telegramChat"`
	Repos                map[string]string `json:"repos"`
}

type Report struct {
	Area        string `json:"area"`
	Kind        string `json:"kind"`
	Reason      string `json:"reason"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Steps       string `json:"steps"`
	Expected    string `json:"expected"`
	Actual      string `json:"actual"`
	Version     string `json:"version"`
	Severity    string `json:"severity"`
	PageURL     string `json:"pageUrl"`
	Environment string `json:"environment"`
	Contact     string `json:"contact"`
	Author      string `json:"author"`
	Website     string `json:"website"`
}

type Result struct {
	Number      int    `json:"number"`
	Prefix      string `json:"prefix"`
	Issue       string `json:"issueUrl"`
	Kind        string `json:"kind"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
}

type Service struct {
	db              *sql.DB
	mu              sync.Mutex
	config          Config
	installationJWT string
	installationEnd time.Time
	lastSeen        map[string]time.Time
	http            *http.Client
}

func New(db *sql.DB) *Service {
	service := &Service{
		db:       db,
		config:   defaultConfig(),
		lastSeen: make(map[string]time.Time),
		http:     &http.Client{Timeout: 20 * time.Second},
	}
	service.load()

	return service
}

func defaultConfig() Config {
	return Config{
		Repos: map[string]string{
			"backend":  "Stream-Hata/nameless-streamhata-backend",
			"frontend": "Stream-Hata/stream-hata-frontend",
			"desktop":  "Stream-Hata/streamhata-desktop",
		},
	}
}

func (s *Service) load() {
	if data, err := vaultsecrets.GetSecret("streamhata-feedback"); err == nil {
		if raw := data["config"]; raw != "" {
			var config Config
			if json.Unmarshal([]byte(raw), &config) == nil {
				if config.Repos == nil {
					config.Repos = defaultConfig().Repos
				}
				s.config = config

				return
			}
		}
	}

	if s.db == nil {
		return
	}

	var raw []byte
	if err := s.db.QueryRowContext(context.Background(), `SELECT value FROM app_setting WHERE key = 'feedback_config'`).Scan(&raw); err != nil {
		return
	}
	var config Config
	if json.Unmarshal(raw, &config) == nil {
		if config.Repos == nil {
			config.Repos = defaultConfig().Repos
		}
		s.config = config
	}
}

func (s *Service) save() error {
	raw, err := json.Marshal(s.config)
	if err != nil {
		return err
	}
	if err := vaultsecrets.PutSecret("streamhata-feedback", map[string]string{"config": string(raw)}); err == nil {
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("OpenBao и база недоступны")
	}
	_, err = s.db.ExecContext(context.Background(), `INSERT INTO app_setting (key, value) VALUES ('feedback_config', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, string(raw))

	return err
}

func (s *Service) RegisterRoutes(srv Deps) {
	s.registerConfigRoute(srv)
	srv.ServeEndpoint("/rgg-sync/feedback", s.createHandler)
}

func (s *Service) createHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var report Report
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&report); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	if report.Website != "" {
		writeJSON(response, http.StatusOK, map[string]string{"ok": "true"})

		return
	}
	if err := s.validate(&report); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}
	if !s.allow(request.RemoteAddr) {
		writeJSON(response, http.StatusTooManyRequests, map[string]string{"error": "слишком часто"})

		return
	}

	result, err := s.create(request.Context(), report)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Service) validate(report *Report) error {
	report.Area = strings.ToLower(strings.TrimSpace(report.Area))
	report.Kind = strings.ToLower(strings.TrimSpace(report.Kind))
	report.Title = strings.TrimSpace(report.Title)
	report.Description = strings.TrimSpace(report.Description)

	if _, ok := map[string]string{"backend": "BE", "frontend": "FE", "desktop": "DE"}[report.Area]; !ok {
		return fmt.Errorf("выберите область: backend, frontend или desktop")
	}
	if _, ok := map[string]bool{"bug": true, "suggestion": true, "feedback": true}[report.Kind]; !ok {
		return fmt.Errorf("выберите тип: bug, suggestion или feedback")
	}
	if len([]rune(report.Title)) < 5 || len([]rune(report.Title)) > 180 {
		return fmt.Errorf("заголовок должен быть от 5 до 180 символов")
	}
	if len([]rune(report.Description)) < 15 {
		return fmt.Errorf("описание слишком короткое")
	}

	return nil
}

func (s *Service) allow(remote string) bool {
	host := remote
	if index := strings.LastIndex(remote, ":"); index > 0 {
		host = remote[:index]
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if last, ok := s.lastSeen[host]; ok && now.Sub(last) < 30*time.Second {
		return false
	}
	s.lastSeen[host] = now

	return true
}

func (s *Service) create(ctx context.Context, report Report) (Result, error) {
	prefix := map[string]string{"backend": "BE", "frontend": "FE", "desktop": "DE"}[report.Area]
	number, err := s.nextNumber(ctx, prefix)
	if err != nil {
		return Result{}, err
	}

	result := Result{Number: number, Prefix: prefix, Kind: report.Kind}
	s.mu.Lock()
	repo := s.config.Repos[report.Area]
	s.mu.Unlock()
	result.Repo = repo
	title := fmt.Sprintf("%s-%03d %s", prefix, number, report.Title)
	if report.Author != "" {
		title += " by " + report.Author
	}
	body := formatBody(report)
	status := "feedback"
	if report.Kind != "feedback" {
		issueURL, issueNumber, err := s.createIssue(ctx, report.Area, title, body)
		if err != nil {
			return Result{}, err
		}
		result.Issue = issueURL
		result.IssueNumber = issueNumber
		status = "created"
	}
	if s.db != nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO feedback_reports
			(prefix, number, area, kind, title, body, author, issue_url, status, repo)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			prefix, number, report.Area, report.Kind, title, body, report.Author, result.Issue, status, repo)
	}
	s.notify(ctx, title, report, result)

	return result, nil
}

func (s *Service) nextNumber(ctx context.Context, prefix string) (int, error) {
	if s.db == nil {
		return int(time.Now().Unix()%900 + 1), nil
	}
	var number int
	err := s.db.QueryRowContext(ctx, `INSERT INTO feedback_counter (prefix, value) VALUES ($1, 1)
		ON CONFLICT (prefix) DO UPDATE SET value = feedback_counter.value + 1
		RETURNING value`, prefix).Scan(&number)

	return number, err
}

func (s *Service) githubToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	config := s.config
	cached := s.installationJWT
	expires := s.installationEnd
	s.mu.Unlock()

	if config.GitHubToken != "" {
		return config.GitHubToken, nil
	}
	if config.GitHubAppID == "" || config.GitHubInstallationID == "" || config.GitHubPrivateKey == "" {
		return "", fmt.Errorf("GitHub App не настроен")
	}
	if cached != "" && time.Now().Before(expires.Add(-time.Minute)) {
		return cached, nil
	}

	jwt, err := githubJWT(config.GitHubAppID, config.GitHubPrivateKey)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", config.GitHubInstallationID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := s.http.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub App: %s", response.Status)
	}
	var payload struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.installationJWT = payload.Token
	s.installationEnd = payload.ExpiresAt
	s.mu.Unlock()

	return payload.Token, nil
}

func githubJWT(appID, privateKey string) (string, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return "", fmt.Errorf("неверный приватный ключ GitHub App")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		rsaKey, rsaErr := x509.ParsePKCS1PrivateKey(block.Bytes)
		if rsaErr != nil {
			return "", fmt.Errorf("неверный приватный ключ GitHub App")
		}
		key = rsaKey
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("ожидается RSA-ключ GitHub App")
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	encode := base64.RawURLEncoding.EncodeToString
	signing := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}

	return signing + "." + encode(signature), nil
}

func (s *Service) createIssue(ctx context.Context, area, title, body string) (string, int, error) {
	s.mu.Lock()
	config := s.config
	s.mu.Unlock()

	repo := config.Repos[area]
	if repo == "" {
		return "", 0, fmt.Errorf("GitHub не настроен для %s", area)
	}
	token, err := s.githubToken(ctx)
	if err != nil {
		return "", 0, err
	}
	payload, _ := json.Marshal(map[string]any{"title": title, "body": body, "assignees": []string{"p7x_dev"}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/repos/"+repo+"/issues", strings.NewReader(string(payload)))
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		var errorPayload struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(response.Body).Decode(&errorPayload)
		if errorPayload.Message == "" {
			errorPayload.Message = response.Status
		}

		return "", 0, fmt.Errorf("GitHub: %s", errorPayload.Message)
	}
	var issue struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := json.NewDecoder(response.Body).Decode(&issue); err != nil {
		return "", 0, err
	}

	return issue.HTMLURL, issue.Number, nil
}

func (s *Service) notify(ctx context.Context, title string, report Report, result Result) {
	s.mu.Lock()
	config := s.config
	s.mu.Unlock()
	if config.TelegramToken == "" || config.TelegramChat == "" {
		return
	}
	text := fmt.Sprintf("📝 %s\n%s\nАвтор: %s", title, strings.ToUpper(report.Kind), report.Author)
	if result.Issue != "" {
		text += "\n" + result.Issue
	}
	payloadMap := map[string]any{"chat_id": config.TelegramChat, "text": text, "disable_web_page_preview": false}
	if result.IssueNumber > 0 {
		key := fmt.Sprintf("%s-%03d", result.Prefix, result.Number)
		payloadMap["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{{
			{"text": "Закрыть issue", "callback_data": "fb:close:" + key},
		}}}
	}
	payload, _ := json.Marshal(payloadMap)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+config.TelegramToken+"/sendMessage", strings.NewReader(string(payload)))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func formatBody(report Report) string {
	var body strings.Builder
	body.WriteString("### Описание\n\n" + report.Description + "\n")
	if report.Steps != "" {
		body.WriteString("\n### Шаги воспроизведения\n\n" + report.Steps + "\n")
	}
	if report.Expected != "" {
		body.WriteString("\n### Ожидаемое поведение\n\n" + report.Expected + "\n")
	}
	if report.Actual != "" {
		body.WriteString("\n### Фактическое поведение\n\n" + report.Actual + "\n")
	}
	if report.Reason != "" {
		body.WriteString("\n### Причина\n\n" + report.Reason + "\n")
	}
	body.WriteString("\n### Окружение\n\n")
	body.WriteString("- Область: " + report.Area + "\n")
	body.WriteString("- Версия: " + report.Version + "\n")
	if report.Severity != "" {
		body.WriteString("- Критичность: " + report.Severity + "\n")
	}
	if report.PageURL != "" {
		body.WriteString("- Страница: " + report.PageURL + "\n")
	}
	if report.Environment != "" {
		body.WriteString("- Окружение: " + report.Environment + "\n")
	}
	body.WriteString("- Автор: " + report.Author + "\n")
	if report.Contact != "" {
		body.WriteString("- Контакт: " + report.Contact + "\n")
	}

	return body.String()
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])
}

type githubIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

func (s *Service) githubList(ctx context.Context, repo string) ([]githubIssue, error) {
	body, err := s.githubRequest(ctx, http.MethodGet, "/repos/"+repo+"/issues?state=open&per_page=10", nil)
	if err != nil {
		return nil, err
	}
	var issues []githubIssue
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, err
	}

	return issues, nil
}

func (s *Service) githubState(ctx context.Context, repo string, number int, state string) error {
	payload, _ := json.Marshal(map[string]string{"state": state})
	_, err := s.githubRequest(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, number), payload)

	return err
}

func (s *Service) githubRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	token, err := s.githubToken(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub: %s", response.Status)
	}
	return io.ReadAll(response.Body)
}
