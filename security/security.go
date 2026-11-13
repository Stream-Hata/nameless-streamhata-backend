package security

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/access"
)

const (
	StatusPendingReview = "pending_review"
	StatusAcknowledged  = "acknowledged"
	StatusConfirmed     = "confirmed_fraud"
	StatusFalsePositive = "false_positive"
	StatusAutoBanned    = "auto_banned"
	StatusClosed        = "closed"

	ActionConfirmFraud  = "confirm_fraud"
	ActionFalsePositive = "false_positive"
	ActionClose         = "close"

	BanReason = "Попытка мошенничества"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Service struct {
	db       *sql.DB
	access   *access.Service
	notifier func(context.Context, Incident) error
	scanner  *Scanner
	links    *LinkChecker
}

type Incident struct {
	ID             int64           `json:"id"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	Kind           string          `json:"kind"`
	Verdict        string          `json:"verdict"`
	Severity       string          `json:"severity"`
	Status         string          `json:"status"`
	ChatType       string          `json:"chatType"`
	ChatID         string          `json:"chatId"`
	MessageID      *int64          `json:"messageId,omitempty"`
	SenderID       *int64          `json:"senderId,omitempty"`
	SenderLogin    string          `json:"senderLogin"`
	RecipientID    *int64          `json:"recipientId,omitempty"`
	FileName       string          `json:"fileName"`
	FileHash       string          `json:"fileHash"`
	Reason         string          `json:"reason"`
	Report         json.RawMessage `json:"report"`
	AutoBan        bool            `json:"autoBan"`
	ReviewDeadline time.Time       `json:"reviewDeadline"`
	AutoBanAt      *time.Time      `json:"autoBanAt,omitempty"`
	AcknowledgedAt *time.Time      `json:"acknowledgedAt,omitempty"`
	AcknowledgedBy string          `json:"acknowledgedBy,omitempty"`
	ReviewedAt     *time.Time      `json:"reviewedAt,omitempty"`
	ReviewedBy     string          `json:"reviewedBy,omitempty"`
	Action         string          `json:"action,omitempty"`
	BanApplied     bool            `json:"banApplied"`
}

type CreateInput struct {
	Kind        string          `json:"kind"`
	Verdict     string          `json:"verdict"`
	Severity    string          `json:"severity"`
	ChatType    string          `json:"chatType"`
	ChatID      string          `json:"chatId"`
	MessageID   int64           `json:"messageId"`
	SenderID    int64           `json:"senderId"`
	SenderLogin string          `json:"senderLogin"`
	RecipientID int64           `json:"recipientId"`
	FileName    string          `json:"fileName"`
	FileHash    string          `json:"fileHash"`
	Reason      string          `json:"reason"`
	Report      json.RawMessage `json:"report"`
	AutoBan     *bool           `json:"autoBan"`
	ReviewAfter time.Duration   `json:"-"`
}

type Alert struct {
	ID             int64      `json:"id"`
	IncidentID     int64      `json:"incidentId"`
	RecipientLogin string     `json:"recipientLogin"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"createdAt"`
	DeliveredAt    *time.Time `json:"deliveredAt,omitempty"`
	ReadAt         *time.Time `json:"readAt,omitempty"`
}

func New(db *sql.DB, accessService *access.Service) *Service {
	return &Service{db: db, access: accessService, scanner: NewScanner(db), links: NewLinkChecker()}
}

func (s *Service) FileScanner() *Scanner {
	return s.scanner
}

func RegisterRoutes(srv Deps, service *Service) {
	service.RegisterRoutes(srv)
}

func (s *Service) SetNotifier(notifier func(context.Context, Incident) error) {
	s.notifier = notifier
}

func TelegramText(incident Incident) string {
	return fmt.Sprintf(
		"Инцидент безопасности #%d\nТип: %s\nВердикт: %s\nОтправитель: %s\nЧат: %s %s\nФайл: %s\nSHA-256: %s\nПричина: %s\nДедлайн: %s",
		incident.ID,
		incident.Kind,
		incident.Verdict,
		incident.SenderLogin,
		incident.ChatType,
		incident.ChatID,
		incident.FileName,
		incident.FileHash,
		incident.Reason,
		incident.ReviewDeadline.Format(time.RFC3339),
	)
}

func severityForVerdict(verdict string) string {
	switch strings.ToLower(strings.TrimSpace(verdict)) {
	case "infected", "malware", "tampered", "fake", "fraud", "dangerous_code":
		return "critical"
	case "suspicious", "blocked":
		return "warning"
	default:
		return "info"
	}
}

func shouldAutoBan(verdict string) bool {
	return severityForVerdict(verdict) == "critical"
}

func validKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "file", "code", "mod", "modpack", "app", "update", "archive", "link":
		return true
	default:
		return false
	}
}

func validResolveAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionConfirmFraud, ActionFalsePositive, ActionClose:
		return true
	default:
		return false
	}
}

func normalizeInput(input *CreateInput) error {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Verdict = strings.ToLower(strings.TrimSpace(input.Verdict))
	input.Severity = strings.ToLower(strings.TrimSpace(input.Severity))

	if !validKind(input.Kind) {
		return fmt.Errorf("invalid incident kind")
	}
	if input.Verdict == "" {
		return fmt.Errorf("verdict is required")
	}
	if input.Severity == "" {
		input.Severity = severityForVerdict(input.Verdict)
	}
	if input.ReviewAfter <= 0 {
		input.ReviewAfter = 24 * time.Hour
	}
	if len(input.Report) > 0 && !json.Valid(input.Report) {
		return fmt.Errorf("invalid report json")
	}
	if len(input.Report) == 0 {
		input.Report = json.RawMessage(`{}`)
	}

	return nil
}

func (s *Service) CreateIncident(ctx context.Context, input CreateInput, actor string) (Incident, error) {
	if err := normalizeInput(&input); err != nil {
		return Incident{}, err
	}
	if s.db == nil {
		return Incident{}, fmt.Errorf("database unavailable")
	}

	autoBan := shouldAutoBan(input.Verdict)
	if input.AutoBan != nil {
		autoBan = *input.AutoBan
	}

	now := time.Now()
	deadline := now.Add(input.ReviewAfter)
	var autoBanAt any
	if autoBan {
		autoBanAt = deadline
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Incident{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	incident, err := scanIncident(tx.QueryRowContext(ctx, `
		INSERT INTO security_incidents (
			kind, verdict, severity, status, chat_type, chat_id, message_id,
			sender_id, sender_login, recipient_id, file_name, file_hash, reason,
			report, auto_ban, review_deadline, auto_ban_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id, created_at, updated_at, kind, verdict, severity, status,
			chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
			file_name, file_hash, reason, report, auto_ban, review_deadline,
			auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
			action, ban_applied`,
		input.Kind, input.Verdict, input.Severity, StatusPendingReview,
		input.ChatType, input.ChatID, nullableID(input.MessageID),
		nullableID(input.SenderID), input.SenderLogin, nullableID(input.RecipientID),
		input.FileName, input.FileHash, input.Reason, string(input.Report),
		autoBan, deadline, autoBanAt,
	))
	if err != nil {
		return Incident{}, err
	}

	if actor == "" {
		actor = "system"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_incident_events (incident_id, actor, action) VALUES ($1,$2,'created')`, incident.ID, actor); err != nil {
		return Incident{}, err
	}

	for _, login := range s.adminLogins() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO security_alerts (incident_id, recipient_login) VALUES ($1,$2)`, incident.ID, login); err != nil {
			return Incident{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Incident{}, err
	}

	if s.notifier != nil {
		go func() {
			notifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.notifier(notifyCtx, incident); err != nil {
				log.Printf("[security] incident %d notification failed: %v", incident.ID, err)
			}
		}()
	}

	return incident, nil
}

func (s *Service) ListIncidents(ctx context.Context, status string, limit int) ([]Incident, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := `SELECT id, created_at, updated_at, kind, verdict, severity, status,
		chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
		file_name, file_hash, reason, report, auto_ban, review_deadline,
		auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
		action, ban_applied
		FROM security_incidents`
	args := make([]any, 0, 2)
	if status = strings.TrimSpace(status); status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]Incident, 0)
	for rows.Next() {
		incident, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, incident)
	}

	return result, rows.Err()
}

func (s *Service) GetIncident(ctx context.Context, id int64) (Incident, error) {
	if s.db == nil {
		return Incident{}, fmt.Errorf("database unavailable")
	}

	item, err := scanIncident(s.db.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at, kind, verdict, severity, status,
			chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
			file_name, file_hash, reason, report, auto_ban, review_deadline,
			auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
			action, ban_applied
		FROM security_incidents WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Incident{}, fmt.Errorf("incident not found")
		}
		return Incident{}, err
	}

	return item, nil
}

func (s *Service) ListAlerts(ctx context.Context, login string, limit int) ([]Alert, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, incident_id, recipient_login, channel, status, created_at, delivered_at, read_at
		FROM security_alerts
		WHERE recipient_login = $1 AND status <> 'resolved'
		ORDER BY created_at DESC
		LIMIT $2`, strings.ToLower(strings.TrimSpace(login)), limit)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	result := make([]Alert, 0)
	for rows.Next() {
		var item Alert
		var deliveredAt, readAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.RecipientLogin, &item.Channel, &item.Status, &item.CreatedAt, &deliveredAt, &readAt); err != nil {
			return nil, err
		}
		if deliveredAt.Valid {
			item.DeliveredAt = &deliveredAt.Time
		}
		if readAt.Valid {
			item.ReadAt = &readAt.Time
		}
		result = append(result, item)
	}

	return result, rows.Err()
}

