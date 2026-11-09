package statuspage

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/vaultsecrets"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type ServiceStatus struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Detail    string   `json:"detail"`
	LatencyMS int64    `json:"latencyMs"`
	Uptime    *float64 `json:"uptime,omitempty"`
}

type Incident struct {
	ID          int64            `json:"id"`
	Code        string           `json:"code"`
	Codename    string           `json:"codename"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Severity    string           `json:"severity"`
	Status      string           `json:"status"`
	StartedAt   time.Time        `json:"startedAt"`
	ResolvedAt  *time.Time       `json:"resolvedAt,omitempty"`
	UpdatedAt   time.Time        `json:"updatedAt"`
	Message     string           `json:"message,omitempty"`
	Updates     []IncidentUpdate `json:"updates"`
}

type IncidentUpdate struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type Snapshot struct {
	Status    string          `json:"status"`
	CheckedAt time.Time       `json:"checkedAt"`
	Services  []ServiceStatus `json:"services"`
	Incidents []Incident      `json:"incidents"`
}

type Service struct {
	db         *sql.DB
	http       *http.Client
	lokiURL    string
	promURL    string
	grafanaURL string
	token      string
	mail       *Mailer
}

func New(db *sql.DB) *Service {
	return &Service{
		db:         db,
		http:       &http.Client{Timeout: 3 * time.Second},
		lokiURL:    envOr("LOKI_URL", "http://10.66.0.2:3100"),
		promURL:    envOr("PROMETHEUS_URL", "http://10.66.0.2:9090"),
		grafanaURL: envOr("GRAFANA_URL", "http://10.66.0.2:3000"),
		token:      statusSecret("statusToken"),
		mail:       NewMailer(statusSecret("smtpPassword")),
	}
}

func statusSecret(key string) string {
	values, err := vaultsecrets.GetSecret("streamhata-status")
	if err != nil {
		return ""
	}

	return strings.TrimSpace(values[key])
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/status", service.snapshotHandler)
	srv.ServeEndpoint("/rgg-sync/status/incidents", service.incidentsHandler)
	srv.ServeEndpoint("/rgg-sync/status/incidents/{id}", service.incidentUpdateHandler)
}

func (s *Service) snapshotHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	writeJSON(response, http.StatusOK, s.snapshot(request.Context()))
}

func (s *Service) snapshot(ctx context.Context) Snapshot {
	services := []ServiceStatus{
		{Key: "backend", Name: "Backend", State: "operational", Detail: "процесс работает"},
	}
	services = append(services, s.checkDatabase(ctx))
	if k3sURL := strings.TrimSpace(os.Getenv("K3S_HEALTH_URL")); k3sURL != "" {
		item := s.checkHTTP(ctx, "kubernetes", "Kubernetes (K3s)", k3sURL)
		if item.State == "operational" {
			item.Detail = envOr("K3S_DETAIL", "control-plane работает")
		}
		services = append(services, item)
	}
	services = append(services,
		s.checkHTTP(ctx, "loki", "Loki", strings.TrimRight(s.lokiURL, "/")+"/ready"),
		s.checkHTTP(ctx, "prometheus", "Prometheus", strings.TrimRight(s.promURL, "/")+"/-/ready"),
		s.checkHTTP(ctx, "grafana", "Grafana", strings.TrimRight(s.grafanaURL, "/")+"/api/health"),
	)

	for index := range services {
		services[index].Uptime = s.uptime(ctx, services[index].Key)
	}

	overall := "operational"
	for _, service := range services {
		if service.State == "down" && (service.Key == "backend" || service.Key == "database") {
			overall = "down"
		}
	}
	if overall != "down" {
		for _, service := range services {
			if service.State != "operational" {
				overall = "degraded"

				break
			}
		}
	}

	return Snapshot{
		Status:    overall,
		CheckedAt: time.Now().UTC(),
		Services:  services,
		Incidents: s.incidents(ctx),
	}
}

func (s *Service) uptime(ctx context.Context, key string) *float64 {
	queries := map[string]string{
		"backend":  `avg_over_time(max(up{job=~"streamhata-backend-.*"})[15d:5m]) * 100`,
		"database": `avg_over_time(up{job="postgres"}[15d]) * 100`,
	}
	query := queries[key]
	if query == "" {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, strings.TrimRight(s.promURL, "/")+"/api/v1/query?query="+url.QueryEscape(query), nil)
	if err != nil {
		return nil
	}
	response, err := s.http.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(response.Body).Decode(&payload) != nil || len(payload.Data.Result) == 0 {
		return nil
	}
	raw, ok := payload.Data.Result[0].Value[1].(string)
	if !ok {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}

	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}

func (s *Service) checkDatabase(ctx context.Context) ServiceStatus {
	start := time.Now()
	status := ServiceStatus{Key: "database", Name: "PostgreSQL", State: "operational", Detail: "доступна"}
	if s.db == nil {
		status.State = "down"
		status.Detail = "нет подключения"

		return status
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(checkCtx); err != nil {
		status.State = "down"
		status.Detail = "ping failed"
	} else {
		status.LatencyMS = time.Since(start).Milliseconds()
	}

	return status
}

func (s *Service) checkHTTP(ctx context.Context, key, name, endpoint string) ServiceStatus {
	start := time.Now()
	status := ServiceStatus{Key: key, Name: name, State: "operational", Detail: "доступен"}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(checkCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.State = "down"
		status.Detail = err.Error()

		return status
	}
	response, err := s.http.Do(request)
	if err != nil {
		status.State = "down"
		status.Detail = err.Error()

		return status
	}
	defer response.Body.Close()
	status.LatencyMS = time.Since(start).Milliseconds()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status.State = "down"
		status.Detail = fmt.Sprintf("HTTP %d", response.StatusCode)
	}

	return status
}

func (s *Service) incidentsHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		writeJSON(response, http.StatusOK, map[string]any{"items": s.incidents(request.Context())})

		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	if !s.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}
	if s.db == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})

		return
	}
	var input Incident
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 32<<10)).Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	normalizeIncident(&input)
	if input.Code == "" {
		input.Code = "INC-" + time.Now().UTC().Format("20060102-1504")
	}
	if input.Codename == "" {
		input.Codename = input.Code
	}
	if input.Title == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "title is required"})

		return
	}
	var incident Incident
	var resolved sql.NullTime
	err := s.db.QueryRowContext(request.Context(), `INSERT INTO status_incidents
		(code, codename, title, description, severity, status)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, code, codename, title, description, severity, status, started_at, resolved_at, updated_at`,
		input.Code, input.Codename, input.Title, input.Description, input.Severity, input.Status).
		Scan(&incident.ID, &incident.Code, &incident.Codename, &incident.Title, &incident.Description,
			&incident.Severity, &incident.Status, &incident.StartedAt, &resolved, &incident.UpdatedAt)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"error": err.Error()})

		return
	}
	if resolved.Valid {
		incident.ResolvedAt = &resolved.Time
	}
	s.addIncidentUpdate(request.Context(), incident.ID, incident.Status, firstNonEmpty(input.Message, "Инцидент создан"))
	s.sendIncidentMail(incident, "создан")
	writeJSON(response, http.StatusCreated, incident)
}

func (s *Service) incidentUpdateHandler(response http.ResponseWriter, request *http.Request) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid id"})

		return
	}
	if request.Method == http.MethodGet {
		incident, err := s.incidentByID(request.Context(), id)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "incident not found"})

			return
		}
		writeJSON(response, http.StatusOK, incident)

		return
	}
	if request.Method != http.MethodPatch && request.Method != http.MethodPut {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	if !s.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}
	var input Incident
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 32<<10)).Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	normalizeIncident(&input)
	if input.Status == "resolved" {
		_, err = s.db.ExecContext(request.Context(), `UPDATE status_incidents SET
			status=$1, title=COALESCE(NULLIF($2,''), title), description=COALESCE(NULLIF($3,''), description),
			severity=COALESCE(NULLIF($4,''), severity), codename=COALESCE(NULLIF($5,''), codename),
			resolved_at=COALESCE(resolved_at, now()), updated_at=now() WHERE id=$6`,
			input.Status, input.Title, input.Description, input.Severity, input.Codename, id)
	} else {
		_, err = s.db.ExecContext(request.Context(), `UPDATE status_incidents SET
			status=$1, title=COALESCE(NULLIF($2,''), title), description=COALESCE(NULLIF($3,''), description),
			severity=COALESCE(NULLIF($4,''), severity), codename=COALESCE(NULLIF($5,''), codename),
			resolved_at=NULL, updated_at=now() WHERE id=$6`,
			input.Status, input.Title, input.Description, input.Severity, input.Codename, id)
	}
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}
	s.addIncidentUpdate(request.Context(), id, input.Status, firstNonEmpty(input.Message, "Обновление инцидента"))
	if incident, err := s.incidentByID(request.Context(), id); err == nil {
		s.sendIncidentMail(incident, "обновлён")
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) addIncidentUpdate(ctx context.Context, incidentID int64, status, message string) {
	if s.db == nil {
		return
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO status_incident_updates (incident_id, status, message)
		VALUES ($1,$2,$3)`, incidentID, status, message)
}

