package trust

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
)

const (
	VerdictVerified   = "verified"
	VerdictTampered   = "tampered"
	VerdictUnknown    = "unknown"
	VerdictSuspicious = "suspicious"
)

var ErrNotFound = errors.New("release not found")

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Release struct {
	ID              int64     `json:"id"`
	ProductSlug     string    `json:"productSlug"`
	Platform        string    `json:"platform"`
	Channel         string    `json:"channel"`
	Version         string    `json:"version"`
	VersionCode     int64     `json:"versionCode"`
	PackageName     string    `json:"packageName"`
	SHA256          string    `json:"sha256"`
	SignatureSHA256 string    `json:"signatureSha256"`
	SignatureType   string    `json:"signatureType"`
	FileSize        int64     `json:"fileSize"`
	URL             string    `json:"url"`
	PublishedAt     time.Time `json:"publishedAt"`
}

type VerifyInput struct {
	ProductSlug     string `json:"productSlug"`
	Platform        string `json:"platform"`
	Channel         string `json:"channel"`
	Version         string `json:"version"`
	VersionCode     int64  `json:"versionCode"`
	PackageName     string `json:"packageName"`
	SHA256          string `json:"sha256"`
	SignatureSHA256 string `json:"signatureSha256"`
}

type Verification struct {
	Verdict         string   `json:"verdict"`
	Reason          string   `json:"reason"`
	ProductSlug     string   `json:"productSlug"`
	Platform        string   `json:"platform"`
	Channel         string   `json:"channel"`
	ActualSHA256    string   `json:"actualSha256"`
	ExpectedSHA256  string   `json:"expectedSha256,omitempty"`
	ActualVersion   string   `json:"actualVersion,omitempty"`
	ExpectedVersion string   `json:"expectedVersion,omitempty"`
	Release         *Release `json:"release,omitempty"`
}

type Service struct {
	db     *sql.DB
	access *access.Service
}

func New(db *sql.DB, accessService *access.Service) *Service {
	return &Service{db: db, access: accessService}
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/trust/releases/latest", service.latestRoute)
	srv.ServeEndpoint("/rgg-sync/trust/releases", service.releaseRoute)
	srv.ServeEndpoint("/rgg-sync/trust/verify", service.verifyRoute)
	srv.ServeEndpoint("/rgg-sync/trust/admin", service.adminDataRoute)
	srv.ServeEndpoint("/rgg-sync/trust/admin/publishers", service.publisherRoute)
	srv.ServeEndpoint("/rgg-sync/trust/admin/products", service.productRoute)
	srv.ServeEndpoint("/rgg-sync/trust/admin/sources", service.sourceRoute)
	srv.ServeEndpoint("/rgg-sync/trust/admin/releases/{id}", service.releaseAdminRoute)
}

func (s *Service) Latest(ctx context.Context, productSlug, platform, channel string) (Release, error) {
	if s.db == nil {
		return Release{}, fmt.Errorf("database unavailable")
	}
	productSlug = strings.TrimSpace(productSlug)
	platform = strings.ToLower(strings.TrimSpace(platform))
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		channel = "stable"
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT r.id, p.slug, p.platform, r.channel, r.version, r.version_code,
			r.package_name, r.sha256, r.signature_sha256, r.signature_type,
			r.file_size, r.url, r.published_at
		FROM trust_releases r
		JOIN trust_products p ON p.id = r.product_id
		WHERE p.slug = $1 AND p.platform = $2 AND r.channel = $3
		ORDER BY r.published_at DESC
		LIMIT 1`, productSlug, platform, channel)

	var release Release
	err := row.Scan(
		&release.ID, &release.ProductSlug, &release.Platform, &release.Channel,
		&release.Version, &release.VersionCode, &release.PackageName, &release.SHA256,
		&release.SignatureSHA256, &release.SignatureType, &release.FileSize,
		&release.URL, &release.PublishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, err
	}

	return release, nil
}

func normalizeRelease(release Release) (Release, error) {
	release.ProductSlug = strings.TrimSpace(release.ProductSlug)
	release.Platform = strings.ToLower(strings.TrimSpace(release.Platform))
	release.Channel = strings.ToLower(strings.TrimSpace(release.Channel))
	release.Version = strings.TrimSpace(release.Version)
	release.SHA256 = strings.ToLower(strings.TrimSpace(release.SHA256))
	release.SignatureSHA256 = strings.ToLower(strings.TrimSpace(release.SignatureSHA256))
	if release.Channel == "" {
		release.Channel = "stable"
	}
	if release.ProductSlug == "" || release.Platform == "" || release.Version == "" || len(release.SHA256) != 64 {
		return Release{}, fmt.Errorf("productSlug, platform, version and sha256 are required")
	}
	if release.PublishedAt.IsZero() {
		release.PublishedAt = time.Now()
	}

	return release, nil
}

func (s *Service) RegisterRelease(ctx context.Context, release Release) (Release, error) {
	if s.db == nil {
		return Release{}, fmt.Errorf("database unavailable")
	}
	var err error
	release, err = normalizeRelease(release)
	if err != nil {
		return Release{}, err
	}

	var productID int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM trust_products WHERE slug = $1 AND platform = $2`,
		release.ProductSlug, release.Platform,
	).Scan(&productID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Release{}, fmt.Errorf("unknown product")
		}
		return Release{}, err
	}

	err = s.db.QueryRowContext(ctx, `
		INSERT INTO trust_releases (
			product_id, channel, version, version_code, package_name, sha256,
			signature_sha256, signature_type, file_size, url, published_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (product_id, channel, version, sha256) DO UPDATE SET
			version_code = EXCLUDED.version_code,
			package_name = EXCLUDED.package_name,
			signature_sha256 = EXCLUDED.signature_sha256,
			signature_type = EXCLUDED.signature_type,
			file_size = EXCLUDED.file_size,
			url = EXCLUDED.url,
			published_at = EXCLUDED.published_at
		RETURNING id`,
		productID, release.Channel, release.Version, release.VersionCode,
		release.PackageName, release.SHA256, release.SignatureSHA256,
		release.SignatureType, release.FileSize, release.URL, release.PublishedAt,
	).Scan(&release.ID)
	if err != nil {
		return Release{}, err
	}

	return release, nil
}

