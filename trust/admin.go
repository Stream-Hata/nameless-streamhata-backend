package trust

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Publisher struct {
	ID         int64     `json:"id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trustLevel"`
	Homepage   string    `json:"homepage"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Product struct {
	ID          int64     `json:"id"`
	PublisherID int64     `json:"publisherId"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Platform    string    `json:"platform"`
	PackageName string    `json:"packageName"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Source struct {
	ID         int64     `json:"id"`
	ProductID  int64     `json:"productId"`
	URL        string    `json:"url"`
	Kind       string    `json:"kind"`
	TrustLevel string    `json:"trustLevel"`
	CreatedAt  time.Time `json:"createdAt"`
}

type AdminRelease struct {
	Release
	ProductID int64 `json:"productId"`
}

type AdminData struct {
	Publishers []Publisher    `json:"publishers"`
	Products   []Product      `json:"products"`
	Sources    []Source       `json:"sources"`
	Releases   []AdminRelease `json:"releases"`
}

func (s *Service) AdminData(ctx context.Context) (AdminData, error) {
	if s.db == nil {
		return AdminData{}, fmt.Errorf("database unavailable")
	}
	publishers, err := s.publishers(ctx)
	if err != nil {
		return AdminData{}, err
	}
	products, err := s.products(ctx)
	if err != nil {
		return AdminData{}, err
	}
	sources, err := s.sources(ctx)
	if err != nil {
		return AdminData{}, err
	}
	releases, err := s.adminReleases(ctx)
	if err != nil {
		return AdminData{}, err
	}

	return AdminData{Publishers: publishers, Products: products, Sources: sources, Releases: releases}, nil
}

func (s *Service) publishers(ctx context.Context) ([]Publisher, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, name, trust_level, homepage, created_at FROM trust_publishers ORDER BY name, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Publisher, 0)
	for rows.Next() {
		var item Publisher
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.TrustLevel, &item.Homepage, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *Service) products(ctx context.Context) ([]Product, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, publisher_id, slug, name, platform, package_name, kind, created_at FROM trust_products ORDER BY name, platform`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Product, 0)
	for rows.Next() {
		var item Product
		if err := rows.Scan(&item.ID, &item.PublisherID, &item.Slug, &item.Name, &item.Platform, &item.PackageName, &item.Kind, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *Service) sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, product_id, url, kind, trust_level, created_at FROM trust_sources ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Source, 0)
	for rows.Next() {
		var item Source
		if err := rows.Scan(&item.ID, &item.ProductID, &item.URL, &item.Kind, &item.TrustLevel, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *Service) adminReleases(ctx context.Context) ([]AdminRelease, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, p.id, p.slug, p.platform, r.channel, r.version, r.version_code,
			r.package_name, r.sha256, r.signature_sha256, r.signature_type,
			r.file_size, r.url, r.published_at
		FROM trust_releases r
		JOIN trust_products p ON p.id = r.product_id
		ORDER BY r.published_at DESC, r.id DESC
		LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdminRelease, 0)
	for rows.Next() {
		var item AdminRelease
		if err := rows.Scan(
			&item.ID, &item.ProductID, &item.ProductSlug, &item.Platform, &item.Channel,
			&item.Version, &item.VersionCode, &item.PackageName, &item.SHA256,
			&item.SignatureSHA256, &item.SignatureType, &item.FileSize, &item.URL, &item.PublishedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

func (s *Service) SavePublisher(ctx context.Context, item Publisher) (Publisher, error) {
	item.Slug = strings.ToLower(strings.TrimSpace(item.Slug))
	item.Name = strings.TrimSpace(item.Name)
	item.TrustLevel = strings.ToLower(strings.TrimSpace(item.TrustLevel))
	item.Homepage = strings.TrimSpace(item.Homepage)
	if item.Slug == "" || item.Name == "" {
		return Publisher{}, fmt.Errorf("slug and name are required")
	}
	if item.TrustLevel == "" {
		item.TrustLevel = "unknown"
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO trust_publishers (slug, name, trust_level, homepage)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			trust_level = EXCLUDED.trust_level,
			homepage = EXCLUDED.homepage
		RETURNING id, created_at`,
		item.Slug, item.Name, item.TrustLevel, item.Homepage,
	).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return Publisher{}, err
	}

	return item, nil
}

func (s *Service) SaveProduct(ctx context.Context, item Product) (Product, error) {
	item.Slug = strings.ToLower(strings.TrimSpace(item.Slug))
	item.Name = strings.TrimSpace(item.Name)
	item.Platform = strings.ToLower(strings.TrimSpace(item.Platform))
	item.PackageName = strings.TrimSpace(item.PackageName)
	item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
	if item.PublisherID == 0 || item.Slug == "" || item.Name == "" || item.Platform == "" {
		return Product{}, fmt.Errorf("publisherId, slug, name and platform are required")
	}
	if item.Kind == "" {
		item.Kind = "app"
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (slug, platform) DO UPDATE SET
			publisher_id = EXCLUDED.publisher_id,
			name = EXCLUDED.name,
			package_name = EXCLUDED.package_name,
			kind = EXCLUDED.kind
		RETURNING id, created_at`,
		item.PublisherID, item.Slug, item.Name, item.Platform, item.PackageName, item.Kind,
	).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return Product{}, err
	}

	return item, nil
}

func (s *Service) SaveSource(ctx context.Context, item Source) (Source, error) {
	item.URL = strings.TrimSpace(item.URL)
	item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
	item.TrustLevel = strings.ToLower(strings.TrimSpace(item.TrustLevel))
	if item.ProductID == 0 || item.URL == "" {
		return Source{}, fmt.Errorf("productId and url are required")
	}
	if item.Kind == "" {
		item.Kind = "official"
	}
	if item.TrustLevel == "" {
		item.TrustLevel = "unknown"
	}

	err := s.db.QueryRowContext(ctx, `
		INSERT INTO trust_sources (product_id, url, kind, trust_level)
		VALUES ($1,$2,$3,$4)
		RETURNING id, created_at`,
		item.ProductID, item.URL, item.Kind, item.TrustLevel,
	).Scan(&item.ID, &item.CreatedAt)
	if err != nil {
		return Source{}, err
	}

	return item, nil
}

func (s *Service) DeletePublisher(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, `trust_publishers`, id)
}

func (s *Service) DeleteProduct(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, `trust_products`, id)
}

