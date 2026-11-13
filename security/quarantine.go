package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const quarantineRetention = 24 * time.Hour

func quarantineDirectory() string {
	directory := strings.TrimSpace(os.Getenv("SECURITY_QUARANTINE_DIR"))
	if directory == "" {
		directory = "quarantine"
	}

	return directory
}

func (s *Service) Quarantine(ctx context.Context, incidentID int64, data []byte, hash string) error {
	if s.db == nil {
		return fmt.Errorf("database unavailable")
	}
	directory := quarantineDirectory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create quarantine directory: %w", err)
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate quarantine name: %w", err)
	}
	name := hex.EncodeToString(random) + ".bin"
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write quarantine file: %w", err)
	}

	expires := time.Now().Add(quarantineRetention)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO security_quarantine (incident_id, sha256, path, size, expires_at)
		VALUES ($1,$2,$3,$4,$5)`, incidentID, hash, path, len(data), expires); err != nil {
		_ = os.Remove(path)

		return fmt.Errorf("record quarantine file: %w", err)
	}

	return nil
}

func (s *Service) CleanupQuarantine(ctx context.Context) (int, error) {
	if s.db == nil {
		return 0, fmt.Errorf("database unavailable")
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path FROM security_quarantine
		WHERE deleted_at IS NULL AND expires_at <= now()
		ORDER BY expires_at
		LIMIT 100`)
	if err != nil {
		return 0, err
	}

	type item struct {
		id   int64
		path string
	}
	items := make([]item, 0)
	for rows.Next() {
		var entry item
		if err := rows.Scan(&entry.id, &entry.path); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, entry)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range items {
		_ = os.Remove(entry.path)
		if _, err := s.db.ExecContext(ctx, `UPDATE security_quarantine SET deleted_at = now() WHERE id = $1`, entry.id); err != nil {
			continue
		}
		count++
	}

	return count, nil
}
