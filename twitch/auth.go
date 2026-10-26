package twitch

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
	"github.com/p7x-dev/nameless-streamhata-backend/server"
	"golang.org/x/sync/errgroup"
)

func SendRequest[T any](url string, ctx context.Context, method, token string) (*T, error) {
	var data T
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Client-Id", Env["TwitchClientKey"])

	if err != nil {
		return nil, err
	}

	resp, err := netcfg.NewHTTPClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = resp.Body.Close()
	}()

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	return &data, nil
}

func generateState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func envAny(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}

	return ""
}

var Env = map[string]string{
	"TwitchBaseURL":      envAny("TWITCH_ID_URL", "TWITCH_BASE_URL"),
	"TwitchClientKey":    envAny("TWITCH_CLIENT_KEY", "TWITCH_CLIENT_ID"),
	"TwitchClientSecret": envAny("TWITCH_CLIENT_SECRET", "TWITCH_SECRET_KEY"),
	"TwitchAppURL":       os.Getenv("TWITCH_APP_URL"),
}

func TwitchAuth(srv server.Server, userRepository data_base.Database[data_base.User], tokenRepo data_base.Database[data_base.Token]) {
	type cachedValidation struct {
		response ValidationResponse
		expires  time.Time
	}

	validationCache := struct {
		sync.Mutex
		items map[string]cachedValidation
	}{items: make(map[string]cachedValidation)}

	writeValidation := func(w http.ResponseWriter, response ValidationResponse) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(response)
	}

	authorizeHandler := func(w http.ResponseWriter, r *http.Request) {
		state, err := generateState()
		if err != nil {
		}

		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		params := url.Values{}
		params.Set("state", state)
		params.Set("client_id", Env["TwitchClientKey"])
		params.Set("redirect_uri", Env["TwitchAppURL"])
		params.Set("response_type", "code")
		params.Set("scope", "moderator:read:followers "+
			"moderator:read:chatters "+
			"moderator:manage:chat_messages "+
			"moderator:manage:banned_users "+
			"channel:read:subscriptions "+
			"channel:read:hype_train "+
			"channel:read:goals "+
			"channel:read:redemptions "+
			"channel:manage:redemptions "+
			"channel:read:polls "+
			"channel:manage:polls "+
			"channel:read:predictions "+
			"channel:manage:broadcast "+
			"moderator:read:shield_mode "+
			"moderator:manage:shield_mode "+
			"bits:read "+
			"clips:edit "+
			"user:read:chat")

		http.SetCookie(w, &http.Cookie{
			Name:     "e-hub-state",
			Value:    state,
			Path:     "/",
			Domain:   cookieDomain(),
			SameSite: http.SameSiteLaxMode,
		})

		if returnTo := r.URL.Query().Get("return"); validReturnURL(returnTo) {
			http.SetCookie(w, &http.Cookie{
				Name:     "e-hub-return",
				Value:    returnTo,
				Path:     "/",
				Domain:   cookieDomain(),
				SameSite: http.SameSiteLaxMode,
			})
		}

		url := Env["TwitchBaseURL"] + "?" + params.Encode()
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	}

	srv.ServeEndpoint("/twitch", authorizeHandler)
	srv.ServeEndpoint("/twitch/authorize", authorizeHandler)
	srv.ServeEndpoint("/rgg-sync/twitch/authorize", authorizeHandler)

	srv.ServeEndpoint("/twitch/callback", func(w http.ResponseWriter, r *http.Request) {
		req := r.URL.Query()
		state, code := req.Get("state"), req.Get("code")
		cookies := r.CookiesNamed("e-hub-state")

		if len(cookies) == 0 || code == "" {
			http.Error(w, "Invalid OAuth", http.StatusBadRequest)

			return
		}

		savedState := cookies[0].Value

		if savedState == "" || savedState != state {
			http.Error(w, "Invalid OAuth", http.StatusBadRequest)

			return
		}

		params := buildParams("authorization_code")
		params.Set("code", code)
		params.Set("state", state)

		tokenReq, err := http.NewRequestWithContext(r.Context(), "POST", twitchTokenURL(), strings.NewReader(params.Encode()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tokenReq.Header.Set("Accept", "application/json")

		tokenResp, err := netcfg.NewHTTPClient(20 * time.Second).Do(tokenReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if tokenResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(tokenResp.Body, 4096))
			_ = tokenResp.Body.Close()
			http.Error(w, "Twitch token exchange failed: "+strings.TrimSpace(string(body)), http.StatusBadGateway)

			return
		}

		var tokenData TwitchOauthREsponse

		tokenDecoder := json.NewDecoder(tokenResp.Body)

		err = tokenDecoder.Decode(&tokenData)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() {
			err = tokenResp.Body.Close()
		}()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "e-hub-refresh-token",
			Value:    tokenData.RefreshToken,
			Path:     "/",
			Domain:   cookieDomain(),
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second),
			Secure:   os.Getenv("IS_PROD") == "1",
			HttpOnly: true,
		})

		http.SetCookie(w, &http.Cookie{
			Name:     "e-hub-login-event",
			Value:    "1",
			Path:     "/",
			Domain:   cookieDomain(),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   120,
			Secure:   os.Getenv("IS_PROD") == "1",
		})

		var users TwitchUserResponse

		userRequest, err := http.NewRequestWithContext(r.Context(), "GET", "https://api.twitch.tv/helix/users", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		userRequest.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
		userRequest.Header.Set("Client-Id", Env["TwitchClientKey"])

		userResponse, err := netcfg.NewHTTPClient(20 * time.Second).Do(userRequest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}

		defer func() {
			_ = userResponse.Body.Close()
		}()

		if userResponse.StatusCode != http.StatusOK {
			http.Error(w, "Twitch user request failed", http.StatusBadGateway)

			return
		}

		if err := json.NewDecoder(userResponse.Body).Decode(&users); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if len(users.Data) == 0 {
			http.Error(w, "Twitch user not found", http.StatusBadGateway)

			return
		}

		user := users.Data[0]

		findUser, err := userRepository.Find(data_base.Where{
			Column: "username",
			Value:  user.Login,
		})
		if err != nil && err != sql.ErrNoRows {
			http.Error(w, "Twitch user lookup failed: "+err.Error(), http.StatusBadGateway)

			return
		}

		twitchUserID, _ := strconv.ParseInt(user.ID, 10, 64)
		userRecord := findUser

		if userRecord == nil {
			userRecord, err = userRepository.Insert(data_base.User{
				Username:     user.Login,
				DisplayName:  user.Login,
				TwitchUserID: twitchUserID,
				Avatar:       user.ProfileImageURL,
			})
			if err != nil || userRecord == nil {
				http.Error(w, "Twitch user save failed: "+fmt.Sprint(err), http.StatusBadGateway)

				return
			}
		} else {
			userRecord.TwitchUserID = twitchUserID
			userRecord.DisplayName = user.Login
			userRecord.Avatar = user.ProfileImageURL
		}

		if userRecord.Banned {
			http.Error(w, "Доступ к StreamHata заблокирован", http.StatusForbidden)

			return
		}

		existingToken, err := tokenRepo.Find(data_base.Where{
			Column: "user_id",
			Value:  userRecord.ID,
		})
		if err != nil && err != sql.ErrNoRows {
			http.Error(w, "Twitch token lookup failed: "+err.Error(), http.StatusBadGateway)

			return
		}

		var tokenRecord *data_base.Token

		if existingToken == nil {
			tokenRecord, err = tokenRepo.Insert(data_base.Token{
				AccessToken:  tokenData.AccessToken,
				RefreshToken: tokenData.RefreshToken,
				UserID:       userRecord.ID,
			})
		} else {
			existingToken.AccessToken = tokenData.AccessToken
			existingToken.RefreshToken = tokenData.RefreshToken
			tokenRecord, err = tokenRepo.Update(*existingToken)
		}

		if err != nil || tokenRecord == nil {
			http.Error(w, "Twitch token save failed: "+fmt.Sprint(err), http.StatusBadGateway)

			return
		}

		userRecord.TwitchToken = sql.NullInt64{Int64: int64(tokenRecord.ID), Valid: true}
		if _, err = userRepository.Update(*userRecord); err != nil {
			http.Error(w, "Twitch user update failed: "+err.Error(), http.StatusBadGateway)

			return
		}

		urls := map[string]string{
			"followers":   fmt.Sprintf("https://api.twitch.tv/helix/channels/followers?broadcaster_id=%s", user.ID),
			"chatters":    fmt.Sprintf("https://api.twitch.tv/helix/chat/chatters?broadcaster_id=%s&moderator_id=%s", user.ID, user.ID),
			"streams":     fmt.Sprintf("https://api.twitch.tv/helix/streams?user_id=%s", user.ID),
			"subscribers": fmt.Sprintf("https://api.twitch.tv/helix/subscriptions?broadcaster_id=%s", user.ID),
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}

		var followers TwitchFollowersResponse
		var streams TwitchStreamsResponse
		var subs TwitchSubscribersResponse
		var chatters TwitchChattersResponse
		g, ctx := errgroup.WithContext(r.Context())

		g.Go(func() error {
			resp, err := SendRequest[TwitchFollowersResponse](
				urls["followers"],
				ctx,
				"GET",
				tokenData.AccessToken)

			if resp != nil {
				followers = *resp
			}

			if err != nil {
				followers = TwitchFollowersResponse{
					Total: 0,
				}
			}

			return err
		})
		g.Go(func() error {
			resp, err := SendRequest[TwitchStreamsResponse](
				urls["streams"],
				ctx,
				http.MethodGet,
				tokenData.AccessToken)

			if resp != nil {
				streams = *resp
			}
			if err != nil {
				streams = TwitchStreamsResponse{}
			}
			return err
		})
		g.Go(func() error {
			resp, err := SendRequest[TwitchSubscribersResponse](
				urls["subscribers"], ctx, http.MethodGet, tokenData.AccessToken)
			if resp != nil {
				subs = *resp
			}
			if err != nil {
				subs = TwitchSubscribersResponse{
					Total: 0,
				}
			}
			return err
		})

		g.Go(func() error {
			resp, err := SendRequest[TwitchChattersResponse](urls["chatters"], ctx, http.MethodGet, tokenData.AccessToken)
			if resp != nil {
				chatters = *resp
			}
			if err != nil {
				chatters = TwitchChattersResponse{
					Total: 0,
				}
			}
			return err
		})

		err = g.Wait()
		streamInfo := TwitchStreamInfo{Online: false}

		if len(streams.Data) > 0 {
			stream := streams.Data[0]
			streamInfo = TwitchStreamInfo{
				Online:       true,
				ID:           stream.ID,
				Title:        stream.Title,
				GameID:       stream.GameID,
				GameName:     stream.GameName,
				ViewerCount:  stream.ViewerCount,
				StartedAt:    stream.StartedAt,
				Language:     stream.Language,
				ThumbnailURL: stream.ThumbnailURL,
				Tags:         stream.Tags,
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var resp ValidationResponse
		if err != nil {
			resp = ValidationResponse{
				Valid: false,
			}
		}

		resp = ValidationResponse{
			Valid:  true,
			Stream: streamInfo,
			Stats: TwitchUserStats{
				Chatters:    chatters.Total,
				Followers:   followers.Total,
				Subscribers: subs.Total,
			},
		}

		resp.User = User{
			ID:           strconv.Itoa(userRecord.ID),
			DisplayName:  userRecord.DisplayName,
			Username:     userRecord.Username,
			TwitchUserID: user.ID,
			Avatar:       userRecord.Avatar,
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			target := "/app/dashboard"

			if cookie, err := r.Cookie("e-hub-return"); err == nil && validReturnURL(cookie.Value) {
				target = cookie.Value
				http.SetCookie(w, &http.Cookie{
					Name:   "e-hub-return",
					Value:  "",
					Path:   "/",
					Domain: cookieDomain(),
					MaxAge: -1,
				})
			}

			http.Redirect(w, r, target, http.StatusSeeOther)

			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})

	validateTokenHandler := func(w http.ResponseWriter, r *http.Request) {
		refreshCookie, err := r.Cookie("e-hub-refresh-token")
		if err != nil || refreshCookie.Value == "" {
			writeValidation(w, ValidationResponse{Valid: false})

			return
		}

		validationCache.Lock()
		cached, cachedOK := validationCache.items[refreshCookie.Value]
		validationCache.Unlock()

		if cachedOK && time.Now().Before(cached.expires) {
			writeValidation(w, cached.response)

			return
		}

		findToken, err := tokenRepo.Find(data_base.Where{
			Column: "refresh_token",
			Value:  refreshCookie.Value,
		})
		if err != nil || findToken == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(ValidationResponse{Valid: false})

			return
		}

		dbUser, err := userRepository.Find(data_base.Where{
			Column: "id",
			Value:  findToken.UserID,
		})
		if err != nil || dbUser == nil || dbUser.Banned {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(ValidationResponse{Valid: false})

			return
		}

		params := url.Values{}
		params.Set("client_id", Env["TwitchClientKey"])
		params.Set("client_secret", Env["TwitchClientSecret"])
		params.Set("grant_type", "refresh_token")
		params.Set("refresh_token", refreshCookie.Value)

		tokenReq, err := http.NewRequestWithContext(
			r.Context(),
			http.MethodPost,
			twitchTokenURL(),
			strings.NewReader(params.Encode()),
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}

		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		response, err := netcfg.NewHTTPClient(20 * time.Second).Do(tokenReq)
		if err != nil || response == nil || response.StatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(ValidationResponse{Valid: false})

			return
		}

		defer func() {
			_ = response.Body.Close()
		}()

		var data TwitchOauthREsponse
		if err := json.NewDecoder(response.Body).Decode(&data); err != nil || data.AccessToken == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(ValidationResponse{Valid: false})

			return
		}

		findToken.AccessToken = data.AccessToken
		findToken.RefreshToken = data.RefreshToken
		if _, err := tokenRepo.Update(*findToken); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     "e-hub-refresh-token",
				Value:    data.RefreshToken,
				Path:     "/",
				Domain:   cookieDomain(),
				Secure:   os.Getenv("IS_PROD") == "1",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Expires:  time.Now().Add(time.Duration(data.ExpiresIn) * time.Second),
			})
		}

		stats, stream := loadTwitchState(r.Context(), data.AccessToken, strconv.FormatInt(dbUser.TwitchUserID, 10))

		validation := ValidationResponse{
			Valid: true,
			User: User{
				ID:           strconv.Itoa(dbUser.ID),
				DisplayName:  dbUser.DisplayName,
				Username:     dbUser.Username,
				TwitchUserID: strconv.FormatInt(dbUser.TwitchUserID, 10),
				Avatar:       dbUser.Avatar,
			},
			Stats:  stats,
			Stream: stream,
		}

		validationCache.Lock()
		validationCache.items[refreshCookie.Value] = cachedValidation{response: validation, expires: time.Now().Add(30 * time.Second)}
		validationCache.Unlock()
		writeValidation(w, validation)
	}

	srv.ServeEndpoint("/rgg-sync/twitch/logout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("e-hub-refresh-token"); err == nil {
			validationCache.Lock()
			delete(validationCache.items, cookie.Value)
			validationCache.Unlock()
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "e-hub-refresh-token",
			Value:    "",
			Path:     "/",
			Domain:   cookieDomain(),
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		_ = json.NewEncoder(w).Encode(map[string]bool{"valid": false})
	})

	srv.ServeEndpoint("/twitch/validate-token", validateTokenHandler)
	srv.ServeEndpoint("/rgg-sync/twitch/validate", validateTokenHandler)

	// Фронтовая панель зовёт /rgg-sync/twitch: если refresh-cookie нет —
	// отвечаем JSON-статусом "не авторизован", иначе валидируем как обычно.
	srv.ServeEndpoint("/rgg-sync/twitch", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("e-hub-refresh-token"); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")

			if err := json.NewEncoder(w).Encode(ValidationResponse{Valid: false}); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}

			return
		}

		validateTokenHandler(w, r)
	})
}

