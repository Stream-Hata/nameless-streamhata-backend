package chatruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestSendAsBroadcasterUsesSessionCredentials(t *testing.T) {
	runtime := &Runtime{
		broadcaster: twitch.ChatUser{ID: "owner"},
		activeToken: "owner-token",
		sent:        make(map[string]time.Time),
		http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if request.Header.Get("Authorization") != "Bearer viewer-token" ||
				payload["broadcaster_id"] != "viewer" || payload["sender_id"] != "viewer" {
				t.Fatalf("message routed to the wrong account: %+v", payload)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":[{"is_sent":true}]}`)),
			}, nil
		})},
	}
	if err := runtime.SendAsBroadcaster(context.Background(), "viewer-token", "viewer", "hello"); err != nil {
		t.Fatal(err)
	}
	if runtime.IsOwnText("hello") {
		t.Fatal("another account's message was marked as sent by the primary account")
	}
}

func TestReplySendsParentMessageID(t *testing.T) {
	called := false
	runtime := &Runtime{
		broadcaster: twitch.ChatUser{ID: "channel-id"},
		activeToken: "channel-token",
		sent:        make(map[string]time.Time),
		http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = true
			var payload map[string]string
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["reply_parent_message_id"] != "original-id" || payload["message"] != "Перевод сообщения: привет" {
				t.Fatalf("unexpected Twitch reply: %+v", payload)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":[{"is_sent":true}]}`)),
			}, nil
		})},
	}
	if err := runtime.Reply("original-id", "Перевод сообщения: привет"); err != nil || !called {
		t.Fatalf("reply was not sent: %v", err)
	}
}

func TestReplyDoesNotRetryAmbiguousHelixFailure(t *testing.T) {
	if canFallbackToIRC(context.DeadlineExceeded, true) {
		t.Fatal("a timed-out reply could already be visible in Twitch")
	}
	if canFallbackToIRC(chatMessageHTTPError(http.StatusBadGateway), true) {
		t.Fatal("a gateway could fail after Twitch accepted the reply")
	}
	if !canFallbackToIRC(chatMessageHTTPError(http.StatusForbidden), true) {
		t.Fatal("an explicitly unauthorized request never sent the reply")
	}
}

func TestOwnTranslationEchoWithTwitchMention(t *testing.T) {
	runtime := &Runtime{}
	if !runtime.IsOwnText("@grep_ban Перевод сообщения: привет") {
		t.Fatal("Twitch reply echo would be translated again")
	}
}

func TestReplyClaimIsSharedAcrossWorkers(t *testing.T) {
	dsn := os.Getenv("STREAMHATA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires a disposable PostgreSQL database with migrations applied")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	channelID := strconv.FormatInt(time.Now().UnixNano(), 10)
	const parentID = "parent-message"
	results := make(chan error, 2)
	claimed := make(chan bool, 2)
	for range 2 {
		go func() {
			ok, err := claimTranslationReply(ctx, db, channelID, parentID)
			claimed <- ok
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	first, second := <-claimed, <-claimed
	if first == second {
		t.Fatal("exactly one worker should be allowed to reply")
	}
	if ok, err := claimTranslationReply(ctx, db, channelID+"-other", parentID); err != nil || !ok {
		t.Fatalf("another channel's reply was blocked: %v", err)
	}
}

func TestCheckChatMessageSent(t *testing.T) {
	sent := strings.NewReader(`{"data":[{"is_sent":true,"drop_reason":null}]}`)
	if err := checkChatMessageSent(sent); err != nil {
		t.Fatalf("sent message: %v", err)
	}

	dropped := strings.NewReader(`{"data":[{"is_sent":false,"drop_reason":{"code":"automod_held","message":"Held for review"}}]}`)
	var rejected *chatMessageRejected
	if err := checkChatMessageSent(dropped); !errors.As(err, &rejected) ||
		rejected.Code != "automod_held" || rejected.Message != "Held for review" {
		t.Fatalf("expected Twitch drop reason, got %v", err)
	}
}
