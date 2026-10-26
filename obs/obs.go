// Package obs — клиент OBS WebSocket 5: сцены, mute, стрим/запись, реплей.
package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	opHello           = 0
	opIdentify        = 1
	opIdentified      = 2
	opEvent           = 5
	opRequest         = 6
	opRequestResponse = 7
	requestTimeout    = 10 * time.Second
)

// Status — состояние подключения и снимок OBS.
type Status struct {
	Connected    bool     `json:"connected"`
	URL          string   `json:"url"`
	Scenes       []string `json:"scenes"`
	CurrentScene string   `json:"currentScene"`
	Inputs       []string `json:"inputs"`
	Streaming    bool     `json:"streaming"`
	Recording    bool     `json:"recording"`
	Error        string   `json:"error,omitempty"`
}

type helloPayload struct {
	RPCVersion     int `json:"rpcVersion"`
	Authentication *struct {
		Challenge string `json:"challenge"`
		Salt      string `json:"salt"`
	} `json:"authentication"`
}

type pendingResponse struct {
	channel chan map[string]any
}

// Client — одно соединение с OBS и очередь запросов.
type Client struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	url    string
	status Status

	pending map[string]pendingResponse
	nextID  int

	onEvent func(eventType string, data map[string]any)
}

func New() *Client {
	return &Client{
		pending: make(map[string]pendingResponse),
	}
}

// SetOnEvent — колбэк на события OBS (смена сцены, статус стрима).
func (c *Client) SetOnEvent(hook func(eventType string, data map[string]any)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onEvent = hook
}

func (c *Client) GetStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.status
}

// Connect подключается к OBS WebSocket и делает первичный снимок состояния.
func (c *Client) Connect(ctx context.Context, url, password string) error {
	c.Disconnect()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: netcfg.ProxyFunc()}

	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		c.setError(err)

		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.url = url
	c.status = Status{Connected: false, URL: url}
	c.mu.Unlock()

	// Hello
	_, payload, err := conn.ReadMessage()
	if err != nil {
		c.Disconnect()

		return err
	}

	var hello struct {
		Op int          `json:"op"`
		D  helloPayload `json:"d"`
	}

	if err := json.Unmarshal(payload, &hello); err != nil || hello.Op != opHello {
		c.Disconnect()

		return errors.New("obs: unexpected hello")
	}

	identify := map[string]any{
		"rpcVersion": 1,
	}

	if hello.D.Authentication != nil {
		identify["authentication"] = authString(
			password,
			hello.D.Authentication.Salt,
			hello.D.Authentication.Challenge,
		)
	}

	if err := conn.WriteJSON(map[string]any{
		"op": opIdentify,
		"d":  identify,
	}); err != nil {
		c.Disconnect()

		return err
	}

	// Identified
	_, payload, err = conn.ReadMessage()
	if err != nil {
		c.Disconnect()

		return err
	}

	var identified struct {
		Op int `json:"op"`
	}

	if err := json.Unmarshal(payload, &identified); err != nil || identified.Op != opIdentified {
		c.Disconnect()

		return errors.New("obs: not identified (проверьте пароль)")
	}

	c.mu.Lock()
	c.status.Connected = true
	c.mu.Unlock()

	go c.readLoop(conn)

	return c.refresh(ctx)
}

func (c *Client) Disconnect() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.status = Status{URL: c.url}

	for id, pending := range c.pending {
		close(pending.channel)
		delete(c.pending, id)
	}

	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Client) setError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.status.Error = err.Error()
}

func authString(password, salt, challenge string) string {
	secret := sha256.Sum256([]byte(password + salt))
	secretB64 := base64.StdEncoding.EncodeToString(secret[:])

	hash := sha256.Sum256([]byte(secretB64 + challenge))

	return base64.StdEncoding.EncodeToString(hash[:])
}

func (c *Client) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()

			if c.conn == conn {
				c.conn = nil
				c.status.Connected = false
			}

			for id, pending := range c.pending {
				close(pending.channel)
				delete(c.pending, id)
			}

			c.mu.Unlock()

			return
		}

		var message struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
		}

		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}

		switch message.Op {
		case opRequestResponse:
			var response struct {
				RequestID string         `json:"requestId"`
				Data      map[string]any `json:"responseData"`
			}

			if err := json.Unmarshal(message.D, &response); err != nil {
				continue
			}

			c.mu.Lock()
			pending, ok := c.pending[response.RequestID]

			if ok {
				delete(c.pending, response.RequestID)
			}

			c.mu.Unlock()

			if ok {
				pending.channel <- response.Data
				close(pending.channel)
			}
		case opEvent:
			var event struct {
				EventType string         `json:"eventType"`
				Data      map[string]any `json:"eventData"`
			}

			if err := json.Unmarshal(message.D, &event); err != nil {
				continue
			}

			c.applyEvent(event.EventType, event.Data)

			c.mu.Lock()
			hook := c.onEvent
			c.mu.Unlock()

			if hook != nil {
				hook(event.EventType, event.Data)
			}
		}
	}
}

