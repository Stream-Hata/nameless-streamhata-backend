package donations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	donationAlertsAPI      = "https://www.donationalerts.com/api/v1"
	donationAlertsOAuth    = "https://www.donationalerts.com/oauth"
	donationAlertsCentrif  = "wss://centrifugo.donationalerts.com/connection/websocket"
	donationAlertsScope    = "oauth-user-show oauth-donation-subscribe oauth-goal-subscribe"
	donationAlertsReadWait = 60 * time.Second
)

// DonationAlertsConfig — параметры OAuth-приложения DonationAlerts.
type DonationAlertsConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string

	AccessToken  string
	RefreshToken string
	OnTokens     func(access, refresh string)
	OnGoal       func(Goal)
}

type donationAlertsTokens struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type donationAlertsUser struct {
	Data struct {
		ID                  int    `json:"id"`
		Code                string `json:"code"`
		Name                string `json:"name"`
		SocketConnectionTkn string `json:"socket_connection_token"`
	} `json:"data"`
}

type donationAlertsSubscription struct {
	Channel string `json:"channel"`
	Token   string `json:"token"`
}

// DonationAlerts — провайдер на базе OAuth + Centrifugo WebSocket.
type DonationAlerts struct {
	cfg DonationAlertsConfig

	http *http.Client

	mu      sync.Mutex
	tokens  donationAlertsTokens
	state   ProviderStatus
	conn    *websocket.Conn
	cancel  context.CancelFunc
	running bool
	pending map[string]time.Time
}

func NewDonationAlerts(cfg DonationAlertsConfig) *DonationAlerts {
	provider := &DonationAlerts{
		cfg:     cfg,
		http:    netcfg.NewHTTPClient(20 * time.Second),
		pending: make(map[string]time.Time),
	}

	provider.tokens.AccessToken = cfg.AccessToken
	provider.tokens.RefreshToken = cfg.RefreshToken

	provider.state = ProviderStatus{
		Name:  "donationalerts",
		State: "disconnected",
	}

	return provider
}

func (d *DonationAlerts) Name() string {
	return "donationalerts"
}

func (d *DonationAlerts) Status() ProviderStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.state
}

func (d *DonationAlerts) setState(state, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state = ProviderStatus{Name: d.Name(), State: state, Detail: detail}
}

// AuthURL собирает ссылку авторизации DonationAlerts.
func (d *DonationAlerts) AuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", d.cfg.ClientID)
	params.Set("redirect_uri", d.cfg.RedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", donationAlertsScope)
	params.Set("state", state)

	return donationAlertsOAuth + "/authorize?" + params.Encode()
}

// BeginAuth создаёт state и возвращает ссылку авторизации.
func (d *DonationAlerts) BeginAuth() (string, string, error) {
	if d.cfg.ClientID == "" || d.cfg.ClientSecret == "" {
		return "", "", errors.New("DonationAlerts не настроен: добавьте DONATIONALERTS_CLIENT_ID и DONATIONALERTS_CLIENT_SECRET в .env")
	}

	state, err := GenerateState()
	if err != nil {
		return "", "", err
	}

	d.mu.Lock()
	d.pruneStatesLocked()
	d.pending[state] = time.Now()
	d.mu.Unlock()

	return state, d.AuthURL(state), nil
}

// CompleteAuth проверяет state и обменивает code на токены.
func (d *DonationAlerts) CompleteAuth(ctx context.Context, state, code string) error {
	d.mu.Lock()
	issuedAt, ok := d.pending[state]

	if ok {
		delete(d.pending, state)
	}

	d.pruneStatesLocked()
	d.mu.Unlock()

	if !ok {
		return errors.New("donationalerts: unknown or expired state")
	}

	if time.Since(issuedAt) > 10*time.Minute {
		return errors.New("donationalerts: state expired")
	}

	return d.ExchangeCode(ctx, code)
}

func (d *DonationAlerts) pruneStatesLocked() {
	for state, issuedAt := range d.pending {
		if time.Since(issuedAt) > 10*time.Minute {
			delete(d.pending, state)
		}
	}
}

// GenerateState — случайный OAuth state.
func GenerateState() (string, error) {
	buf := make([]byte, 32)

	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
}

// ExchangeCode обменивает authorization code на токены.
func (d *DonationAlerts) ExchangeCode(ctx context.Context, code string) error {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", d.cfg.ClientID)
	form.Set("client_secret", d.cfg.ClientSecret)
	form.Set("redirect_uri", d.cfg.RedirectURI)
	form.Set("code", code)

	return d.requestTokens(ctx, form)
}

