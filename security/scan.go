package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ScanNoThreatsFound = "no_threats_found"
	ScanCleanDegraded  = "clean_degraded"
	ScanSuspicious     = "suspicious"
	ScanBlocked        = "blocked"
	ScanUnavailable    = "scan_unavailable"
	ScanAllowlisted    = "allowlisted"
)

var (
	ErrScannerDisabled = errors.New("security scanner is not configured")
	ErrScanUnavailable = errors.New("security scanner unavailable")
)

type ScanMeta struct {
	FileName   string `json:"fileName"`
	Mime       string `json:"mime"`
	Executable bool   `json:"executable"`
}

type EngineReport struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Detected bool   `json:"detected"`
	Trusted  bool   `json:"trusted"`
	Detail   string `json:"detail,omitempty"`
}

type ScanReport struct {
	SHA256            string         `json:"sha256"`
	Size              int64          `json:"size"`
	FileName          string         `json:"fileName"`
	Verdict           string         `json:"verdict"`
	Severity          string         `json:"severity"`
	Reason            string         `json:"reason"`
	RawDetections     int            `json:"rawDetections"`
	TrustedDetections int            `json:"trustedDetections"`
	Engines           []EngineReport `json:"engines"`
	Cached            bool           `json:"cached"`
	CheckedAt         time.Time      `json:"checkedAt"`
	Error             string         `json:"error,omitempty"`
}

type HashRecord struct {
	SHA256    string
	Verdict   string
	Reason    string
	Source    string
	CheckedAt time.Time
	ExpiresAt *time.Time
}

type HashStore interface {
	Lookup(ctx context.Context, hash string) (HashRecord, bool, error)
	Save(ctx context.Context, record HashRecord) error
}

type ClamResult struct {
	Detected  bool
	Signature string
}

type ClamAVEngine interface {
	Scan(ctx context.Context, data []byte) (ClamResult, error)
}

type ReputationResult struct {
	RawDetections     int            `json:"rawDetections"`
	TrustedDetections int            `json:"trustedDetections"`
	Engines           []EngineReport `json:"engines"`
}

type ReputationEngine interface {
	Lookup(ctx context.Context, hash string) (ReputationResult, error)
}

type Scanner struct {
	hashes     HashStore
	rules      HashRuleStore
	clam       ClamAVEngine
	reputation ReputationEngine
	cacheTTL   time.Duration
	now        func() time.Time
}

func NewScanner(db *sql.DB) *Scanner {
	scanner := &Scanner{
		cacheTTL: 24 * time.Hour,
		now:      time.Now,
	}
	if db != nil {
		scanner.hashes = NewDBHashStore(db)
		scanner.rules = NewDBHashRuleStore(db)
	}
	if addr := strings.TrimSpace(os.Getenv("SECURITY_CLAMAV_ADDR")); addr != "" {
		scanner.clam = &ClamAVScanner{Addr: addr, Timeout: 10 * time.Second}
	}
	if url := strings.TrimSpace(os.Getenv("SECURITY_REPUTATION_URL")); url != "" {
		scanner.reputation = &HTTPReputation{URL: url, Client: &http.Client{Timeout: 12 * time.Second}}
	}

	return scanner
}

func (s *Scanner) Enabled() bool {
	return s != nil && (s.clam != nil || s.reputation != nil)
}

