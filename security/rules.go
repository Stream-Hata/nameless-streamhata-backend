package security

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HashRuleMalicious = "malicious"
	HashRuleTrusted   = "trusted"
)

type HashRule struct {
	ID        int64     `json:"id"`
	SHA256    string    `json:"sha256"`
	Verdict   string    `json:"verdict"`
	Reason    string    `json:"reason"`
	Source    string    `json:"source"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type HashRuleStore interface {
	Lookup(ctx context.Context, hash string) (HashRule, bool, error)
}

type DBHashRuleStore struct {
	db *sql.DB
}

func NewDBHashRuleStore(db *sql.DB) *DBHashRuleStore {
	return &DBHashRuleStore{db: db}
}

func (s *DBHashRuleStore) Lookup(ctx context.Context, hash string) (HashRule, bool, error) {
	var rule HashRule
	err := s.db.QueryRowContext(ctx, `
		SELECT id, sha256, verdict, reason, source, created_by, created_at
		FROM security_hash_rules WHERE sha256 = $1`, hash).
		Scan(&rule.ID, &rule.SHA256, &rule.Verdict, &rule.Reason, &rule.Source, &rule.CreatedBy, &rule.CreatedAt)
	if err == sql.ErrNoRows {
		return HashRule{}, false, nil
	}
	if err != nil {
		return HashRule{}, false, err
	}
	return rule, true, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func normalizeHashRule(rule HashRule) (HashRule, error) {
	rule.SHA256 = strings.ToLower(strings.TrimSpace(rule.SHA256))
	rule.Verdict = strings.ToLower(strings.TrimSpace(rule.Verdict))
	rule.Reason = strings.TrimSpace(rule.Reason)
	if !validSHA256(rule.SHA256) {
		return HashRule{}, fmt.Errorf("sha256 must be 64 hex characters")
	}
	if rule.Verdict != HashRuleMalicious && rule.Verdict != HashRuleTrusted {
		return HashRule{}, fmt.Errorf("verdict must be malicious or trusted")
	}
	if rule.Source == "" {
		rule.Source = "admin"
	}
	return rule, nil
}

func (s *Service) HashRules(ctx context.Context) ([]HashRule, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sha256, verdict, reason, source, created_by, created_at
		FROM security_hash_rules ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]HashRule, 0)
	for rows.Next() {
		var rule HashRule
		if err := rows.Scan(&rule.ID, &rule.SHA256, &rule.Verdict, &rule.Reason, &rule.Source, &rule.CreatedBy, &rule.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Service) AddHashRule(ctx context.Context, rule HashRule, actor string) (HashRule, error) {
	if s.db == nil {
		return HashRule{}, fmt.Errorf("database unavailable")
	}
	var err error
	rule, err = normalizeHashRule(rule)
	if err != nil {
		return HashRule{}, err
	}
	rule.CreatedBy = strings.TrimSpace(actor)
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO security_hash_rules (sha256, verdict, reason, source, created_by)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (sha256) DO UPDATE SET
			verdict = EXCLUDED.verdict,
			reason = EXCLUDED.reason,
			source = EXCLUDED.source,
			created_by = EXCLUDED.created_by,
			created_at = now()
		RETURNING id, created_at`,
		rule.SHA256, rule.Verdict, rule.Reason, rule.Source, rule.CreatedBy,
	).Scan(&rule.ID, &rule.CreatedAt)
	if err != nil {
		return HashRule{}, err
	}
	return rule, nil
}

func (s *Service) DeleteHashRule(ctx context.Context, id int64) error {
	if s.db == nil {
		return fmt.Errorf("database unavailable")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM security_hash_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("hash rule not found")
	}
	return nil
}

func (s *Service) hashRulesRoute(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.isAdmin(request); !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	switch request.Method {
	case http.MethodGet:
		items, err := s.HashRules(request.Context())
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "failed to load hash rules"})
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var rule HashRule
		if err := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16<<10)).Decode(&rule); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		login, _ := s.isAdmin(request)
		saved, err := s.AddHashRule(request.Context(), rule, login)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(response, http.StatusCreated, saved)
	default:
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) hashRuleRoute(response http.ResponseWriter, request *http.Request) {
	if _, ok := s.isAdmin(request); !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if request.Method != http.MethodDelete {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err := s.DeleteHashRule(request.Context(), id); err != nil {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) hashRulesBulkRoute(response http.ResponseWriter, request *http.Request) {
	login, ok := s.isAdmin(request)
	if !ok {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})

		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	var data []byte
	var err error
	contentType := request.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid form"})

			return
		}
		file, _, fileErr := request.FormFile("file")
		if fileErr != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "file is required"})

			return
		}
		defer func() {
			_ = file.Close()
		}()
		data, err = io.ReadAll(io.LimitReader(file, 2<<20))
	} else {
		data, err = io.ReadAll(http.MaxBytesReader(response, request.Body, 2<<20))
	}
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "failed to read list"})

		return
	}

	verdict := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("verdict")))
	if verdict != HashRuleTrusted {
		verdict = HashRuleMalicious
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	added := 0
	failed := 0
	failures := make([]string, 0)
	seen := make(map[string]bool)

	for index, line := range lines {
		if len(seen) >= 10000 {
			break
		}
		value := strings.TrimSpace(line)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		fields := strings.FieldsFunc(value, func(symbol rune) bool {
			return symbol == ',' || symbol == ';' || symbol == '\t' || symbol == '|'
		})
		if len(fields) == 0 {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(fields[0]))
		if hash == "sha256" || hash == "hash" {
			continue
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true

		rule := HashRule{SHA256: hash, Verdict: verdict, Source: "bulk"}
		if len(fields) > 1 {
			candidate := strings.ToLower(strings.TrimSpace(fields[1]))
			if candidate == HashRuleMalicious || candidate == HashRuleTrusted {
				rule.Verdict = candidate
				if len(fields) > 2 {
					rule.Reason = strings.Join(fields[2:], ", ")
				}
			} else {
				rule.Reason = strings.Join(fields[1:], ", ")
			}
		}
		if _, err := s.AddHashRule(request.Context(), rule, login); err != nil {
			failed++
			if len(failures) < 10 {
				failures = append(failures, fmt.Sprintf("line %d: %v", index+1, err))
			}

			continue
		}
		added++
	}

	writeJSON(response, http.StatusOK, map[string]any{
		"ok":     true,
		"added":  added,
		"failed": failed,
		"errors": failures,
	})
}