func (s *Service) Acknowledge(ctx context.Context, id int64, actor string) (Incident, error) {
	if s.db == nil {
		return Incident{}, fmt.Errorf("database unavailable")
	}
	if actor == "" {
		return Incident{}, fmt.Errorf("actor is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Incident{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	incident, err := scanIncident(tx.QueryRowContext(ctx, `
		UPDATE security_incidents
		SET status = $2,
			acknowledged_at = now(),
			acknowledged_by = $3,
			updated_at = now(),
			auto_ban_at = CASE WHEN auto_ban THEN now() + interval '24 hours' ELSE NULL END
		WHERE id = $1 AND status IN ($4,$5)
		RETURNING id, created_at, updated_at, kind, verdict, severity, status,
			chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
			file_name, file_hash, reason, report, auto_ban, review_deadline,
			auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
			action, ban_applied`,
		id, StatusAcknowledged, actor, StatusPendingReview, StatusAcknowledged,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Incident{}, fmt.Errorf("incident not found or already resolved")
		}
		return Incident{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO security_incident_events (incident_id, actor, action) VALUES ($1,$2,'acknowledged')`, id, actor); err != nil {
		return Incident{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_alerts SET status = 'resolved', read_at = now() WHERE incident_id = $1 AND status <> 'resolved'`, id); err != nil {
		return Incident{}, err
	}

	if err := tx.Commit(); err != nil {
		return Incident{}, err
	}

	return incident, nil
}

func (s *Service) Resolve(ctx context.Context, id int64, actor, action, note string) (Incident, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if !validResolveAction(action) {
		return Incident{}, fmt.Errorf("invalid action")
	}
	if s.db == nil {
		return Incident{}, fmt.Errorf("database unavailable")
	}
	if actor == "" {
		return Incident{}, fmt.Errorf("actor is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Incident{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	current, err := scanIncident(tx.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at, kind, verdict, severity, status,
			chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
			file_name, file_hash, reason, report, auto_ban, review_deadline,
			auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
			action, ban_applied
		FROM security_incidents WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Incident{}, fmt.Errorf("incident not found")
		}
		return Incident{}, err
	}
	if current.Status == StatusFalsePositive || current.Status == StatusClosed {
		return Incident{}, fmt.Errorf("incident already resolved")
	}
	if current.Status == StatusConfirmed && action == ActionConfirmFraud {
		return Incident{}, fmt.Errorf("incident already confirmed")
	}

	status := StatusClosed
	banApplied := current.BanApplied
	if action == ActionConfirmFraud {
		status = StatusConfirmed
		banApplied = current.SenderID != nil
	}
	if action == ActionFalsePositive {
		status = StatusFalsePositive
		banApplied = false
	}

	if action == ActionConfirmFraud && current.SenderID != nil {
		if _, err := banUser(ctx, tx, *current.SenderID); err != nil {
			return Incident{}, err
		}
	}
	if action == ActionFalsePositive && current.BanApplied && current.SenderID != nil {
		if _, err := unbanUser(ctx, tx, *current.SenderID); err != nil {
			return Incident{}, err
		}
	}

	incident, err := scanIncident(tx.QueryRowContext(ctx, `
		UPDATE security_incidents
		SET status = $2, reviewed_at = now(), reviewed_by = $3, action = $4,
			updated_at = now(), auto_ban_at = NULL, ban_applied = $5
		WHERE id = $1
		RETURNING id, created_at, updated_at, kind, verdict, severity, status,
			chat_type, chat_id, message_id, sender_id, sender_login, recipient_id,
			file_name, file_hash, reason, report, auto_ban, review_deadline,
			auto_ban_at, acknowledged_at, acknowledged_by, reviewed_at, reviewed_by,
			action, ban_applied`,
		id, status, actor, action, banApplied,
	))
	if err != nil {
		return Incident{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO security_incident_events (incident_id, actor, action, note) VALUES ($1,$2,$3,$4)`, id, actor, action, note); err != nil {
		return Incident{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_alerts SET status = 'resolved', read_at = now() WHERE incident_id = $1 AND status <> 'resolved'`, id); err != nil {
		return Incident{}, err
	}

	if err := tx.Commit(); err != nil {
		return Incident{}, err
	}

	return incident, nil
}

func (s *Service) ProcessExpired(ctx context.Context) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("database unavailable")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM security_incidents
		WHERE status IN ($1,$2)
			AND auto_ban = true
			AND auto_ban_at IS NOT NULL
			AND auto_ban_at <= now()
			AND sender_id IS NOT NULL
		ORDER BY auto_ban_at
		LIMIT 100`, StatusPendingReview, StatusAcknowledged)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = rows.Close()
	}()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, id := range ids {
		if err := s.autoBan(ctx, id); err != nil {
			log.Printf("[security] auto-ban incident %d failed: %v", id, err)
			continue
		}
		count++
	}

	return count, nil
}

func (s *Service) RunSLA(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := s.ProcessExpired(runCtx); err != nil {
				log.Printf("[security] SLA sweep failed: %v", err)
			}
			if _, err := s.CleanupQuarantine(runCtx); err != nil {
				log.Printf("[security] quarantine cleanup failed: %v", err)
			}
			cancel()
		}
	}
}