func (s *Scanner) Scan(ctx context.Context, data []byte, meta ScanMeta) (ScanReport, error) {
	if !s.Enabled() {
		return ScanReport{}, ErrScannerDisabled
	}
	if !meta.Executable {
		meta.Executable = executableName(meta.FileName)
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	now := s.now()
	report := ScanReport{
		SHA256:    hash,
		Size:      int64(len(data)),
		FileName:  meta.FileName,
		CheckedAt: now,
	}

	if s.rules != nil {
		rule, found, err := s.rules.Lookup(ctx, hash)
		if err == nil && found {
			report.Engines = []EngineReport{{Name: "admin-rule", Status: rule.Verdict, Detected: rule.Verdict == HashRuleMalicious, Trusted: true, Detail: rule.Reason}}
			if rule.Verdict == HashRuleTrusted {
				report.Verdict = ScanAllowlisted
				report.Severity = "info"
				report.Reason = firstNonEmptyString(rule.Reason, "hash allowlisted by administrator")
				return report, nil
			}
			report.Verdict = ScanBlocked
			report.Severity = "critical"
			report.Reason = firstNonEmptyString(rule.Reason, "hash blocked by administrator")
			report.TrustedDetections = 1
			report.RawDetections = 1
			s.save(ctx, report, meta, now)
			return report, nil
		}
	}

	if s.hashes != nil {
		record, found, err := s.hashes.Lookup(ctx, hash)
		if err == nil && found {
			if record.Verdict == "malicious" {
				report.Verdict = ScanBlocked
				report.Severity = "critical"
				report.Reason = record.Reason
				report.Cached = true
				report.Engines = []EngineReport{{Name: "cache", Status: "detected", Detected: true, Trusted: true, Detail: record.Source}}
				return report, nil
			}
			if record.Verdict == "clean" && (record.ExpiresAt == nil || record.ExpiresAt.After(now)) {
				report.Verdict = ScanNoThreatsFound
				report.Severity = "info"
				report.Reason = "cached clean result"
				report.Cached = true
				report.Engines = []EngineReport{{Name: "cache", Status: "clean", Detail: record.Source}}
				return report, nil
			}
		}
	}

	clamDetected := false
	if s.clam != nil {
		result, err := s.clam.Scan(ctx, data)
		if err != nil {
			report.Verdict = ScanUnavailable
			report.Severity = "warning"
			report.Reason = "ClamAV is unavailable"
			report.Error = err.Error()
			return report, ErrScanUnavailable
		}
		engine := EngineReport{Name: "clamav", Status: "clean", Trusted: true}
		if result.Detected {
			clamDetected = true
			engine.Status = "detected"
			engine.Detected = true
			engine.Detail = result.Signature
			report.RawDetections++
			report.TrustedDetections++
		}
		report.Engines = append(report.Engines, engine)
	}

	if s.reputation != nil {
		result, err := s.reputation.Lookup(ctx, hash)
		if err != nil {
			report.Engines = append(report.Engines, EngineReport{Name: "reputation", Status: "error", Detail: err.Error()})
			if report.TrustedDetections == 0 {
				report.Verdict = ScanCleanDegraded
				report.Severity = "warning"
				report.Reason = "external reputation is unavailable"
				s.save(ctx, report, meta, now)

				return report, nil
			}
		}
		report.RawDetections += result.RawDetections
		report.TrustedDetections += result.TrustedDetections
		report.Engines = append(report.Engines, result.Engines...)
	}

	switch {
	case clamDetected:
		report.Verdict = ScanBlocked
		report.Severity = "critical"
		report.Reason = "ClamAV detected malware"
	case report.TrustedDetections >= 2:
		report.Verdict = ScanBlocked
		report.Severity = "critical"
		report.Reason = fmt.Sprintf("%d trusted engines detected malware", report.TrustedDetections)
	case report.TrustedDetections == 1 && meta.Executable:
		report.Verdict = ScanBlocked
		report.Severity = "critical"
		report.Reason = "executable flagged by a trusted engine"
	case report.TrustedDetections == 1:
		report.Verdict = ScanSuspicious
		report.Severity = "warning"
		report.Reason = "flagged by one trusted engine"
	case report.RawDetections > 0:
		report.Verdict = ScanSuspicious
		report.Severity = "warning"
		report.Reason = "flagged by untrusted engines only"
	default:
		report.Verdict = ScanNoThreatsFound
		report.Severity = "info"
		report.Reason = "no threats found"
	}

	s.save(ctx, report, meta, now)
	return report, nil
}

func (s *Scanner) save(ctx context.Context, report ScanReport, meta ScanMeta, now time.Time) {
	if s.hashes == nil {
		return
	}
	record := HashRecord{
		SHA256:    report.SHA256,
		Verdict:   "clean",
		Reason:    report.Reason,
		Source:    "scanner",
		CheckedAt: now,
	}
	switch report.Verdict {
	case ScanBlocked:
		record.Verdict = "malicious"
	case ScanSuspicious:
		record.Verdict = "suspicious"
	}
	if record.Verdict != "malicious" {
		expires := now.Add(s.cacheTTL)
		record.ExpiresAt = &expires
	}
	_ = s.hashes.Save(ctx, record)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func executableName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".apk", ".apks", ".xapk", ".aab", ".exe", ".msi", ".msix", ".dll", ".scr", ".com",
		".dmg", ".pkg", ".app", ".ipa", ".deb", ".rpm", ".appimage", ".jar", ".sh", ".ps1",
		".bat", ".cmd", ".elf", ".bin", ".run":
		return true
	default:
		return false
	}
}

