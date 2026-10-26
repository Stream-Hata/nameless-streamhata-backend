package donations

import (
	"encoding/json"
	"net/http"
	"strings"
)

type ttsRouter interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func RegisterTTSRoutes(srv ttsRouter, service *Service, tts *TTS) {
	srv.ServeEndpoint("/rgg-sync/donations/tts/voices", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeTTSJSON(response, http.StatusOK, map[string]any{
			"voices":     tts.Voices(),
			"models":     []string{"canopylabs/orpheus-v1-english", "canopylabs/orpheus-arabic-saudi"},
			"configured": tts.Configured(),
		})
	})

	srv.ServeEndpoint("/rgg-sync/donations/tts", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			DonationID string `json:"donationId"`
			Text       string `json:"text"`
			Voice      string `json:"voice"`
			Model      string `json:"model"`
			Provider   string `json:"provider"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeTTSJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		event := findDonation(service, body.DonationID)

		if event == nil && strings.TrimSpace(body.Text) == "" {
			writeTTSJSON(response, http.StatusBadRequest, map[string]string{"error": "donationId or text is required"})

			return
		}

		username := ""
		text := strings.TrimSpace(body.Text)

		if event != nil {
			username = event.Username
			text = event.Message
		}

		setting := tts.SettingFor(username)
		if event != nil && !setting.Enabled {
			writeTTSJSON(response, http.StatusOK, map[string]any{"skipped": true, "reason": "user disabled tts"})

			return
		}

		provider := strings.TrimSpace(body.Provider)
		if provider == "" {
			provider = setting.Provider
		}

		voice := strings.TrimSpace(body.Voice)
		if voice == "" {
			voice = setting.Voice
		}

		url, err := tts.Speak(request.Context(), provider, text, voice, body.Model)
		if err != nil {
			writeTTSJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeTTSJSON(response, http.StatusOK, map[string]any{
			"url":   url,
			"voice": voice,
			"model": body.Model,
		})
	})
}

func findDonation(service *Service, id string) *Event {
	if service == nil || id == "" {
		return nil
	}

	for _, event := range service.Bus().Recent(200) {
		if event.ID == id {
			copy := event

			return &copy
		}
	}

	return nil
}

func writeTTSJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