func (s *Service) autoBan(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var senderID sql.NullInt64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT sender_id, status FROM security_incidents WHERE id = $1 FOR UPDATE`, id).Scan(&senderID, &status); err != nil {
		return err
	}
	if !senderID.Valid || (status != StatusPendingReview && status != StatusAcknowledged) {
		return nil
	}

	if _, err := banUser(ctx, tx, senderID.Int64); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_incidents
		SET status = $2, auto_ban_at = NULL, updated_at = now(), ban_applied = true, action = 'auto_ban'
		WHERE id = $1`, id, StatusAutoBanned); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_incident_events (incident_id, actor, action, note) VALUES ($1,'system','auto_ban',$2)`, id, BanReason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_alerts SET status = 'resolved', read_at = now() WHERE incident_id = $1 AND status <> 'resolved'`, id); err != nil {
		return err
	}

	return tx.Commit()
}

func banUser(ctx context.Context, tx *sql.Tx, userID int64) (sql.Result, error) {
	return tx.ExecContext(ctx, `UPDATE users SET banned = true, ban_reason = $2, banned_at = now() WHERE id = $1`, userID, BanReason)
}

func unbanUser(ctx context.Context, tx *sql.Tx, userID int64) (sql.Result, error) {
	return tx.ExecContext(ctx, `UPDATE users SET banned = false, ban_reason = '', banned_at = NULL WHERE id = $1 AND ban_reason = $2`, userID, BanReason)
}

