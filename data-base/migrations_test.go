package data_base

import (
	"database/sql"
	"os"
	"testing"
)

func TestApplyEmbeddedMigrations(t *testing.T) {
	dsn := os.Getenv("STREAMHATA_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an empty disposable PostgreSQL database")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := applyMigrations(db); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(db); err != nil {
		t.Fatalf("reapplying migrations: %v", err)
	}
	var columnCount int
	if err := db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'channel_id'
		  AND table_name IN ('points_wallet', 'points_transaction')`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	if columnCount != 2 {
		t.Fatalf("expected both points channel columns, got %d", columnCount)
	}
}
