package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type lokiEntry struct {
	Timestamp string `json:"ts"`
	ChatEvent
}

type lokiPush struct {
	Streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	} `json:"streams"`
}

// LokiLogger buffers chat events and ships them directly to Loki, bypassing journald.
type LokiLogger struct {
	url     string
	token   string
	client  *http.Client
	queue   chan lokiEntry
	dropped atomic.Uint64
	failed  atomic.Uint64
}

func NewLokiLogger() *LokiLogger {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("LOKI_URL")), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:3100"
	}
	logger := &LokiLogger{
		url:    baseURL + "/loki/api/v1/push",
		token:  strings.TrimSpace(os.Getenv("LOKI_BEARER_TOKEN")),
		client: &http.Client{Timeout: 4 * time.Second},
		queue:  make(chan lokiEntry, 2048),
	}
	go logger.run()
	return logger
}

func (logger *LokiLogger) Observe(event ChatEvent) {
	entry := lokiEntry{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), ChatEvent: event}
	select {
	case logger.queue <- entry:
	default:
		logger.dropped.Add(1)
	}
}

func (logger *LokiLogger) Metrics() (dropped, failed uint64) {
	return logger.dropped.Load(), logger.failed.Load()
}

func (logger *LokiLogger) run() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]lokiEntry, 0, 128)
	for {
		select {
		case entry := <-logger.queue:
			batch = append(batch, entry)
			if len(batch) >= 128 {
				logger.push(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				logger.push(batch)
				batch = batch[:0]
			}
		}
	}
}

func (logger *LokiLogger) push(entries []lokiEntry) {
	var payload lokiPush
	stream := map[string]string{"job": "streamhata-chat", "service": "backend", "host": "streamhata-main"}
	values := make([][2]string, 0, len(entries))
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			logger.failed.Add(1)
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			timestamp = time.Now().UTC()
		}
		values = append(values, [2]string{strconv.FormatInt(timestamp.UnixNano(), 10), string(line)})
	}
	if len(values) == 0 {
		return
	}
	payload.Streams = append(payload.Streams, struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}{Stream: stream, Values: values})
	body, err := json.Marshal(payload)
	if err != nil {
		logger.failed.Add(1)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, logger.url, bytes.NewReader(body))
	if err != nil {
		logger.failed.Add(1)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	if logger.token != "" {
		request.Header.Set("Authorization", "Bearer "+logger.token)
	}
	response, err := logger.client.Do(request)
	if err != nil {
		logger.failed.Add(1)
		return
	}
	_ = response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		logger.failed.Add(1)
	}
}
