package moderation

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

// WordList — открытый список запреток/правил.
type WordList struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Words       []string `json:"words"`
}

func presets() []WordList {
	return []WordList{
		{
			ID:          "basic-ru",
			Name:        "Базовый RU",
			Description: "Стартовый набор оскорблений и мата (демо-список)",
			Words:       []string{"дурак", "идиот", "тупой", "лох", "дебил"},
		},
		{
			ID:          "spam-scam",
			Name:        "Спам и скам",
			Description: "Типовые маркеры скама и рекламы",
			Words: []string{
				"бесплатные битсы", "free bits", "нигерийский принц", "казино",
				"ставки 1xbet", "промокод", "заработок из дома", "crypto giveaway",
			},
		},
		{
			ID:          "twitch-basic",
			Name:        "Twitch: базовые правила",
			Description: "Рекомендованные сообществом стоп-слова (не официальный список Twitch)",
			Words: []string{
				"hate", "kys", "kill yourself", "nazi", "racial slur",
				"спам", "скам", "угрозы", "травля", "хейт",
			},
		},
	}
}

// ParseWordList разбирает текст в список слов: строки/запятые, без комментариев,
// пустых значений и дублей (регистронезависимо).
func ParseWordList(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})

	seen := map[string]bool{}
	words := make([]string, 0, len(fields))

	for _, field := range fields {
		word := strings.TrimSpace(field)

		if word == "" || strings.HasPrefix(word, "#") || strings.HasPrefix(word, "//") {
			continue
		}

		key := strings.ToLower(word)

		if seen[key] {
			continue
		}

		seen[key] = true
		words = append(words, word)
	}

	return words
}

func RegisterWordListRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/moderation/wordlists", func(response http.ResponseWriter, request *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{"presets": presets()})
	})

	srv.ServeEndpoint("/rgg-sync/moderation/wordlists/fetch", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			URL string `json:"url"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.URL == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "укажите url текстового списка"})

			return
		}

		requestHTTP, err := http.NewRequestWithContext(request.Context(), http.MethodGet, body.URL, nil)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		responseHTTP, err := netcfg.NewHTTPClient(0).Do(requestHTTP)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		defer func() {
			_ = responseHTTP.Body.Close()
		}()

		if responseHTTP.StatusCode != http.StatusOK {
			writeJSON(response, http.StatusBadGateway, map[string]string{
				"error": fmt.Sprintf("источник вернул %d", responseHTTP.StatusCode),
			})

			return
		}

		raw, err := io.ReadAll(io.LimitReader(responseHTTP.Body, 1<<20))
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"words": ParseWordList(string(raw)),
		})
	})
}
