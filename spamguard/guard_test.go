package spamguard

import (
	"testing"
	"time"
)

func TestDMRateLimitAndRecovery(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	guard := New(Config{
		DMWindow:    time.Minute,
		DMLimit:     2,
		DMPeerLimit: 10,
	})
	guard.now = func() time.Time { return now }

	if decision := guard.CheckDM(1, 2); !decision.Allowed {
		t.Fatalf("first message must be allowed: %+v", decision)
	}
	if decision := guard.CheckDM(1, 3); !decision.Allowed {
		t.Fatalf("second message must be allowed: %+v", decision)
	}
	decision := guard.CheckDM(1, 2)
	if decision.Allowed || decision.RetryAfter != time.Minute {
		t.Fatalf("third message must be limited, got %+v", decision)
	}

	now = now.Add(time.Minute)
	if decision := guard.CheckDM(1, 2); !decision.Allowed {
		t.Fatalf("message must be allowed after the window: %+v", decision)
	}
}

func TestDMPeerLimit(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	guard := New(Config{
		DMWindow:    time.Minute,
		DMLimit:     10,
		DMPeerLimit: 1,
	})
	guard.now = func() time.Time { return now }

	if decision := guard.CheckDM(1, 2); !decision.Allowed {
		t.Fatalf("first peer message must be allowed: %+v", decision)
	}
	if decision := guard.CheckDM(1, 2); decision.Allowed || decision.Reason != "rate limit" {
		t.Fatalf("second peer message must be limited, got %+v", decision)
	}
	if decision := guard.CheckDM(1, 3); !decision.Allowed {
		t.Fatalf("message to another peer must be allowed: %+v", decision)
	}
}

func TestDiscussionContentChecks(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	guard := New(Config{
		DiscussionWindow: time.Minute,
		DiscussionLimit:  10,
		DuplicateWindow:  time.Minute,
		MaxLinks:         2,
		MaxMentions:      2,
	})
	guard.now = func() time.Time { return now }

	if decision := guard.CheckDiscussion(1, "hello"); !decision.Allowed {
		t.Fatalf("normal message must be allowed: %+v", decision)
	}
	if decision := guard.CheckDiscussion(1, "hello"); decision.Allowed || decision.Reason != "duplicate message" {
		t.Fatalf("duplicate must be blocked, got %+v", decision)
	}
	if decision := guard.CheckDiscussion(2, "https://a.example https://b.example https://c.example"); decision.Allowed || decision.Reason != "too many links" {
		t.Fatalf("link flood must be blocked, got %+v", decision)
	}
	if decision := guard.CheckDiscussion(3, "@a @b @c"); decision.Allowed || decision.Reason != "too many mentions" {
		t.Fatalf("mention flood must be blocked, got %+v", decision)
	}

	now = now.Add(time.Minute)
	if decision := guard.CheckDiscussion(1, "hello"); !decision.Allowed {
		t.Fatalf("duplicate must expire: %+v", decision)
	}
}

func TestUploadLimit(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	guard := New(Config{
		UploadWindow: time.Minute,
		UploadLimit:  1,
	})
	guard.now = func() time.Time { return now }

	if decision := guard.CheckUpload(1); !decision.Allowed {
		t.Fatalf("first upload must be allowed: %+v", decision)
	}
	if decision := guard.CheckUpload(1); decision.Allowed || decision.RetryAfterSeconds() != 60 {
		t.Fatalf("second upload must be limited, got %+v", decision)
	}
}
