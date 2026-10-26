// Package metrics — сводные метрики для приложения и Telegram-дайджеста.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/analytics"
	"github.com/p7x-dev/nameless-streamhata-backend/donations"
	"github.com/p7x-dev/nameless-streamhata-backend/music"
	"github.com/p7x-dev/nameless-streamhata-backend/points"
)

type Service struct {
	analytics *analytics.Service
	donations *donations.Bus
	points    *points.Service
	music     *music.Service

	telegram func(ctx context.Context, text string) error
}

func New(
	analyticsService *analytics.Service,
	donationsBus *donations.Bus,
	pointsService *points.Service,
	musicService *music.Service,
) *Service {
	return &Service{
		analytics: analyticsService,
		donations: donationsBus,
		points:    pointsService,
		music:     musicService,
	}
}

func (s *Service) SetTelegramSender(sender func(ctx context.Context, text string) error) {
	s.telegram = sender
}

func (s *Service) ID() string { return "metrics" }

func (s *Service) Name() string { return "Метрики" }

func (s *Service) Kind() string { return "metrics" }

func (s *Service) Enabled() bool { return true }

func (s *Service) SetEnabled(bool) error { return nil }

func (s *Service) Status() string { return "дайджест в Telegram" }

type Snapshot struct {
	Session     *analytics.Session  `json:"session"`
	History     []analytics.Session `json:"history"`
	Donations   donations.Stats     `json:"donations"`
	TopPoints   []points.Wallet     `json:"topPoints"`
	Music       music.Stats         `json:"music"`
	GeneratedAt time.Time           `json:"generatedAt"`
}

func (s *Service) Snapshot() Snapshot {
	return Snapshot{
		Session:     s.analytics.Current(),
		History:     s.analytics.History(),
		Donations:   s.donations.Stats(),
		TopPoints:   s.points.Top(5),
		Music:       s.music.Stats(),
		GeneratedAt: time.Now().UTC(),
	}
}

// DigestText собирает HTML-сообщение для Telegram.
func (s *Service) DigestText() string {
	snapshot := s.Snapshot()
	lines := []string{"<b>📊 Метрики Nameless StreamHata</b>"}

	if session := snapshot.Session; session != nil {
		lines = append(lines, fmt.Sprintf(
			"Эфир: пик <b>%d</b>, среднее <b>%d</b>, сообщений <b>%d</b>",
			session.PeakViewers,
			session.AvgViewers,
			session.Messages,
		))
	} else {
		lines = append(lines, "Эфир: не идёт")
	}

	donationParts := make([]string, 0, len(snapshot.Donations.Totals))

	for currency, total := range snapshot.Donations.Totals {
		donationParts = append(donationParts, fmt.Sprintf("%.0f %s", total, currency))
	}

	if len(donationParts) > 0 {
		lines = append(lines, "Донаты: "+strings.Join(donationParts, " · "))
	}

	if len(snapshot.TopPoints) > 0 {
		top := make([]string, 0, len(snapshot.TopPoints))

		for index, wallet := range snapshot.TopPoints {
			top = append(top, fmt.Sprintf("%d) %s — %d", index+1, escape(wallet.DisplayName), wallet.Balance))
		}

		lines = append(lines, "Баллы: "+strings.Join(top, ", "))
	}

	lines = append(lines, fmt.Sprintf(
		"Музыка: %d треков · %s",
		snapshot.Music.TotalTracks,
		formatDuration(snapshot.Music.TotalDuration),
	))

	if len(snapshot.History) > 0 {
		lines = append(lines, fmt.Sprintf("Сессий в архиве: %d", len(snapshot.History)))
	}

	return strings.Join(lines, "\n")
}

func (s *Service) SendDigestTelegram(ctx context.Context) error {
	if s.telegram == nil {
		return fmt.Errorf("Telegram не настроен")
	}

	return s.telegram(ctx, s.DigestText())
}

func formatDuration(total int) string {
	hours := total / 3600
	minutes := (total % 3600) / 60

	if hours > 0 {
		return fmt.Sprintf("%dч %02dм", hours, minutes)
	}

	return fmt.Sprintf("%dм", minutes)
}

func escape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

/* ── Роуты ────────────────────────────────────────────────────────── */

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/metrics", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, service.Snapshot())
	})

	srv.ServeEndpoint("/rgg-sync/metrics/telegram", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if err := service.SendDigestTelegram(request.Context()); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"ok": true})
	})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