func (s *Service) DeleteSource(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, `trust_sources`, id)
}

func (s *Service) DeleteRelease(ctx context.Context, id int64) error {
	return s.deleteByID(ctx, `trust_releases`, id)
}

func (s *Service) deleteByID(ctx context.Context, table string, id int64) error {
	if s.db == nil {
		return fmt.Errorf("database unavailable")
	}
	if id <= 0 {
		return fmt.Errorf("invalid id")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = $1`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (s *Service) adminDataRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := s.AdminData(request.Context())
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load trust data"})
		return
	}
	writeJSON(response, http.StatusOK, data)
}

func (s *Service) publisherRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut:
		var item Publisher
		if err := decodeJSON(response, request, &item); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		saved, err := s.SavePublisher(request.Context(), item)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, saved)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(request.URL.Query().Get("id"), 10, 64)
		if err := s.DeletePublisher(request.Context(), id); err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "publisher not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) productRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut:
		var item Product
		if err := decodeJSON(response, request, &item); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		saved, err := s.SaveProduct(request.Context(), item)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, saved)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(request.URL.Query().Get("id"), 10, 64)
		if err := s.DeleteProduct(request.Context(), id); err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "product not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) sourceRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	switch request.Method {
	case http.MethodPost, http.MethodPut:
		var item Source
		if err := decodeJSON(response, request, &item); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		saved, err := s.SaveSource(request.Context(), item)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusOK, saved)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(request.URL.Query().Get("id"), 10, 64)
		if err := s.DeleteSource(request.Context(), id); err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "source not found"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) releaseAdminRoute(response http.ResponseWriter, request *http.Request) {
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if request.Method != http.MethodDelete {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err := s.DeleteRelease(request.Context(), id); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func decodeJSON(response http.ResponseWriter, request *http.Request, value any) error {
	return json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(value)
}
