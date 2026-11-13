package feedback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxDiagnosticsRunes = 120000

type diagnosticLine struct {
	timestamp time.Time
	source    string
	text      string
}

var diagnosticsPeriods = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

var diagnosticsSources = []struct {
	Key   string
	Label string
	Query string
}{
	{"backend", "Backend (nsh-backend)", `{host="streamhata-main", unit="streamhata-backend.service"}`},
	{"nginx", "Nginx", `{host="streamhata-main", job="nginx"}`},
	{"database", "Database server (journal)", `{host="db-2"}`},
	{"postgres", "PostgreSQL", `{host="db-2", unit="postgresql.service"}`},
	{"openbao", "OpenBao", `{host="db-2", unit="openbao.service"}`},
}

var diagnosticsMetrics = []struct {
	Label string
	Query string
}{
	{"CPU, %", `100 - (avg by (host) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`},
	{"Memory, %", `100 * (1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)`},
	{"Disk, %", `100 * (1 - node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|squashfs"} / node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs"})`},
	{"Load 1m", `node_load1`},
	{"Backend up", `nsh_up`},
	{"Database reachable", `nsh_db_up`},
	{"Goroutines", `nsh_goroutines`},
	{"Backend memory, bytes", `nsh_memory_alloc_bytes`},
	{"PostgreSQL up", `pg_up`},
	{"PostgreSQL connections", `pg_stat_database_numbackends{datname="rgg"}`},
	{"PostgreSQL size, bytes", `pg_database_size_bytes{datname="rgg"}`},
	{"OpenBao unsealed", `vault_core_unsealed`},
}

var diagnosticsPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization|proxy-authorization)\s*:\s*(?:bearer|basic)\s+[^\s]+`), `${1}: [REDACTED]`},
	{regexp.MustCompile(`(?i)(cookie|set-cookie)\s*:\s*[^\r\n]+`), `${1}: [REDACTED]`},
	{regexp.MustCompile(`(?i)(x-vault-token)\s*:\s*[^\s]+`), `${1}: [REDACTED]`},
	{regexp.MustCompile(`(?i)("(?:access_token|refresh_token|client_secret|accessToken|refreshToken|clientSecret|password|secret|api[_-]?key|apiKey|token)"\s*:\s*")[^"]*`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)\b(access_token|refresh_token|client_secret|accessToken|refreshToken|clientSecret|password|passwd|secret|api[_-]?key|apiKey|token)\b\s*[=:]\s*[^&\s"']+`), `${1}=[REDACTED]`},
	{regexp.MustCompile(`(?i)([?&](?:access_token|refresh_token|token|code|key|secret|password)=)[^&\s]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`), `Bearer [REDACTED]`},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), `[REDACTED_JWT]`},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`), `[REDACTED_GITHUB_TOKEN]`},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), `[REDACTED_API_KEY]`},
	{regexp.MustCompile(`(?i)\boauth:[a-z0-9]{20,}\b`), `oauth:[REDACTED]`},
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}

	return fallback
}

func diagnosticsSourceAllowed(source string) bool {
	if source == "metrics" {
		return true
	}
	for _, item := range diagnosticsSources {
		if item.Key == source {
			return true
		}
	}

	return false
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit]) + "\n…[truncated]"
}

func sanitizeDiagnostics(value string) string {
	for _, pattern := range diagnosticsPatterns {
		value = pattern.re.ReplaceAllString(value, pattern.repl)
	}

	return value
}

func (s *Service) collectDiagnostics(ctx context.Context, request DiagnosticsRequest, author string) string {
	period, ok := diagnosticsPeriods[request.Period]
	if !ok {
		return ""
	}
	end := time.Now().UTC()
	start := end.Add(-period)
	selected := make(map[string]bool, len(request.Sources))
	for _, source := range request.Sources {
		selected[source] = true
	}

	var result strings.Builder
	fmt.Fprintf(&result, "Период: %s — %s\n", start.Format(time.RFC3339), end.Format(time.RFC3339))
	if author != "" {
		fmt.Fprintf(&result, "Пользователь: %s\n", author)
	}
	lines := make([]diagnosticLine, 0)
	for _, source := range diagnosticsSources {
		if !selected[source.Key] {
			continue
		}
		content, err := s.queryLoki(ctx, source.Query, start, end)
		if err != nil {
			fmt.Fprintf(&result, "\n## %s\nОшибка: %v\n", source.Label, err)
			continue
		}
		for _, line := range content {
			line.source = source.Label
			lines = append(lines, line)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].timestamp.Equal(lines[j].timestamp) {
			return lines[i].source < lines[j].source
		}

		return lines[i].timestamp.Before(lines[j].timestamp)
	})
	if len(lines) == 0 {
		result.WriteString("\n## Логи\nНет записей.\n")
	} else {
		result.WriteString("\n## Логи (по времени)\n")
		for _, line := range lines {
			fmt.Fprintf(&result, "%s [%s] %s\n", line.timestamp.UTC().Format(time.RFC3339), line.source, line.text)
		}
	}

	if selected["metrics"] {
		result.WriteString("\n## Prometheus\n")
		for _, metric := range diagnosticsMetrics {
			content, err := s.queryPrometheus(ctx, metric.Query, start, end)
			if err != nil {
				fmt.Fprintf(&result, "%s: ошибка: %v\n", metric.Label, err)
				continue
			}
			if strings.TrimSpace(content) == "" {
				fmt.Fprintf(&result, "%s: нет данных\n", metric.Label)
				continue
			}
			fmt.Fprintf(&result, "%s:\n%s", metric.Label, content)
		}
	}

	return truncateRunes(sanitizeDiagnostics(result.String()), maxDiagnosticsRunes)
}

func (s *Service) queryLoki(ctx context.Context, query string, start, end time.Time) ([]diagnosticLine, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("limit", "300")
	params.Set("direction", "backward")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.lokiURL, "/")+"/loki/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := s.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))

		return nil, fmt.Errorf("Loki %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("Loki status: %s", payload.Status)
	}

	result := make([]diagnosticLine, 0)
	for _, stream := range payload.Data.Result {
		labels := formatDiagnosticsLabels(stream.Stream)
		for _, value := range stream.Values {
			line := strings.TrimSpace(value[1])
			if line == "" {
				continue
			}
			timestamp, _ := strconv.ParseInt(value[0], 10, 64)
			result = append(result, diagnosticLine{
				timestamp: time.Unix(0, timestamp),
				text:      labels + " " + truncateRunes(line, 2000),
			})
		}
	}

	return result, nil
}

func (s *Service) queryPrometheus(ctx context.Context, query string, start, end time.Time) (string, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	params.Set("step", "60s")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.prometheusURL, "/")+"/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := s.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))

		return "", fmt.Errorf("Prometheus %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Status != "success" {
		return "", fmt.Errorf("Prometheus status: %s", payload.Status)
	}

	var result strings.Builder
	for _, series := range payload.Data.Result {
		values := make([]float64, 0, len(series.Values))
		for _, pair := range series.Values {
			if len(pair) < 2 {
				continue
			}
			raw, ok := pair[1].(string)
			if !ok {
				continue
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err == nil {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			continue
		}
		minValue, maxValue, sum := values[0], values[0], 0.0
		for _, value := range values {
			if value < minValue {
				minValue = value
			}
			if value > maxValue {
				maxValue = value
			}
			sum += value
		}
		fmt.Fprintf(&result, "%s min=%.3g avg=%.3g max=%.3g\n",
			formatDiagnosticsLabels(series.Metric), minValue, sum/float64(len(values)), maxValue)
	}

	return result.String(), nil
}

func formatDiagnosticsLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if key == "__name__" || key == "instance" || key == "job" || key == "service_name" || key == "hostname" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	if len(parts) == 0 {
		return "all"
	}

	return strings.Join(parts, ",")
}