func (c *Client) applyEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch eventType {
	case "CurrentProgramSceneChanged":
		if name, ok := data["sceneName"].(string); ok {
			c.status.CurrentScene = name
		}
	case "StreamStateChanged":
		if active, ok := data["outputActive"].(bool); ok {
			c.status.Streaming = active
		}
	case "RecordStateChanged":
		if active, ok := data["outputActive"].(bool); ok {
			c.status.Recording = active
		}
	}
}

// request отправляет запрос и ждёт ответ.
func (c *Client) request(ctx context.Context, requestType string, data map[string]any) (map[string]any, error) {
	c.mu.Lock()
	conn := c.conn

	if conn == nil {
		c.mu.Unlock()

		return nil, errors.New("obs: not connected")
	}

	c.nextID++
	requestID := fmt.Sprintf("req-%d", c.nextID)
	channel := make(chan map[string]any, 1)
	c.pending[requestID] = pendingResponse{channel: channel}
	c.mu.Unlock()

	payload := map[string]any{
		"op": opRequest,
		"d": map[string]any{
			"requestType": requestType,
			"requestId":   requestID,
			"requestData": data,
		},
	}

	if err := conn.WriteJSON(payload); err != nil {
		return nil, err
	}

	select {
	case response, ok := <-channel:
		if !ok {
			return nil, errors.New("obs: connection closed")
		}

		return response, nil
	case <-time.After(requestTimeout):
		return nil, errors.New("obs: request timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// refresh обновляет сцены, источники и статус выходов.
func (c *Client) refresh(ctx context.Context) error {
	if scenes, err := c.request(ctx, "GetSceneList", nil); err == nil {
		names := make([]string, 0)

		if items, ok := scenes["scenes"].([]any); ok {
			for _, item := range items {
				if scene, ok := item.(map[string]any); ok {
					if name, ok := scene["sceneName"].(string); ok {
						names = append(names, name)
					}
				}
			}
		}

		current, _ := scenes["currentProgramSceneName"].(string)

		c.mu.Lock()
		c.status.Scenes = names
		c.status.CurrentScene = current
		c.mu.Unlock()
	}

	if inputs, err := c.request(ctx, "GetInputList", nil); err == nil {
		names := make([]string, 0)

		if items, ok := inputs["inputs"].([]any); ok {
			for _, item := range items {
				if input, ok := item.(map[string]any); ok {
					if name, ok := input["inputName"].(string); ok {
						names = append(names, name)
					}
				}
			}
		}

		c.mu.Lock()
		c.status.Inputs = names
		c.mu.Unlock()
	}

	if status, err := c.request(ctx, "GetStreamStatus", nil); err == nil {
		if active, ok := status["outputActive"].(bool); ok {
			c.mu.Lock()
			c.status.Streaming = active
			c.mu.Unlock()
		}
	}

	return nil
}

/* ── Действия ─────────────────────────────────────────────────────── */

func (c *Client) SetScene(ctx context.Context, sceneName string) error {
	_, err := c.request(ctx, "SetCurrentProgramScene", map[string]any{"sceneName": sceneName})

	if err == nil {
		c.mu.Lock()
		c.status.CurrentScene = sceneName
		c.mu.Unlock()
	}

	return err
}

func (c *Client) ToggleMute(ctx context.Context, inputName string) (bool, error) {
	response, err := c.request(ctx, "ToggleInputMute", map[string]any{"inputName": inputName})
	if err != nil {
		return false, err
	}

	muted, _ := response["inputMuted"].(bool)

	return muted, nil
}

// OutputAction управляет выходами: stream/record/replay («start», «stop», «save»).
func (c *Client) OutputAction(ctx context.Context, output, action string) error {
	requestType := ""

	switch output {
	case "stream":
		requestType = map[string]string{"start": "StartStream", "stop": "StopStream"}[action]
	case "record":
		requestType = map[string]string{"start": "StartRecord", "stop": "StopRecord"}[action]
	case "replay":
		requestType = map[string]string{
			"start": "StartReplayBuffer",
			"stop":  "StopReplayBuffer",
			"save":  "SaveReplayBuffer",
		}[action]
	}

	if requestType == "" {
		return errors.New("obs: неизвестное действие")
	}

	_, err := c.request(ctx, requestType, nil)

	return err
}