func (s *Service) incidentUpdates(ctx context.Context, incidentID int64) []IncidentUpdate {
	if s.db == nil {
		return []IncidentUpdate{}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, message, created_at
		FROM status_incident_updates WHERE incident_id=$1 ORDER BY created_at ASC`, incidentID)
	if err != nil {
		return []IncidentUpdate{}
	}
	defer rows.Close()
	items := make([]IncidentUpdate, 0)
	for rows.Next() {
		var item IncidentUpdate
		if rows.Scan(&item.ID, &item.Status, &item.Message, &item.CreatedAt) == nil {
			items = append(items, item)
		}
	}

	return items
}

func (s *Service) incidentByID(ctx context.Context, id int64) (Incident, error) {
	var item Incident
	var resolved sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id, code, codename, title, description, severity, status,
		started_at, resolved_at, updated_at FROM status_incidents WHERE id = $1`, id).
		Scan(&item.ID, &item.Code, &item.Codename, &item.Title, &item.Description, &item.Severity,
			&item.Status, &item.StartedAt, &resolved, &item.UpdatedAt)
	if err != nil {
		return item, err
	}
	if resolved.Valid {
		item.ResolvedAt = &resolved.Time
	}
	item.Updates = s.incidentUpdates(ctx, item.ID)

	return item, nil
}

