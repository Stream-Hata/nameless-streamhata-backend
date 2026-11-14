package marketplace

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/pluginmanifest"
)

const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusRejected  = "rejected"
	StatusBlocked   = "blocked"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Plugin struct {
	ID             int64           `json:"id"`
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Summary        string          `json:"summary"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	LatestVersion  string          `json:"latestVersion"`
	PublisherSlug  string          `json:"publisherSlug"`
	Permissions    []string        `json:"permissions"`
	ArtifactURL    string          `json:"artifactUrl"`
	ArtifactSHA256 string          `json:"artifactSha256"`
	SignatureType  string          `json:"signatureType"`
	Signature      string          `json:"signature"`
	Manifest       json.RawMessage `json:"manifest,omitempty"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	PublishedAt    *time.Time      `json:"publishedAt,omitempty"`
	DownloadCount  int64           `json:"downloadCount"`
}

type PluginVersion struct {
	ID             int64           `json:"id"`
	PluginID       int64           `json:"pluginId"`
	Version        string          `json:"version"`
	Manifest       json.RawMessage `json:"manifest"`
	ArtifactURL    string          `json:"artifactUrl"`
	ArtifactSHA256 string          `json:"artifactSha256"`
	ArtifactSize   int64           `json:"artifactSize"`
	SignatureType  string          `json:"signatureType"`
	Signature      string          `json:"signature"`
	Status         string          `json:"status"`
	CreatedBy      string          `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	PublishedAt    *time.Time      `json:"publishedAt,omitempty"`
}

type Service struct {
	db     *sql.DB
	access *access.Service
}

func New(db *sql.DB, accessService *access.Service) *Service {
	return &Service{db: db, access: accessService}
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/plugins", service.pluginRoute)
	srv.ServeEndpoint("/rgg-sync/plugins/admin", service.adminListRoute)
	srv.ServeEndpoint("/rgg-sync/plugins/admin/{slug}/status", service.statusRoute)
	srv.ServeEndpoint("/rgg-sync/plugins/{slug}", service.detailRoute)
}

func (s *Service) ListPublished(ctx context.Context, query string, limit int) ([]Plugin, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, name, summary, kind, status, latest_version, publisher_slug,
			permissions, artifact_url, artifact_sha256, signature_type, signature,
			created_by, created_at, updated_at, published_at
		FROM plugin_registry
		WHERE status = $1 AND ($2 = '' OR name ILIKE '%' || $2 || '%' OR slug ILIKE '%' || $2 || '%')
		ORDER BY published_at DESC NULLS LAST, updated_at DESC
		LIMIT $3`, StatusPublished, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanPlugins(rows)
}