type DBHashStore struct {
	db *sql.DB
}

func NewDBHashStore(db *sql.DB) *DBHashStore {
	return &DBHashStore{db: db}
}

func (s *DBHashStore) Lookup(ctx context.Context, hash string) (HashRecord, bool, error) {
	var record HashRecord
	var expiresAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT sha256, verdict, reason, source, checked_at, expires_at
		FROM security_file_hashes WHERE sha256 = $1`, hash).
		Scan(&record.SHA256, &record.Verdict, &record.Reason, &record.Source, &record.CheckedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HashRecord{}, false, nil
	}
	if err != nil {
		return HashRecord{}, false, err
	}
	if expiresAt.Valid {
		record.ExpiresAt = &expiresAt.Time
	}

	return record, true, nil
}

func (s *DBHashStore) Save(ctx context.Context, record HashRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO security_file_hashes (sha256, verdict, reason, source, checked_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (sha256) DO UPDATE SET
			verdict = EXCLUDED.verdict,
			reason = EXCLUDED.reason,
			source = EXCLUDED.source,
			checked_at = EXCLUDED.checked_at,
			expires_at = EXCLUDED.expires_at`,
		record.SHA256, record.Verdict, record.Reason, record.Source, record.CheckedAt, record.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("save file hash: %w", err)
	}

	return nil
}

type ClamAVScanner struct {
	Addr    string
	Timeout time.Duration
}

func (s *ClamAVScanner) Scan(ctx context.Context, data []byte) (ClamResult, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return ClamResult{}, err
	}
	defer func() {
		_ = conn.Close()
	}()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return ClamResult{}, err
	}
	for offset := 0; offset < len(data); offset += 32 << 10 {
		end := offset + (32 << 10)
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(chunk)))
		if _, err := conn.Write(size[:]); err != nil {
			return ClamResult{}, err
		}
		if _, err := conn.Write(chunk); err != nil {
			return ClamResult{}, err
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return ClamResult{}, err
	}

	raw, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if err != nil {
		return ClamResult{}, err
	}
	text := strings.TrimSpace(strings.TrimRight(string(raw), "\x00"))
	if strings.Contains(text, "FOUND") {
		signature := text
		if index := strings.Index(text, ":"); index >= 0 {
			signature = strings.TrimSpace(text[index+1:])
		}
		return ClamResult{Detected: true, Signature: signature}, nil
	}
	if strings.Contains(text, "OK") {
		return ClamResult{}, nil
	}

	return ClamResult{}, fmt.Errorf("clamav: unexpected response %q", text)
}

type HTTPReputation struct {
	URL    string
	Client *http.Client
}

func (s *HTTPReputation) Lookup(ctx context.Context, hash string) (ReputationResult, error) {
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]string{"sha256": hash})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return ReputationResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ReputationResult{}, err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ReputationResult{}, fmt.Errorf("reputation returned %d", response.StatusCode)
	}
	var result ReputationResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return ReputationResult{}, err
	}

	return result, nil
}
