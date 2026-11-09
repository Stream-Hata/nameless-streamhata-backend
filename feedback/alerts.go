package feedback

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type grafanaAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
}

type grafanaAlertPayload struct {
	Status string         `json:"status"`
	Alerts []grafanaAlert `json:"alerts"`
}

func (s *Service) grafanaAlertHandler(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	expected := s.alertWebhookToken()
	provided := strings.TrimSpace(request.URL.Query().Get("token"))
	if expected == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})

		return
	}
	var payload grafanaAlertPayload
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 128<<10)).Decode(&payload); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	for _, alert := range payload.Alerts {
		s.syncStatusIncident(request.Context(), alert, payload.Status)
	}

	text := formatGrafanaAlert(payload)
	if text == "" {
		writeJSON(response, http.StatusOK, map[string]any{"ok": true, "sent": false})

		return
	}
	if err := s.sendTelegramAlert(request.Context(), text); err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"ok": true, "sent": true})
}

type incidentTarget struct {
	code     string
	codename string
	service  string
}

func alertTarget(labels map[string]string) incidentTarget {
	raw := strings.ToLower(strings.Join([]string{
		labels["service"], labels["job"], labels["app"], labels["alertname"], labels["instance"],
	}, " "))

	switch {
	case strings.Contains(raw, "postgres"), strings.Contains(raw, "database"), strings.Contains(raw, " pg"):
		return incidentTarget{"DB", "PostgreSQL", "database"}
	case strings.Contains(raw, "loki"), strings.Contains(raw, "prometheus"), strings.Contains(raw, "grafana"), strings.Contains(raw, "node_exporter"):
		return incidentTarget{"MON", "Мониторинг", "monitoring"}
	case strings.Contains(raw, "obs"):
		return incidentTarget{"OBS", "OBS / стрим", "obs"}
	case strings.Contains(raw, "twitch"), strings.Contains(raw, "auth"), strings.Contains(raw, "oauth"):
		return incidentTarget{"AUTH", "Авторизация", "auth"}
	case strings.Contains(raw, "backend"), strings.Contains(raw, "api"), strings.Contains(raw, "nsh"):
		return incidentTarget{"API", "Backend API", "api"}
	case strings.Contains(raw, "nginx"), strings.Contains(raw, "web"), strings.Contains(raw, "frontend"):
		return incidentTarget{"WEB", "Веб-панель", "web"}
	case strings.Contains(raw, "music"), strings.Contains(raw, "media"), strings.Contains(raw, "audio"):
		return incidentTarget{"MEDIA", "Медиа", "media"}
	default:
		service := strings.TrimSpace(labels["service"])
		if service == "" {
			service = strings.TrimSpace(labels["job"])
		}
		if service == "" {
			service = "service"
		}
		code := strings.ToUpper(strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, service))
		code = strings.Trim(code, "-")
		if len(code) > 32 {
			code = code[:32]
		}
		if code == "" {
			code = "SVC"
		}
		return incidentTarget{code, service, service}
	}
}

func alertSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "fatal", "error":
		return "critical"
	case "warning", "warn":
		return "major"
	default:
		return "minor"
	}
}

func (s *Service) syncStatusIncident(ctx context.Context, alert grafanaAlert, payloadStatus string) {
	if s.db == nil {
		return
	}
	target := alertTarget(alert.Labels)
	status := strings.ToLower(strings.TrimSpace(alert.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(payloadStatus))
	}
	title := firstNonEmpty(alert.Labels["alertname"], "Инцидент")
	title = title + " · " + target.codename
	description := firstNonEmpty(alert.Annotations["description"], alert.Annotations["summary"])
	severity := alertSeverity(alert.Labels["severity"])

	if status == "resolved" {
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM status_incidents
			WHERE code=$1 AND resolved_at IS NULL ORDER BY id DESC LIMIT 1`, target.code).Scan(&id)
		if err != nil {
			return
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE status_incidents
			SET status='resolved', resolved_at=now(), updated_at=now()
			WHERE id=$1`, id)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO status_incident_updates (incident_id,status,message)
			VALUES ($1,'resolved',$2)`, id, "Автоматически восстановлено по данным мониторинга")
		return
	}

	var id int64
	var currentSeverity string
	var currentTitle string
	err := s.db.QueryRowContext(ctx, `SELECT id,severity,title FROM status_incidents
		WHERE code=$1 AND resolved_at IS NULL ORDER BY id DESC LIMIT 1`, target.code).
		Scan(&id, &currentSeverity, &currentTitle)
	if err == nil {
		if currentSeverity == severity && currentTitle == title {
			return
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE status_incidents
			SET title=$2, description=$3, severity=$4, updated_at=now()
			WHERE id=$1`, id, title, description, severity)
		if currentSeverity != severity {
			_, _ = s.db.ExecContext(ctx, `INSERT INTO status_incident_updates (incident_id,status,message)
				VALUES ($1,'investigating',$2)`, id, "Изменена важность инцидента: "+severity)
		}
		return
	}

	var resolvedAt *time.Time
	err = s.db.QueryRowContext(ctx, `SELECT id,resolved_at FROM status_incidents
		WHERE code=$1 ORDER BY id DESC LIMIT 1`, target.code).Scan(&id, &resolvedAt)
	if err == nil && resolvedAt != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE status_incidents
			SET title=$2, description=$3, severity=$4, status='investigating',
				started_at=now(), resolved_at=NULL, updated_at=now()
			WHERE id=$1`, id, title, description, severity)
		_, _ = s.db.ExecContext(ctx, `INSERT INTO status_incident_updates (incident_id,status,message)
			VALUES ($1,'investigating',$2)`, id, "Инцидент повторился")
		return
	}

	_, _ = s.db.ExecContext(ctx, `INSERT INTO status_incidents
		(code,codename,title,description,severity,status,started_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,'investigating',now(),now())
		ON CONFLICT (code) DO NOTHING`,
		target.code, target.codename, title, description, severity)
}

func formatGrafanaAlert(payload grafanaAlertPayload) string {
	if len(payload.Alerts) == 0 {
		return ""
	}
	var result strings.Builder
	state := strings.ToUpper(strings.TrimSpace(payload.Status))
	if state == "" {
		state = "ALERT"
	}
	fmt.Fprintf(&result, "%s · Grafana\n", state)
	for _, alert := range payload.Alerts {
		labels := alert.Labels
		name := firstNonEmpty(labels["alertname"], labels["__alert_rule_title__"], "Alert")
		fmt.Fprintf(&result, "\n• %s\n", name)
		for _, key := range []string{"severity", "service", "host", "instance"} {
			if value := labels[key]; value != "" {
				fmt.Fprintf(&result, "  %s: %s\n", key, value)
			}
		}
		if summary := alert.Annotations["summary"]; summary != "" {
			fmt.Fprintf(&result, "  %s\n", summary)
		}
		if description := alert.Annotations["description"]; description != "" {
			fmt.Fprintf(&result, "  %s\n", description)
		}
	}
	return strings.TrimSpace(result.String())
}

func (s *Service) sendTelegramAlert(ctx context.Context, text string) error {
	s.mu.Lock()
	chat := strings.TrimSpace(s.config.TelegramChat)
	s.mu.Unlock()

	return s.sendTelegramTo(ctx, chat, text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}

	return ""
}