func (s *Service) GetPublished(ctx context.Context, slug string) (Plugin, []PluginVersion, error) {
	var plugin Plugin
	var permissions []byte
	var published sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, slug, name, summary, kind, status, latest_version, publisher_slug,
			permissions, artifact_url, artifact_sha256, signature_type, signature,
			created_by, created_at, updated_at, published_at
		FROM plugin_registry WHERE slug = $1 AND status = $2`,
		strings.TrimSpace(slug), StatusPublished).
		Scan(&plugin.ID, &plugin.Slug, &plugin.Name, &plugin.Summary, &plugin.Kind,
			&plugin.Status, &plugin.LatestVersion, &plugin.PublisherSlug, &permissions,
			&plugin.ArtifactURL, &plugin.ArtifactSHA256, &plugin.SignatureType,
			&plugin.Signature, &plugin.CreatedBy, &plugin.CreatedAt, &plugin.UpdatedAt, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, nil, sql.ErrNoRows
	}
	if err != nil {
		return Plugin{}, nil, err
	}
	_ = json.Unmarshal(permissions, &plugin.Permissions)
	if published.Valid {
		plugin.PublishedAt = &published.Time
	}
	versions, err := s.versions(ctx, plugin.ID, true)
	if err != nil {
		return Plugin{}, nil, err
	}

	return plugin, versions, nil
}

func (s *Service) ListAdmin(ctx context.Context) ([]Plugin, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, slug, name, summary, kind, status, latest_version, publisher_slug,
			permissions, artifact_url, artifact_sha256, signature_type, signature,
			created_by, created_at, updated_at, published_at
		FROM plugin_registry
		ORDER BY updated_at DESC, id DESC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanPlugins(rows)
}

func (s *Service) Submit(ctx context.Context, raw json.RawMessage, actor string) (Plugin, error) {
	manifest, err := pluginmanifest.Parse(raw)
	if err != nil {
		return Plugin{}, err
	}
	if manifest.Artifact.URL == "" || manifest.Artifact.SHA256 == "" {
		return Plugin{}, fmt.Errorf("artifact.url and artifact.sha256 are required for marketplace submission")
	}
	permissions, _ := json.Marshal(manifest.Permissions)
	publisher := manifest.Publisher.ID
	if publisher == "" {
		publisher = "streamhata"
	}
	summary := manifest.Description
	if len([]rune(summary)) > 280 {
		summary = string([]rune(summary)[:280])
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Plugin{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var pluginID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO plugin_registry (
			slug, name, summary, kind, status, manifest, publisher_slug, permissions,
			artifact_url, artifact_sha256, signature_type, signature, created_by, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now())
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			summary = EXCLUDED.summary,
			kind = EXCLUDED.kind,
			manifest = EXCLUDED.manifest,
			publisher_slug = EXCLUDED.publisher_slug,
			permissions = EXCLUDED.permissions,
			artifact_url = EXCLUDED.artifact_url,
			artifact_sha256 = EXCLUDED.artifact_sha256,
			signature_type = EXCLUDED.signature_type,
			signature = EXCLUDED.signature,
			updated_at = now()
		RETURNING id`,
		manifest.ID, manifest.Name, summary, manifest.Kind, StatusPending, raw, publisher,
		permissions, manifest.Artifact.URL, strings.ToLower(manifest.Artifact.SHA256),
		manifest.Artifact.SignatureType, manifest.Artifact.Signature, actor,
	).Scan(&pluginID)
	if err != nil {
		return Plugin{}, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO plugin_versions (
			plugin_id, version, manifest, artifact_url, artifact_sha256, artifact_size,
			signature_type, signature, status, created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (plugin_id, version) DO NOTHING`,
		pluginID, manifest.Version, raw, manifest.Artifact.URL,
		strings.ToLower(manifest.Artifact.SHA256), manifest.Artifact.Size,
		manifest.Artifact.SignatureType, manifest.Artifact.Signature, StatusPending, actor)
	if err != nil {
		return Plugin{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Plugin{}, fmt.Errorf("version %s already submitted", manifest.Version)
	}
	if err := tx.Commit(); err != nil {
		return Plugin{}, err
	}

	return s.pluginBySlug(ctx, manifest.ID)
}

func (s *Service) SetStatus(ctx context.Context, slug, version, status, actor string) (Plugin, error) {
	if status != StatusPublished && status != StatusRejected && status != StatusBlocked {
		return Plugin{}, fmt.Errorf("status must be published, rejected or blocked")
	}
	var pluginID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM plugin_registry WHERE slug = $1`, slug).Scan(&pluginID); err != nil {
		return Plugin{}, err
	}
	if version == "" {
		err := s.db.QueryRowContext(ctx, `
			SELECT version FROM plugin_versions WHERE plugin_id = $1 AND status = $2
			ORDER BY created_at DESC LIMIT 1`, pluginID, StatusPending).Scan(&version)
		if err != nil {
			return Plugin{}, fmt.Errorf("pending version not found")
		}
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE plugin_versions SET status = $2, published_at = CASE WHEN $2 = 'published' THEN now() ELSE published_at END
		WHERE plugin_id = $1 AND version = $3`, pluginID, status, version)
	if err != nil {
		return Plugin{}, err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return Plugin{}, sql.ErrNoRows
	}
	if status == StatusPublished {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE plugin_registry SET status = $2, latest_version = $3, published_at = now(), updated_at = now()
			WHERE id = $1`, pluginID, StatusPublished, version); err != nil {
			return Plugin{}, err
		}
	} else {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE plugin_registry SET status = CASE
				WHEN EXISTS (SELECT 1 FROM plugin_versions WHERE plugin_id = $1 AND status = 'published') THEN status
				ELSE $2 END, updated_at = now()
			WHERE id = $1`, pluginID, status); err != nil {
			return Plugin{}, err
		}
	}
	_ = actor

	return s.pluginBySlug(ctx, slug)
}

func (s *Service) pluginBySlug(ctx context.Context, slug string) (Plugin, error) {
	var plugin Plugin
	var permissions []byte
	var published sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, slug, name, summary, kind, status, latest_version, publisher_slug,
			permissions, artifact_url, artifact_sha256, signature_type, signature,
			created_by, created_at, updated_at, published_at
		FROM plugin_registry WHERE slug = $1`, slug).
		Scan(&plugin.ID, &plugin.Slug, &plugin.Name, &plugin.Summary, &plugin.Kind,
			&plugin.Status, &plugin.LatestVersion, &plugin.PublisherSlug, &permissions,
			&plugin.ArtifactURL, &plugin.ArtifactSHA256, &plugin.SignatureType,
			&plugin.Signature, &plugin.CreatedBy, &plugin.CreatedAt, &plugin.UpdatedAt, &published)
	if err != nil {
		return Plugin{}, err
	}
	_ = json.Unmarshal(permissions, &plugin.Permissions)
	if published.Valid {
		plugin.PublishedAt = &published.Time
	}

	return plugin, nil
}