func (s *Service) Verify(ctx context.Context, input VerifyInput) (Verification, error) {
	release, err := s.Latest(ctx, input.ProductSlug, input.Platform, input.Channel)
	if errors.Is(err, ErrNotFound) {
		return Verification{
			Verdict:      VerdictUnknown,
			Reason:       "официальный релиз не найден",
			ProductSlug:  input.ProductSlug,
			Platform:     input.Platform,
			ActualSHA256: strings.ToLower(strings.TrimSpace(input.SHA256)),
		}, nil
	}
	if err != nil {
		return Verification{}, err
	}

	return evaluate(release, input), nil
}

func evaluate(release Release, input VerifyInput) Verification {
	result := Verification{
		Verdict:         VerdictVerified,
		ProductSlug:     release.ProductSlug,
		Platform:        release.Platform,
		Channel:         release.Channel,
		ActualSHA256:    strings.ToLower(strings.TrimSpace(input.SHA256)),
		ExpectedSHA256:  release.SHA256,
		ActualVersion:   strings.TrimSpace(input.Version),
		ExpectedVersion: release.Version,
		Release:         &release,
	}

	if result.ActualSHA256 != release.SHA256 {
		result.Verdict = VerdictTampered
		result.Reason = "SHA-256 не совпадает с официальным релизом"
		return result
	}
	if input.Version != "" && input.Version != release.Version {
		result.Verdict = VerdictTampered
		result.Reason = "версия не совпадает с официальным релизом"
		return result
	}
	if input.VersionCode != 0 && release.VersionCode != 0 && input.VersionCode != release.VersionCode {
		result.Verdict = VerdictTampered
		result.Reason = "versionCode не совпадает с официальным релизом"
		return result
	}
	if input.PackageName != "" && release.PackageName != "" && input.PackageName != release.PackageName {
		result.Verdict = VerdictTampered
		result.Reason = "package name не совпадает с официальным релизом"
		return result
	}
	actualSignature := strings.ToLower(strings.TrimSpace(input.SignatureSHA256))
	if release.SignatureSHA256 != "" && actualSignature == "" {
		result.Verdict = VerdictSuspicious
		result.Reason = "подпись файла не передана для проверки"
		return result
	}
	if actualSignature != "" && release.SignatureSHA256 != "" && actualSignature != release.SignatureSHA256 {
		result.Verdict = VerdictTampered
		result.Reason = "подпись не совпадает с официальным релизом"
		return result
	}
	if release.SignatureSHA256 == "" {
		result.Verdict = VerdictSuspicious
		result.Reason = "официальный релиз найден, но подпись не зарегистрирована"
		return result
	}

	result.Reason = "официальный релиз подтверждён"
	return result
}

func (s *Service) latestRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := request.URL.Query()
	release, err := s.Latest(request.Context(), query.Get("product"), query.Get("platform"), query.Get("channel"))
	if errors.Is(err, ErrNotFound) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load release"})
		return
	}
	writeJSON(response, http.StatusOK, release)
}

func (s *Service) releaseRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		if !s.authorized(request) {
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		items, err := s.adminReleases(request.Context())
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load releases"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": items})
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var release Release
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&release); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	release, err := s.RegisterRelease(request.Context(), release)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusCreated, release)
}

func (s *Service) verifyRoute(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var input VerifyInput
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64<<10)).Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	result, err := s.Verify(request.Context(), input)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to verify artifact"})
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (s *Service) authorized(request *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("SECURITY_INTERNAL_TOKEN"))
	provided := strings.TrimSpace(request.Header.Get("X-Security-Token"))
	if expected != "" && provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1 {
		return true
	}
	if s.access == nil {
		return false
	}
	login := s.access.LoginFromRequest(request)
	return login != "" && s.access.IsAdmin(login)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func ParseVersionCode(value string) int64 {
	number, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return number
}