func loadTwitchState(ctx context.Context, token string, broadcasterID string) (TwitchUserStats, TwitchStreamInfo) {
	stats := TwitchUserStats{}
	stream := TwitchStreamInfo{Online: false}

	if response, err := SendRequest[TwitchFollowersResponse](
		"https://api.twitch.tv/helix/channels/followers?broadcaster_id="+broadcasterID,
		ctx,
		http.MethodGet,
		token,
	); err == nil && response != nil {
		stats.Followers = response.Total
	}

	if response, err := SendRequest[TwitchSubscribersResponse](
		"https://api.twitch.tv/helix/subscriptions?broadcaster_id="+broadcasterID,
		ctx,
		http.MethodGet,
		token,
	); err == nil && response != nil {
		stats.Subscribers = response.Total
	}

	if response, err := SendRequest[TwitchChattersResponse](
		"https://api.twitch.tv/helix/chat/chatters?broadcaster_id="+broadcasterID+"&moderator_id="+broadcasterID,
		ctx,
		http.MethodGet,
		token,
	); err == nil && response != nil {
		stats.Chatters = response.Total
	}

	if response, err := SendRequest[TwitchStreamsResponse](
		"https://api.twitch.tv/helix/streams?user_id="+broadcasterID,
		ctx,
		http.MethodGet,
		token,
	); err == nil && response != nil && len(response.Data) > 0 {
		item := response.Data[0]
		stream = TwitchStreamInfo{
			Online:       true,
			ID:           item.ID,
			Title:        item.Title,
			GameID:       item.GameID,
			GameName:     item.GameName,
			ViewerCount:  item.ViewerCount,
			StartedAt:    item.StartedAt,
			Language:     item.Language,
			ThumbnailURL: item.ThumbnailURL,
			Tags:         item.Tags,
		}
	}

	return stats, stream
}

func validReturnURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" {
		return false
	}

	host := parsed.Hostname()

	return host == "streamhata.rdpctd.dev" || host == "streamhata-admin.rdpctd.dev"
}

func cookieDomain() string {
	return strings.TrimSpace(os.Getenv("COOKIE_DOMAIN"))
}

func twitchTokenURL() string {
	if value := strings.TrimSpace(os.Getenv("TWITCH_TOKEN_URL")); value != "" {
		return value
	}

	return "https://id.twitch.tv/oauth2/token"
}

func buildParams(grantType string) url.Values {
	params := url.Values{}
	params.Set("client_id", Env["TwitchClientKey"])
	params.Set("client_secret", Env["TwitchClientSecret"])

	params.Set("grant_type", grantType)
	params.Set("redirect_uri", Env["TwitchAppURL"])
	return params
}
