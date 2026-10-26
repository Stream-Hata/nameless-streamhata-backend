package donations

import (
	"context"
	"database/sql"
	"encoding/json"
)

const tokenStoreKey = "donationalerts_tokens"

type tokenStore struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

func LoadDonationAlertsTokens(db *sql.DB) (string, string) {
	if db == nil {
		return "", ""
	}

	var raw []byte

	if err := db.QueryRowContext(
		context.Background(),
		`SELECT value FROM app_setting WHERE key = $1`,
		tokenStoreKey,
	).Scan(&raw); err != nil {
		return "", ""
	}

	var store tokenStore
	if json.Unmarshal(raw, &store) != nil {
		return "", ""
	}

	return store.Access, store.Refresh
}

func SaveDonationAlertsTokens(db *sql.DB, access, refresh string) {
	if db == nil || (access == "" && refresh == "") {
		return
	}

	raw, err := json.Marshal(tokenStore{Access: access, Refresh: refresh})
	if err != nil {
		return
	}

	_, _ = db.ExecContext(
		context.Background(),
		`INSERT INTO app_setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		tokenStoreKey,
		string(raw),
	)
}