func (d *DonationAlerts) refreshTokens(ctx context.Context) error {
	if d.tokens.RefreshToken == "" {
		return errors.New("donationalerts: refresh token is empty")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", d.tokens.RefreshToken)
	form.Set("client_id", d.cfg.ClientID)
	form.Set("client_secret", d.cfg.ClientSecret)
	form.Set("scope", donationAlertsScope)

	return d.requestTokens(ctx, form)
}

func (d *DonationAlerts) requestTokens(ctx context.Context, form url.Values) error {
	tokenURL := donationAlertsOAuth + "/token"

	if grant := form.Get("grant_type"); grant != "" {
		tokenURL += "?grant_type=" + url.QueryEscape(grant)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

		return fmt.Errorf("donationalerts: token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokens donationAlertsTokens

	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return err
	}

	if tokens.RefreshToken == "" {
		tokens.RefreshToken = d.tokens.RefreshToken
	}

	d.mu.Lock()
	d.tokens = tokens
	d.mu.Unlock()

	if d.cfg.OnTokens != nil {
		d.cfg.OnTokens(tokens.AccessToken, tokens.RefreshToken)
	}

	return nil
}

// Start подключается к Centrifugo и слушает донат-канал пользователя.
func (d *DonationAlerts) Start(ctx context.Context, sink Sink) error {
	if d.cfg.ClientID == "" && d.tokens.AccessToken == "" {
		d.setState("disconnected", "не настроен: нет client id/secret или access token")

		return errors.New("donationalerts: client id/secret or access token are not configured")
	}

	d.mu.Lock()
	running := d.running
	d.mu.Unlock()

	if running {
		return nil
	}

	if d.tokens.AccessToken == "" && d.tokens.RefreshToken != "" {
		if err := d.refreshTokens(ctx); err != nil {
			d.setState("error", err.Error())

			return err
		}
	}

	if d.tokens.AccessToken == "" {
		d.setState("disconnected", "нужна авторизация")

		return errors.New("donationalerts: no access token")
	}

	profile, err := d.fetchUser(ctx)
	if err != nil {
		d.setState("error", err.Error())

		return err
	}

	channel := fmt.Sprintf("$alerts:donation_%d", profile.Data.ID)

	// Периодический refresh токена, пока провайдер запущен.
	runCtx, cancel := context.WithCancel(ctx)

	d.mu.Lock()
	d.running = true
	d.cancel = cancel
	d.mu.Unlock()

	go d.refreshLoop(runCtx)

	go d.listen(runCtx, channel, profile.Data.SocketConnectionTkn, func(raw []byte) {
		for _, event := range parseCentrifugoPush(raw) {
			sink(event)
		}
	})

	if d.cfg.OnGoal != nil {
		goalChannel := fmt.Sprintf("$goals:goal_%d", profile.Data.ID)
		go d.listen(runCtx, goalChannel, profile.Data.SocketConnectionTkn, func(raw []byte) {
			if goal, ok := parseGoalPush(raw); ok {
				d.cfg.OnGoal(goal)
			}
		})
	}

	d.setState("connected", "канал "+profile.Data.Name)

	return nil
}

func (d *DonationAlerts) Stop() error {
	d.mu.Lock()
	cancel := d.cancel
	conn := d.conn
	d.running = false
	d.cancel = nil
	d.conn = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if conn != nil {
		_ = conn.Close()
	}

	d.setState("disconnected", "остановлен")

	return nil
}

func (d *DonationAlerts) fetchUser(ctx context.Context) (*donationAlertsUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, donationAlertsAPI+"/user/oauth", nil)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	token := d.tokens.AccessToken
	d.mu.Unlock()

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("donationalerts: user endpoint returned %d", resp.StatusCode)
	}

	var user donationAlertsUser

	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

func (d *DonationAlerts) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(90 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.refreshTokens(ctx); err != nil {
				d.setState("error", "refresh: "+err.Error())
			}
		}
	}
}

