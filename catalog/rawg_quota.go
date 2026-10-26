package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rawgQuotaState struct {
	Month       string `json:"month"`
	Count       int    `json:"count"`
	Limit       int    `json:"limit"`
	PausedUntil string `json:"pausedUntil,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	RewindYear  int    `json:"rewindYear,omitempty"`
}

var rawgQuota struct {
	sync.Mutex
	db    *sql.DB
	state rawgQuotaState
	ready bool
}

func initRAWGQuota(db *sql.DB) {
	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuota.db = db
	rawgQuota.ready = false
}

func rawgQuotaMonth() string {
	return time.Now().UTC().Format("2006-01")
}

func rawgQuotaLimit() int {
	limit := 18000

	if value := strings.TrimSpace(os.Getenv("RAWG_MONTHLY_LIMIT")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	return limit
}

func (q rawgQuotaState) paused() bool {
	if q.PausedUntil == "" {
		return false
	}

	until, err := time.Parse(time.RFC3339, q.PausedUntil)
	if err != nil {
		return false
	}

	return time.Now().UTC().Before(until)
}

func rawgQuotaEnsure() {
	if rawgQuota.ready {
		return
	}

	rawgQuota.ready = true
	rawgQuota.state = rawgQuotaState{Month: rawgQuotaMonth(), Limit: rawgQuotaLimit()}

	if rawgQuota.db == nil {
		return
	}

	var raw []byte

	if err := rawgQuota.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = 'rawg_quota'`,
	).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &rawgQuota.state)
	}

	rawgQuota.state.Limit = rawgQuotaLimit()

	if rawgQuota.state.Month != rawgQuotaMonth() {
		rawgQuota.state.Month = rawgQuotaMonth()
		rawgQuota.state.Count = 0
		rawgQuota.state.PausedUntil = ""
		rawgQuota.state.LastError = ""
	}

	rawgQuota.state.Limit = rawgQuotaLimit()
}

func rawgQuotaPersist() {
	if rawgQuota.db == nil {
		return
	}

	raw, err := json.Marshal(rawgQuota.state)
	if err != nil {
		return
	}

	_, _ = rawgQuota.db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ('rawg_quota', $1)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		string(raw),
	)
}

func rawgQuotaBegin() error {
	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuotaEnsure()

	if rawgQuota.state.paused() {
		return errors.New("rawg: quota paused until " + rawgQuota.state.PausedUntil)
	}

	rawgQuota.state.PausedUntil = ""

	if rawgQuota.state.Count >= rawgQuota.state.Limit {
		rawgQuota.state.PausedUntil = nextMonthStart().Format(time.RFC3339)
		rawgQuota.state.LastError = "monthly API limit reached"
		rawgQuotaPersist()

		return errors.New("rawg: monthly request limit reached")
	}

	rawgQuota.state.Count++
	rawgQuotaPersist()

	return nil
}

func rawgQuotaObserve(status int, body []byte) {
	if status != 401 && status != 429 {
		return
	}

	message := string(body)

	if !strings.Contains(strings.ToLower(message), "monthly api limit reached") &&
		!strings.Contains(strings.ToLower(message), "limit reached") {
		return
	}

	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuotaEnsure()
	rawgQuota.state.PausedUntil = nextMonthStart().Format(time.RFC3339)
	rawgQuota.state.LastError = strings.TrimSpace(message)
	rawgQuota.state.RewindYear = 1982
	rawgQuotaPersist()
	log.Printf("[rawg-quota] monthly limit reached, paused until %s", rawgQuota.state.PausedUntil)
}

func rawgQuotaStatus() rawgQuotaState {
	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuotaEnsure()

	return rawgQuota.state
}

func rawgQuotaTakeRewind() int {
	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuotaEnsure()
	year := rawgQuota.state.RewindYear
	rawgQuota.state.RewindYear = 0
	rawgQuotaPersist()

	return year
}

func resumeRAWGQuota() rawgQuotaState {
	rawgQuota.Lock()
	defer rawgQuota.Unlock()

	rawgQuotaEnsure()
	rawgQuota.state.PausedUntil = ""
	rawgQuota.state.LastError = ""
	rawgQuotaPersist()

	return rawgQuota.state
}

func nextMonthStart() time.Time {
	now := time.Now().UTC()

	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}
