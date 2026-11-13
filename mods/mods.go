package mods

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

const (
	VerdictVerified    = "mod_verified"
	VerdictUnknown     = "mod_unknown"
	VerdictTampered    = "mod_tampered"
	VerdictUnavailable = "mod_unavailable"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Result struct {
	Source        string    `json:"source"`
	Verdict       string    `json:"verdict"`
	Reason        string    `json:"reason"`
	ProjectID     string    `json:"projectId,omitempty"`
	ProjectSlug   string    `json:"projectSlug,omitempty"`
	Title         string    `json:"title,omitempty"`
	VersionID     string    `json:"versionId,omitempty"`
	VersionNumber string    `json:"versionNumber,omitempty"`
	FileName      string    `json:"fileName,omitempty"`
	FileURL       string    `json:"fileUrl,omitempty"`
	GameVersions  []string  `json:"gameVersions,omitempty"`
	Loaders       []string  `json:"loaders,omitempty"`
	CheckedAt     time.Time `json:"checkedAt"`
}

type Service struct {
	db         *sql.DB
	access     *access.Service
	client     *http.Client
	baseURL    string
	curseForge CurseForge
}

type modrinthVersion struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string `json:"loaders"`
	Files         []struct {
		Hashes struct {
			SHA512 string `json:"sha512"`
			SHA1   string `json:"sha1"`
		} `json:"hashes"`
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Primary  bool   `json:"primary"`
		Size     int64  `json:"size"`
	} `json:"files"`
}

type modrinthProject struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func New(db *sql.DB, accessService *access.Service) *Service {
	service := &Service{
		db:      db,
		access:  accessService,
		client:  netcfg.NewHTTPClient(12 * time.Second),
		baseURL: strings.TrimRight(envOr("MODRINTH_API_URL", "https://api.modrinth.com/v2"), "/"),
	}
	if endpoint := strings.TrimSpace(os.Getenv("CURSEFORGE_PROXY_URL")); endpoint != "" {
		service.curseForge = &HTTPCurseForge{URL: endpoint, Client: netcfg.NewHTTPClient(15 * time.Second)}
	}
	return service
}

func RegisterRoutes(srv Deps, service *Service) {
	srv.ServeEndpoint("/rgg-sync/mods/verify", service.verifyRoute)
}

func (s *Service) Verify(ctx context.Context, sha512 string, fingerprints []uint32) (Result, error) {
	sha512 = strings.ToLower(strings.TrimSpace(sha512))
	if len(sha512) != 128 {
		return Result{}, fmt.Errorf("sha512 must be 128 hex characters")
	}
	if cached, ok := s.cached(ctx, sha512); ok {
		return cached, nil
	}

	version, status, err := s.lookupVersion(ctx, sha512)
	if err != nil {
		return Result{}, err
	}
	if status == http.StatusNotFound {
		if s.curseForge != nil {
			if result, found, cfErr := s.curseForge.Lookup(ctx, fingerprints); cfErr == nil && found {
				s.save(ctx, sha512, result)
				return result, nil
			}
		}
		result := Result{
			Source:    "modrinth",
			Verdict:   VerdictUnknown,
			Reason:    "файл не найден в официальном источнике",
			CheckedAt: time.Now(),
		}
		s.save(ctx, sha512, result)
		return result, nil
	}
	if status < 200 || status >= 300 {
		return Result{}, fmt.Errorf("modrinth returned %d", status)
	}

	fileHash := ""
	fileName := ""
	fileURL := ""
	for _, file := range version.Files {
		if strings.EqualFold(file.Hashes.SHA512, sha512) {
			fileHash = file.Hashes.SHA512
			fileName = file.Filename
			fileURL = file.URL
			if file.Primary {
				break
			}
		}
	}
	if fileHash == "" {
		return Result{
			Source:        "modrinth",
			Verdict:       VerdictTampered,
			Reason:        "hash не совпадает ни с одним файлом версии",
			ProjectID:     version.ProjectID,
			VersionID:     version.ID,
			VersionNumber: version.VersionNumber,
			CheckedAt:     time.Now(),
		}, nil
	}

	project, _ := s.lookupProject(ctx, version.ProjectID)
	result := Result{
		Source:        "modrinth",
		Verdict:       VerdictVerified,
		Reason:        "файл найден в Modrinth",
		ProjectID:     version.ProjectID,
		ProjectSlug:   project.Slug,
		Title:         firstNonEmpty(project.Title, version.Name),
		VersionID:     version.ID,
		VersionNumber: version.VersionNumber,
		FileName:      fileName,
		FileURL:       fileURL,
		GameVersions:  version.GameVersions,
		Loaders:       version.Loaders,
		CheckedAt:     time.Now(),
	}
	s.save(ctx, sha512, result)
	return result, nil
}

