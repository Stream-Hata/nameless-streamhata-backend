package main

import (
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/commands"
	"github.com/p7x-dev/nameless-streamhata-backend/points"
	"github.com/p7x-dev/nameless-streamhata-backend/timers"
)

func TestChannelStorageIsolation(t *testing.T) {
	dsn := os.Getenv("STREAMHATA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires a disposable PostgreSQL database with migrations applied")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	prefix := time.Now().UnixNano()
	firstID, secondID := strconv.FormatInt(prefix, 10), strconv.FormatInt(prefix+1, 10)
	for _, channelID := range []string{firstID, secondID} {
		service := commands.NewForChannel(nil, db, channelID)
		if _, err := service.Upsert(commands.Command{ID: "shared", Name: "hello", Response: channelID}); err != nil {
			t.Fatal(err)
		}
		timerService := timers.New(nil)
		if err := timerService.UseStorage(db, channelID); err != nil {
			t.Fatal(err)
		}
		if _, err := timerService.Upsert(timers.Timer{ID: "shared", Messages: []string{channelID}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}

	firstCommands := commands.NewForChannel(nil, db, firstID)
	if !firstCommands.Delete("shared") {
		t.Fatal("first channel command missing")
	}
	secondCommands := commands.NewForChannel(nil, db, secondID).List()
	if len(secondCommands) != 1 || secondCommands[0].Response != secondID {
		t.Fatalf("second channel commands changed: %+v", secondCommands)
	}

	firstTimers, secondTimers := timers.New(nil), timers.New(nil)
	if err := firstTimers.UseStorage(db, firstID); err != nil {
		t.Fatal(err)
	}
	if err := firstTimers.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := secondTimers.UseStorage(db, secondID); err != nil {
		t.Fatal(err)
	}
	items := secondTimers.List()
	if len(items) != 1 || !items[0].Enabled || items[0].Messages[0] != secondID {
		t.Fatalf("second channel timers changed: %+v", items)
	}

	wallets := points.NewForChannel(nil, db, firstID)
	wallets.Add("viewer-1", "viewer", "Viewer", 7, "test")
	wallet, exists := points.NewForChannel(nil, db, firstID).Wallet("viewer-1")
	if !exists || wallet.Balance != 7 {
		t.Fatalf("first channel wallet did not persist: %+v", wallet)
	}
	if _, exists := points.NewForChannel(nil, db, secondID).Wallet("viewer-1"); exists {
		t.Fatal("first channel wallet is visible in the second channel")
	}
}