func (d *DonationAlerts) listen(ctx context.Context, channel, connectionToken string, handler func([]byte)) {
	for {
		if ctx.Err() != nil {
			return
		}

		err := d.listenCycle(ctx, channel, connectionToken, handler)
		if ctx.Err() != nil {
			return
		}

		if err != nil && !isNetworkTimeout(err) {
			d.setState("error", "reconnect: "+err.Error())
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func isNetworkTimeout(err error) bool {
	var networkError net.Error

	return errors.As(err, &networkError) && networkError.Timeout()
}

func (d *DonationAlerts) listenCycle(
	ctx context.Context,
	channel string,
	connectionToken string,
	handler func([]byte),
) error {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: netcfg.ProxyFunc()}

	conn, _, err := dialer.DialContext(ctx, donationAlertsCentrif, nil)
	if err != nil {
		return err
	}

	defer func() {
		_ = conn.Close()
	}()

	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()

	if err := conn.WriteJSON(map[string]any{
		"id":     1,
		"params": map[string]any{"token": connectionToken},
	}); err != nil {
		return err
	}

	_, payload, err := conn.ReadMessage()
	if err != nil {
		return err
	}

	clientID := extractString(payload, "client")

	if clientID == "" {
		return errors.New("centrifugo: no client id in welcome")
	}

	channelToken, err := d.subscribeChannel(ctx, channel, clientID)
	if err != nil {
		return err
	}

	if err := conn.WriteJSON(map[string]any{
		"id":     2,
		"method": 1,
		"params": map[string]any{"channel": channel, "token": channelToken},
	}); err != nil {
		return err
	}

	d.setState("connected", "канал активен")

	pingDone := make(chan struct{})

	defer close(pingDone)

	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				if err := conn.WriteJSON(map[string]any{}); err != nil {
					return
				}
			}
		}
	}()

	for {
		if err := conn.SetReadDeadline(time.Now().Add(donationAlertsReadWait)); err != nil {
			return err
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		handler(raw)
	}
}

func (d *DonationAlerts) subscribeChannel(
	ctx context.Context,
	channel string,
	clientID string,
) (string, error) {
	body, err := json.Marshal(map[string]any{
		"channels": []string{channel},
		"client":   clientID,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		donationAlertsAPI+"/centrifuge/subscribe",
		strings.NewReader(string(body)),
	)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	token := d.tokens.AccessToken
	d.mu.Unlock()

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("donationalerts: subscribe returned %d", resp.StatusCode)
	}

	var data struct {
		Channels []donationAlertsSubscription `json:"channels"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	if len(data.Channels) == 0 {
		return "", errors.New("donationalerts: empty subscription list")
	}

	return data.Channels[0].Token, nil
}

// parseCentrifugoPush вытаскивает донат-события из push-сообщения Centrifugo.
func parseCentrifugoPush(raw []byte) []Event {
	var message map[string]any

	if err := json.Unmarshal(raw, &message); err != nil {
		return nil
	}

	result, ok := message["result"].(map[string]any)
	if !ok {
		return nil
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		return nil
	}

	candidates := []map[string]any{data}

	if nested, ok := data["data"].(map[string]any); ok {
		candidates = append(candidates, nested)
	}

	channel, _ := result["channel"].(string)

	var events []Event

	for _, candidate := range candidates {
		event, ok := normalizeMap(candidate)
		if !ok {
			continue
		}

		event.Provider = "donationalerts"

		if event.Currency == "" {
			_ = channel
		}

		events = append(events, event)

		break
	}

	return events
}

func extractString(raw []byte, key string) string {
	var message map[string]any

	if err := json.Unmarshal(raw, &message); err != nil {
		return ""
	}

	result, ok := message["result"].(map[string]any)
	if !ok {
		return ""
	}

	value, _ := result[key].(string)

	return value
}

func parseGoalPush(raw []byte) (Goal, bool) {
	var message map[string]any

	if json.Unmarshal(raw, &message) != nil {
		return Goal{}, false
	}

	result, ok := message["result"].(map[string]any)
	if !ok {
		return Goal{}, false
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		return Goal{}, false
	}

	if nested, ok := data["data"].(map[string]any); ok {
		data = nested
	}

	id := stringValue(data["id"])
	if id == "" {
		return Goal{}, false
	}

	active := true

	switch value := data["is_active"].(type) {
	case bool:
		active = value
	case float64:
		active = value != 0
	case string:
		active = value != "0" && value != "false"
	}

	return Goal{
		ID:           id,
		Provider:     "donationalerts",
		Title:        stringValue(data["title"]),
		Currency:     stringValue(data["currency"]),
		StartAmount:  floatValue(data["start_amount"]),
		RaisedAmount: floatValue(data["raised_amount"]),
		GoalAmount:   floatValue(data["goal_amount"]),
		Active:       active,
		UpdatedAt:    time.Now().UTC(),
	}, true
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return ""
	}
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		var number float64
		_, _ = fmt.Sscanf(typed, "%f", &number)

		return number
	default:
		return 0
	}
}