func (s *Service) lookupVersion(ctx context.Context, sha512 string) (modrinthVersion, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/version_file/"+sha512+"?algorithm=sha512", nil)
	if err != nil {
		return modrinthVersion{}, 0, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return modrinthVersion{}, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return modrinthVersion{}, response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return modrinthVersion{}, response.StatusCode, nil
	}
	var version modrinthVersion
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&version); err != nil {
		return modrinthVersion{}, response.StatusCode, err
	}
	return version, response.StatusCode, nil
}

func (s *Service) lookupProject(ctx context.Context, projectID string) (modrinthProject, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/project/"+projectID, nil)
	if err != nil {
		return modrinthProject{}, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return modrinthProject{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return modrinthProject{}, fmt.Errorf("modrinth project returned %d", response.StatusCode)
	}
	var project modrinthProject
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&project); err != nil {
		return modrinthProject{}, err
	}
	return project, nil
}

func (s *Service) cached(ctx context.Context, sha512 string) (Result, bool) {
	if s.db == nil {
		return Result{}, false
	}
	var result Result
	var gameVersions, loaders []byte
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT source, verdict, reason, project_id, project_slug, title, version_id,
			version_number, file_name, file_url, game_versions, loaders, checked_at, expires_at
		FROM mod_verifications WHERE sha512 = $1 ORDER BY checked_at DESC LIMIT 1`, sha512).
		Scan(&result.Source, &result.Verdict, &result.Reason, &result.ProjectID, &result.ProjectSlug,
			&result.Title, &result.VersionID, &result.VersionNumber, &result.FileName, &result.FileURL,
			&gameVersions, &loaders, &result.CheckedAt, &expiresAt)
	if err != nil {
		return Result{}, false
	}
	if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
		return Result{}, false
	}
	_ = json.Unmarshal(gameVersions, &result.GameVersions)
	_ = json.Unmarshal(loaders, &result.Loaders)
	return result, true
}

func (s *Service) save(ctx context.Context, sha512 string, result Result) {
	if s.db == nil {
		return
	}
	gameVersions, _ := json.Marshal(result.GameVersions)
	loaders, _ := json.Marshal(result.Loaders)
	expires := time.Now().Add(6 * time.Hour)
	if result.Verdict == VerdictUnknown {
		expires = time.Now().Add(time.Hour)
	}
	source := result.Source
	if source == "" {
		source = "modrinth"
	}
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO mod_verifications (
			source, sha512, verdict, project_id, project_slug, title, version_id,
			version_number, file_name, file_url, game_versions, loaders, reason, checked_at, expires_at
		) VALUES ($14,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now(),$13)
		ON CONFLICT (source, sha512) DO UPDATE SET
			verdict = EXCLUDED.verdict,
			project_id = EXCLUDED.project_id,
			project_slug = EXCLUDED.project_slug,
			title = EXCLUDED.title,
			version_id = EXCLUDED.version_id,
			version_number = EXCLUDED.version_number,
			file_name = EXCLUDED.file_name,
			file_url = EXCLUDED.file_url,
			game_versions = EXCLUDED.game_versions,
			loaders = EXCLUDED.loaders,
			reason = EXCLUDED.reason,
			checked_at = now(),
			expires_at = EXCLUDED.expires_at`,
		sha512, result.Verdict, result.ProjectID, result.ProjectSlug, result.Title,
		result.VersionID, result.VersionNumber, result.FileName, result.FileURL,
		gameVersions, loaders, result.Reason, expires, source)
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
	var body struct {
		SHA512       string   `json:"sha512"`
		Fingerprints []uint32 `json:"fingerprints"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	result, err := s.Verify(request.Context(), body.SHA512, body.Fingerprints)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
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

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