func (s *Service) adminLogins() []string {
	roles := s.access.Get()
	seen := make(map[string]bool)
	result := make([]string, 0, len(roles.Owners)+len(roles.Admins))

	for _, login := range append(roles.Owners, roles.Admins...) {
		login = strings.ToLower(strings.TrimSpace(login))
		if login == "" || seen[login] {
			continue
		}
		seen[login] = true
		result = append(result, login)
	}

	return result
}

func (s *Service) isAdmin(request *http.Request) (string, bool) {
	login := s.access.LoginFromRequest(request)
	return login, login != "" && (s.access.IsAdmin(login) || s.access.IsGroupAdmin(login))
}

func (s *Service) internalAuthorized(request *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("SECURITY_INTERNAL_TOKEN"))
	if expected == "" {
		return false
	}
	provided := strings.TrimSpace(request.Header.Get("X-Security-Token"))
	if provided == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/security/incidents", s.incidents)
	srv.ServeEndpoint("/rgg-sync/security/incidents/{id}", s.incident)
	srv.ServeEndpoint("/rgg-sync/security/incidents/{id}/ack", s.ackIncident)
	srv.ServeEndpoint("/rgg-sync/security/incidents/{id}/resolve", s.resolveIncident)
	srv.ServeEndpoint("/rgg-sync/security/alerts", s.alerts)
	srv.ServeEndpoint("/rgg-sync/security/scan", s.scanFile)
	srv.ServeEndpoint("/rgg-sync/security/links/check", s.checkLink)
	srv.ServeEndpoint("/rgg-sync/security/hashes", s.hashRulesRoute)
	srv.ServeEndpoint("/rgg-sync/security/hashes/bulk", s.hashRulesBulkRoute)
	srv.ServeEndpoint("/rgg-sync/security/hashes/{id}", s.hashRuleRoute)
}

