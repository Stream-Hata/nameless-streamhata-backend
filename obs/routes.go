package obs

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
)

// Deps — минимальный контракт сервера.
type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

/* Контракт integrations.Component */

func (c *Client) ID() string {
	return "obs"
}

func (c *Client) Name() string {
	return "OBS WebSocket"
}

func (c *Client) Kind() string {
	return "obs"
}

func (c *Client) Enabled() bool {
	status := c.GetStatus()

	return status.Connected
}

func (c *Client) SetEnabled(enabled bool) error {
	if !enabled {
		c.Disconnect()
	}

	return nil
}

// Status — строка для реестра интеграций (контракт integrations.Component).
func (c *Client) Status() string {
	status := c.GetStatus()

	if !status.Connected {
		return "не подключён"
	}

	return "подключён: " + status.CurrentScene
}

func RegisterRoutes(srv Deps, client *Client) {
	srv.ServeEndpoint("/rgg-sync/obs", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, client.GetStatus())
	})

	srv.ServeEndpoint("/rgg-sync/obs/connect", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			URL      string `json:"url"`
			Password string `json:"password"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.URL == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "укажите url вида ws://127.0.0.1:4455"})

			return
		}

		if err := client.Connect(request.Context(), body.URL, body.Password); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, client.GetStatus())
	})

	srv.ServeEndpoint("/rgg-sync/obs/disconnect", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		client.Disconnect()
		writeJSON(response, http.StatusOK, client.GetStatus())
	})

	srv.ServeEndpoint("/rgg-sync/obs/action", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Type   string `json:"type"`
			Scene  string `json:"scene"`
			Input  string `json:"input"`
			Output string `json:"output"`
			Action string `json:"action"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		var err error
		muted := false

		switch body.Type {
		case "scene":
			err = client.SetScene(request.Context(), body.Scene)
		case "mute":
			muted, err = client.ToggleMute(request.Context(), body.Input)
		case "output":
			err = client.OutputAction(request.Context(), body.Output, body.Action)
		default:
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "type: scene | mute | output"})

			return
		}

		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"ok":    true,
			"muted": muted,
			"state": client.GetStatus(),
		})
	})
}

// AutoConnect — подключение при старте, если заданы env OBS_URL/OBS_PASSWORD.
func AutoConnect(client *Client) {
	url := os.Getenv("OBS_URL")

	if url == "" {
		return
	}

	_ = client.Connect(context.Background(), url, os.Getenv("OBS_PASSWORD"))
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
