package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/access"
	"github.com/p7x-dev/nameless-streamhata-backend/llm"
	"github.com/p7x-dev/nameless-streamhata-backend/twitch"
	"github.com/p7x-dev/nameless-streamhata-backend/vaultsecrets"
)

type llmCredentialsInput struct {
	Code             string `json:"code"`
	GroqAPIKey       string `json:"groq_api_key"`
	OpenRouterAPIKey string `json:"openrouter_api_key"`
}

type llmSetupGrant struct {
	login        string
	twitchUserID string
	expires      time.Time
}

func registerLLMCredentialsRoute(
	srv interface {
		ServeEndpoint(string, http.HandlerFunc)
	},
	accessService *access.Service,
	providers *llm.ProviderChain,
) {
	var grants = struct {
		sync.Mutex
		items map[string]llmSetupGrant
	}{items: make(map[string]llmSetupGrant)}

	srv.ServeEndpoint("/rgg-sync/settings/llm/providers", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv.ServeEndpoint("/rgg-sync/settings/llm/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if twitch.Env["TwitchBaseURL"] == "" || twitch.Env["TwitchClientKey"] == "" || twitch.Env["TwitchAppURL"] == "" {
			http.Error(w, "Twitch OAuth is not configured", http.StatusServiceUnavailable)
			return
		}
		state, err := randomLLMSetupCode()
		if err != nil {
			http.Error(w, "could not start Twitch authorization", http.StatusInternalServerError)
			return
		}
		twitch.RegisterOAuthSetupFlow(state, func(w http.ResponseWriter, login, twitchUserID string) {
			if accessService == nil || !accessService.IsApplicationOwner(login, twitchUserID) {
				http.Error(w, "This Twitch account is not the channel owner", http.StatusForbidden)
				return
			}
			code, err := randomLLMSetupCode()
			if err != nil {
				http.Error(w, "could not create setup session", http.StatusInternalServerError)
				return
			}
			grants.Lock()
			now := time.Now()
			for key, grant := range grants.items {
				if now.After(grant.expires) {
					delete(grants.items, key)
				}
			}
			grants.items[code] = llmSetupGrant{login: strings.ToLower(login), twitchUserID: twitchUserID, expires: now.Add(5 * time.Minute)}
			grants.Unlock()
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/rgg-sync/settings/llm/setup/form#"+code, http.StatusSeeOther)
		})

		params := url.Values{}
		params.Set("client_id", twitch.Env["TwitchClientKey"])
		params.Set("redirect_uri", twitch.Env["TwitchAppURL"])
		params.Set("response_type", "code")
		params.Set("state", state)
		params.Set("scope", "user:read:email")
		authorizeURL := twitch.Env["TwitchBaseURL"] + "?" + params.Encode()
		http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
	})

	srv.ServeEndpoint("/rgg-sync/settings/llm/setup/form", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(llmSetupPage))
	})

	srv.ServeEndpoint("/rgg-sync/settings/llm/setup/claim", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeLLMJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || origin.Scheme != "https" || !strings.EqualFold(origin.Host, r.Host) {
			writeLLMJSON(w, http.StatusForbidden, map[string]string{"error": "same-origin HTTPS request required"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var input struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeLLMJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		grants.Lock()
		grant, ok := grants.items[strings.TrimSpace(input.Code)]
		delete(grants.items, strings.TrimSpace(input.Code))
		grants.Unlock()
		if !ok || time.Now().After(grant.expires) || accessService == nil || !accessService.IsApplicationOwner(grant.login, grant.twitchUserID) {
			writeLLMJSON(w, http.StatusUnauthorized, map[string]string{"error": "setup authorization expired; start again"})
			return
		}
		ticket, err := randomLLMSetupCode()
		if err != nil {
			writeLLMJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create setup session"})
			return
		}
		grants.Lock()
		grants.items[ticket] = llmSetupGrant{login: grant.login, twitchUserID: grant.twitchUserID, expires: time.Now().Add(5 * time.Minute)}
		grants.Unlock()
		writeLLMJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
	})

	srv.ServeEndpoint("/rgg-sync/settings/llm/setup/submit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeLLMJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || origin.Scheme != "https" || !strings.EqualFold(origin.Host, r.Host) {
			writeLLMJSON(w, http.StatusForbidden, map[string]string{"error": "same-origin HTTPS request required"})
			return
		}
		if providers == nil {
			writeLLMJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "LLM providers are not initialized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input llmCredentialsInput
		if err := decoder.Decode(&input); err != nil {
			writeLLMJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		input.Code = strings.TrimSpace(input.Code)
		input.GroqAPIKey = strings.TrimSpace(input.GroqAPIKey)
		input.OpenRouterAPIKey = strings.TrimSpace(input.OpenRouterAPIKey)
		if len(input.GroqAPIKey) < 16 || len(input.GroqAPIKey) > 512 || len(input.OpenRouterAPIKey) < 16 || len(input.OpenRouterAPIKey) > 512 {
			writeLLMJSON(w, http.StatusBadRequest, map[string]string{"error": "both provider keys are required"})
			return
		}
		grants.Lock()
		grant, ok := grants.items[input.Code]
		delete(grants.items, input.Code)
		grants.Unlock()
		if !ok || time.Now().After(grant.expires) || accessService == nil || !accessService.IsApplicationOwner(grant.login, grant.twitchUserID) {
			writeLLMJSON(w, http.StatusUnauthorized, map[string]string{"error": "setup authorization expired; start again"})
			return
		}

		if err := vaultsecrets.MergeSecret("streamhata-llm", map[string]string{
			"GROQ_API_KEY":       input.GroqAPIKey,
			"OPENROUTER_API_KEY": input.OpenRouterAPIKey,
			"LLM_PROVIDER":       "groq",
		}); err != nil {
			writeLLMJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "secret storage rejected the update"})
			return
		}
		if err := providers.UpdateCredentials(input.GroqAPIKey, input.OpenRouterAPIKey, "groq"); err != nil {
			writeLLMJSON(w, http.StatusInternalServerError, map[string]string{"error": "provider update failed"})
			return
		}
		writeLLMJSON(w, http.StatusOK, map[string]any{"ok": true, "primary": "groq", "fallback": "openrouter"})
	})
}