func (s *Service) incidents(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		if _, ok := s.isAdmin(request); !ok {
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
		items, err := s.ListIncidents(request.Context(), request.URL.Query().Get("status"), limit)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load incidents"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"incidents": items})
		return
	}

	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	actor := "scanner"
	if !s.internalAuthorized(request) {
		login, ok := s.isAdmin(request)
		if !ok {
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		actor = login
	}

	var input CreateInput
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 256<<10)).Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	incident, err := s.CreateIncident(request.Context(), input, actor)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusCreated, incident)
}

func (s *Service) incident(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.isAdmin(request); !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
	item, err := s.GetIncident(request.Context(), id)
	if err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}
	writeJSON(response, http.StatusOK, item)
}

func (s *Service) ackIncident(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	login, ok := s.isAdmin(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
	incident, err := s.Acknowledge(request.Context(), id, login)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, incident)
}

func (s *Service) resolveIncident(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	login, ok := s.isAdmin(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	var body struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 32<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
	incident, err := s.Resolve(request.Context(), id, login, body.Action, body.Note)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, incident)
}

func (s *Service) scanFile(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.internalAuthorized(request) {
		if _, ok := s.isAdmin(request); !ok {
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
	}
	scanner := s.scanner
	if scanner == nil || !scanner.Enabled() {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "security scanner is not configured"})
		return
	}

	request.Body = http.MaxBytesReader(response, request.Body, 100<<20)
	if err := request.ParseMultipartForm(100 << 20); err != nil {
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

	data, err := io.ReadAll(io.LimitReader(file, 100<<20))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "failed to read file"})
		return
	}
	report, err := scanner.Scan(request.Context(), data, ScanMeta{
		FileName: header.Filename,
		Mime:     header.Header.Get("Content-Type"),
	})
	if errors.Is(err, ErrScanUnavailable) {
		writeJSON(response, http.StatusServiceUnavailable, report)
		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, report)
}

func (s *Service) checkLink(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.access.LoginFromRequest(request) == "" {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "authorization required"})
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	report, err := s.links.Check(request.Context(), body.URL)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to check link"})
		return
	}
	writeJSON(response, http.StatusOK, report)
}

func (s *Service) alerts(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	login, ok := s.isAdmin(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	items, err := s.ListAlerts(request.Context(), login, 100)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load alerts"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"alerts": items})
}

func nullableID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func scanIncident(scanner interface{ Scan(...any) error }) (Incident, error) {
	var item Incident
	var messageID, senderID, recipientID sql.NullInt64
	var report []byte
	var autoBanAt, acknowledgedAt, reviewedAt sql.NullTime

	err := scanner.Scan(
		&item.ID, &item.CreatedAt, &item.UpdatedAt, &item.Kind, &item.Verdict,
		&item.Severity, &item.Status, &item.ChatType, &item.ChatID, &messageID,
		&senderID, &item.SenderLogin, &recipientID, &item.FileName, &item.FileHash,
		&item.Reason, &report, &item.AutoBan, &item.ReviewDeadline, &autoBanAt,
		&acknowledgedAt, &item.AcknowledgedBy, &reviewedAt, &item.ReviewedBy,
		&item.Action, &item.BanApplied,
	)
	if err != nil {
		return Incident{}, err
	}

	if messageID.Valid {
		item.MessageID = &messageID.Int64
	}
	if senderID.Valid {
		item.SenderID = &senderID.Int64
	}
	if recipientID.Valid {
		item.RecipientID = &recipientID.Int64
	}
	if autoBanAt.Valid {
		item.AutoBanAt = &autoBanAt.Time
	}
	if acknowledgedAt.Valid {
		item.AcknowledgedAt = &acknowledgedAt.Time
	}
	if reviewedAt.Valid {
		item.ReviewedAt = &reviewedAt.Time
	}
	if len(report) == 0 {
		report = []byte(`{}`)
	}
	item.Report = json.RawMessage(report)

	return item, nil
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