func (s *Service) versions(ctx context.Context, pluginID int64, publishedOnly bool) ([]PluginVersion, error) {
	query := `SELECT id, plugin_id, version, manifest, artifact_url, artifact_sha256, artifact_size,
		signature_type, signature, status, created_by, created_at, published_at
		FROM plugin_versions WHERE plugin_id = $1`
	if publishedOnly {
		query += ` AND status = 'published'`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PluginVersion, 0)
	for rows.Next() {
		var item PluginVersion
		var manifest []byte
		var published sql.NullTime
		if err := rows.Scan(&item.ID, &item.PluginID, &item.Version, &manifest,
			&item.ArtifactURL, &item.ArtifactSHA256, &item.ArtifactSize,
			&item.SignatureType, &item.Signature, &item.Status, &item.CreatedBy,
			&item.CreatedAt, &published); err != nil {
			return nil, err
		}
		item.Manifest = manifest
		if published.Valid {
			item.PublishedAt = &published.Time
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func scanPlugins(rows *sql.Rows) ([]Plugin, error) {
	items := make([]Plugin, 0)
	for rows.Next() {
		var item Plugin
		var permissions []byte
		var published sql.NullTime
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Summary, &item.Kind,
			&item.Status, &item.LatestVersion, &item.PublisherSlug, &permissions,
			&item.ArtifactURL, &item.ArtifactSHA256, &item.SignatureType, &item.Signature,
			&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt, &published); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(permissions, &item.Permissions)
		if published.Valid {
			item.PublishedAt = &published.Time
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *Service) authorized(request *http.Request) (string, bool) {
	expected := strings.TrimSpace(os.Getenv("SECURITY_INTERNAL_TOKEN"))
	provided := strings.TrimSpace(request.Header.Get("X-Security-Token"))
	if expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1 {
		return "internal", true
	}
	if s.access == nil {
		return "", false
	}
	login := s.access.LoginFromRequest(request)
	if login == "" || !s.access.IsAdmin(login) {
		return "", false
	}

	return login, true
}

func (s *Service) pluginRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		s.submitRoute(response, request)

		return
	}
	s.listRoute(response, request)
}

func (s *Service) submitRoute(response http.ResponseWriter, request *http.Request) {
	actor, ok := s.authorized(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return
	}
	var body struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, pluginmanifest.MaxSize+8<<10)).Decode(&body); err != nil || len(body.Manifest) == 0 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid manifest"})

		return
	}
	plugin, err := s.Submit(request.Context(), body.Manifest, actor)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}
	writeJSON(response, http.StatusCreated, plugin)
}

func (s *Service) listRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	items, err := s.ListPublished(request.Context(), request.URL.Query().Get("q"), limit)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load plugins"})

		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) detailRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	plugin, versions, err := s.GetPublished(request.Context(), request.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "plugin not found"})

		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load plugin"})

		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"plugin": plugin, "versions": versions})
}

func (s *Service) adminListRoute(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.authorized(request); !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return
	}
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	items, err := s.ListAdmin(request.Context())
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load plugins"})

		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"items": items})
}

func (s *Service) statusRoute(response http.ResponseWriter, request *http.Request) {
	actor, ok := s.authorized(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	var body struct {
		Version string `json:"version"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

		return
	}
	plugin, err := s.SetStatus(request.Context(), request.PathValue("slug"), body.Version, body.Status, actor)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

		return
	}
	writeJSON(response, http.StatusOK, plugin)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