func randomLLMSetupCode() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeLLMJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

const llmSetupPage = `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Настройки — StreamHata</title><style>body{font:16px system-ui;max-width:560px;margin:40px auto;padding:0 18px;color:#eee;background:#171717}h1{font-size:24px}label{display:block;margin:20px 0 6px}input{box-sizing:border-box;width:100%;padding:12px;border:1px solid #555;border-radius:8px;background:#222;color:#fff}button{margin-top:22px;padding:12px 18px;border:0;border-radius:8px;background:#9147ff;color:#fff;font-weight:700}#status{white-space:pre-wrap;margin-top:16px}</style><h1>Настройки</h1><p id="status">Проверка доступа…</p><form id="form" hidden><label for="groq">Groq API key</label><input id="groq" type="password" autocomplete="off" required><label for="openrouter">OpenRouter API key</label><input id="openrouter" type="password" autocomplete="off" required><button>Сохранить</button></form><script>const code=location.hash.slice(1);history.replaceState(null,"",location.pathname);const form=document.getElementById("form"),status=document.getElementById("status");let ticket="";async function post(path,body){const r=await fetch(path,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});const data=await r.json();if(!r.ok)throw new Error(data.error||"Не удалось подтвердить доступ");return data}if(code){post("/rgg-sync/settings/llm/setup/claim",{code}).then(data=>{ticket=data.ticket;form.hidden=false;status.textContent="Доступ подтверждён. Введи новые ключи провайдеров."}).catch(err=>{status.textContent=err.message})}else{status.textContent="Страница недоступна."}form.addEventListener("submit",async e=>{e.preventDefault();status.textContent="Сохраняю…";try{await post("/rgg-sync/settings/llm/setup/submit",{code:ticket,groq_api_key:document.getElementById("groq").value,openrouter_api_key:document.getElementById("openrouter").value});form.reset();form.hidden=true;status.textContent="Ключи сохранены."}catch(err){status.textContent=err.message}});</script></html>`
