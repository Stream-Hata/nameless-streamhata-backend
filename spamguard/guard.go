package spamguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	DMWindow         time.Duration
	DMLimit          int
	DMPeerLimit      int
	DiscussionWindow time.Duration
	DiscussionLimit  int
	UploadWindow     time.Duration
	UploadLimit      int
	DuplicateWindow  time.Duration
	MaxLinks         int
	MaxMentions      int
}

type Decision struct {
	Allowed    bool
	Reason     string
	RetryAfter time.Duration
}

func (d Decision) RetryAfterSeconds() int {
	if d.RetryAfter <= 0 {
		return 0
	}

	return int((d.RetryAfter + time.Second - 1) / time.Second)
}

type Guard struct {
	cfg    Config
	mu     sync.Mutex
	events map[string][]time.Time
	recent map[string]time.Time
	now    func() time.Time
}

func New(cfg Config) *Guard {
	return &Guard{
		cfg:    cfg,
		events: make(map[string][]time.Time),
		recent: make(map[string]time.Time),
		now:    time.Now,
	}
}

func NewFromEnv() *Guard {
	return New(Config{
		DMWindow:         time.Minute,
		DMLimit:          envInt("SPAM_DM_PER_MINUTE", 20),
		DMPeerLimit:      envInt("SPAM_DM_PEER_PER_MINUTE", 10),
		DiscussionWindow: time.Minute,
		DiscussionLimit:  envInt("SPAM_DISCUSSION_PER_MINUTE", 10),
		UploadWindow:     time.Minute,
		UploadLimit:      envInt("SPAM_UPLOAD_PER_MINUTE", 10),
		DuplicateWindow:  time.Duration(envInt("SPAM_DUPLICATE_WINDOW_SECONDS", 300)) * time.Second,
		MaxLinks:         envInt("SPAM_MAX_LINKS", 3),
		MaxMentions:      envInt("SPAM_MAX_MENTIONS", 10),
	})
}

func (g *Guard) CheckDM(userID, peerID int64) Decision {
	if g == nil {
		return Decision{Allowed: true}
	}
	if decision := g.allow(fmt.Sprintf("dm-peer:%d:%d", userID, peerID), g.cfg.DMPeerLimit, g.cfg.DMWindow); !decision.Allowed {
		return decision
	}

	return g.allow(fmt.Sprintf("dm:%d", userID), g.cfg.DMLimit, g.cfg.DMWindow)
}

func (g *Guard) CheckUpload(userID int64) Decision {
	if g == nil {
		return Decision{Allowed: true}
	}

	return g.allow(fmt.Sprintf("upload:%d", userID), g.cfg.UploadLimit, g.cfg.UploadWindow)
}

func (g *Guard) CheckDiscussion(userID int64, content string) Decision {
	if g == nil {
		return Decision{Allowed: true}
	}
	if decision := g.allow(fmt.Sprintf("discussion:%d", userID), g.cfg.DiscussionLimit, g.cfg.DiscussionWindow); !decision.Allowed {
		return decision
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return Decision{Allowed: true}
	}
	linkCount := 0
	for _, field := range strings.Fields(content) {
		candidate := strings.Trim(field, "()[]{}<>,.!?;:\"'")
		if strings.HasPrefix(strings.ToLower(candidate), "www.") {
			linkCount++

			continue
		}
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			linkCount++
		}
	}
	if g.cfg.MaxLinks > 0 && linkCount > g.cfg.MaxLinks {
		return Decision{Reason: "too many links"}
	}
	if g.cfg.MaxMentions > 0 && strings.Count(content, "@") > g.cfg.MaxMentions {
		return Decision{Reason: "too many mentions"}
	}

	return g.checkDuplicate(userID, content)
}

func (g *Guard) allow(key string, limit int, window time.Duration) Decision {
	if limit <= 0 || window <= 0 {
		return Decision{Allowed: true}
	}

	now := g.now()
	cutoff := now.Add(-window)
	g.mu.Lock()
	defer g.mu.Unlock()

	items := g.events[key]
	kept := items[:0]
	for _, timestamp := range items {
		if timestamp.After(cutoff) {
			kept = append(kept, timestamp)
		}
	}
	if len(kept) >= limit {
		g.events[key] = kept

		return Decision{Reason: "rate limit", RetryAfter: kept[0].Add(window).Sub(now)}
	}
	g.events[key] = append(kept, now)
	if len(g.events) > 10000 {
		g.prune(now)
	}

	return Decision{Allowed: true}
}

func (g *Guard) checkDuplicate(userID int64, content string) Decision {
	if g.cfg.DuplicateWindow <= 0 {
		return Decision{Allowed: true}
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	sum := sha256.Sum256([]byte(normalized))
	key := fmt.Sprintf("duplicate:%d:%s", userID, hex.EncodeToString(sum[:]))
	now := g.now()
	cutoff := now.Add(-g.cfg.DuplicateWindow)

	g.mu.Lock()
	defer g.mu.Unlock()

	if timestamp, ok := g.recent[key]; ok && timestamp.After(cutoff) {
		return Decision{Reason: "duplicate message", RetryAfter: timestamp.Add(g.cfg.DuplicateWindow).Sub(now)}
	}
	g.recent[key] = now
	if len(g.recent) > 10000 {
		for item, timestamp := range g.recent {
			if !timestamp.After(cutoff) {
				delete(g.recent, item)
			}
		}
	}

	return Decision{Allowed: true}
}

func (g *Guard) prune(cutoff time.Time) {
	for key, items := range g.events {
		if len(items) == 0 || !items[len(items)-1].After(cutoff) {
			delete(g.events, key)
		}
	}
	for key, timestamp := range g.recent {
		if !timestamp.After(cutoff) {
			delete(g.recent, key)
		}
	}
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}

	return value
}