func (s *Service) sendIncidentMail(incident Incident, action string) {
	if s.mail == nil || !s.mail.Enabled() {
		return
	}
	subject := fmt.Sprintf("[StreamHata] %s / %s — %s", incident.Code, incident.Codename, incident.Status)
	body := fmt.Sprintf("Инцидент %s (%s) %s.\n\n%s\n\nСтатус: %s\nКритичность: %s\nНачало: %s\n",
		incident.Code, incident.Codename, action, incident.Title, incident.Status, incident.Severity,
		incident.StartedAt.Format(time.RFC3339))
	if incident.Description != "" {
		body += "\n" + incident.Description + "\n"
	}
	_ = s.mail.Send(subject, body)
}

func (s *Service) incidents(ctx context.Context) []Incident {
	if s.db == nil {
		return []Incident{}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, code, codename, title, description, severity, status,
		started_at, resolved_at, updated_at FROM status_incidents
		ORDER BY (resolved_at IS NULL) DESC, started_at DESC LIMIT 30`)
	if err != nil {
		return []Incident{}
	}
	defer rows.Close()
	items := make([]Incident, 0)
	for rows.Next() {
		var item Incident
		var resolved sql.NullTime
		if rows.Scan(&item.ID, &item.Code, &item.Codename, &item.Title, &item.Description, &item.Severity,
			&item.Status, &item.StartedAt, &resolved, &item.UpdatedAt) != nil {
			continue
		}
		if resolved.Valid {
			item.ResolvedAt = &resolved.Time
		}
		item.Updates = s.incidentUpdates(ctx, item.ID)
		items = append(items, item)
	}

	return items
}

func (s *Service) AdminToken() string {
	return s.token
}

func (s *Service) authorized(request *http.Request) bool {
	if s.token == "" {
		return false
	}
	provided := request.Header.Get("X-Status-Token")
	if provided == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}

func normalizeIncident(incident *Incident) {
	incident.Code = strings.ToUpper(strings.TrimSpace(incident.Code))
	incident.Codename = strings.TrimSpace(incident.Codename)
	incident.Title = strings.TrimSpace(incident.Title)
	incident.Description = strings.TrimSpace(incident.Description)
	incident.Severity = strings.ToLower(strings.TrimSpace(incident.Severity))
	incident.Status = strings.ToLower(strings.TrimSpace(incident.Status))
	if incident.Severity == "" {
		incident.Severity = "minor"
	}
	if incident.Status == "" {
		incident.Status = "investigating"
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return fallback
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
