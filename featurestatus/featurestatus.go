// Package featurestatus exposes the app-wide list of features that may be unstable.
package featurestatus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/p7x-dev/nameless-streamhata-backend/access"
)

const settingKey = "feature_status"

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type Feature struct {
	ID string `json:"id"`
}

type Snapshot struct {
	Features []Feature         `json:"features"`
	Statuses map[string]string `json:"statuses"`
	Unstable []string          `json:"unstable,omitempty"`
}

type Service struct {
	db     *sql.DB
	access *access.Service
}

var catalog = []Feature{
	{ID: "streamer-sites"},
	{ID: "viewer-playlists"},
	{ID: "streamer-playlists"},
	{ID: "yandex-music"},
	{ID: "chat-external-send"},
	{ID: "tui-editor-plugins"},
	{ID: "android-tui-editor"},
	{ID: "webgl-3d"},
}

var defaultUnstable = []string{
	"streamer-sites",
	"viewer-playlists",
	"streamer-playlists",
	"yandex-music",
	"chat-external-send",
	"tui-editor-plugins",
	"android-tui-editor",
	"webgl-3d",
}

func New(db *sql.DB, accessService *access.Service) *Service {
	return &Service{db: db, access: accessService}
}

func (s *Service) RegisterRoutes(server Deps) {
	server.ServeEndpoint("/rgg-sync/features", s.publicRoute)
	server.ServeEndpoint("/rgg-sync/features/admin", s.adminRoute)
}

func (s *Service) publicRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	snapshot, err := s.load(r)
	if err != nil {
		http.Error(w, "failed to load feature status", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Service) adminRoute(w http.ResponseWriter, r *http.Request) {
	login := ""
	if s.access != nil {
		login = s.access.LoginFromRequest(r)
	}
	if login == "" || !s.access.IsAdmin(login) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.load(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load feature status"})
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	case http.MethodPut:
		s.update(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) load(r *http.Request) (Snapshot, error) {
	snapshot := Snapshot{Features: append([]Feature(nil), catalog...), Statuses: defaultStatuses()}
	snapshot.Unstable = unstableIDs(snapshot.Statuses)
	if s.db == nil {
		return snapshot, nil
	}
	var raw []byte
	err := s.db.QueryRowContext(r.Context(), `SELECT value FROM app_setting WHERE key = $1`, settingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var stored struct {
		Statuses map[string]string `json:"statuses"`
		Unstable []string          `json:"unstable"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return Snapshot{}, err
	}
	if stored.Statuses != nil {
		snapshot.Statuses = normalizeStatuses(stored.Statuses)
	} else {
		snapshot.Statuses = make(map[string]string, len(stored.Unstable))
		for _, id := range normalizeKnown(stored.Unstable) {
			snapshot.Statuses[id] = "unstable"
		}
	}
	snapshot.Unstable = unstableIDs(snapshot.Statuses)
	return snapshot, nil
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "database unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Statuses map[string]string `json:"statuses"`
		Unstable []string          `json:"unstable"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid feature status"})
		return
	}
	allowed := make(map[string]bool, len(catalog))
	for _, feature := range catalog {
		allowed[feature.ID] = true
	}
	statuses := make(map[string]string)
	if input.Statuses != nil {
		for rawID, status := range input.Statuses {
			id := strings.TrimSpace(rawID)
			if !allowed[id] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown feature: " + rawID})
				return
			}
			if status != "" && status != "unstable" && status != "untested" && status != "experimental" && status != "maintenance" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status for feature: " + id})
				return
			}
			if status != "" {
				statuses[id] = status
			}
		}
	} else {
		for _, id := range normalizeKnown(input.Unstable) {
			statuses[id] = "unstable"
		}
		for _, id := range input.Unstable {
			if !allowed[strings.TrimSpace(id)] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown feature: " + id})
				return
			}
		}
	}
	raw, err := json.Marshal(struct {
		Statuses map[string]string `json:"statuses"`
	}{Statuses: statuses})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to encode feature status"})
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO app_setting (key, value) VALUES ($1, $2::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, settingKey, string(raw)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save feature status"})
		return
	}
	writeJSON(w, http.StatusOK, Snapshot{Features: append([]Feature(nil), catalog...), Statuses: statuses, Unstable: unstableIDs(statuses)})
}

func normalize(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeKnown(values []string) []string {
	allowed := make(map[string]bool, len(catalog))
	for _, feature := range catalog {
		allowed[feature.ID] = true
	}
	known := make([]string, 0, len(values))
	for _, value := range values {
		if allowed[strings.TrimSpace(value)] {
			known = append(known, value)
		}
	}
	return normalize(known)
}

func defaultStatuses() map[string]string {
	statuses := make(map[string]string, len(defaultUnstable))
	for _, id := range defaultUnstable {
		statuses[id] = "unstable"
	}
	return statuses
}

func normalizeStatuses(values map[string]string) map[string]string {
	allowed := make(map[string]bool, len(catalog))
	for _, feature := range catalog {
		allowed[feature.ID] = true
	}
	result := make(map[string]string)
	for id, status := range values {
		id = strings.TrimSpace(id)
		if allowed[id] && (status == "unstable" || status == "untested" || status == "experimental" || status == "maintenance") {
			result[id] = status
		}
	}
	return result
}

func unstableIDs(statuses map[string]string) []string {
	ids := make([]string, 0)
	for id, status := range statuses {
		if status == "unstable" {
			ids = append(ids, id)
		}
	}
	return normalize(ids)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
