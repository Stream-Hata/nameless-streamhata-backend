package donations

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

var (
	idKeys       = []string{"id", "donation_id", "notification_id", "message_id", "transaction_id", "kofi_transaction_id"}
	usernameKeys = []string{"username", "name", "from_name", "user_name", "user", "nickname", "display_name"}
	messageKeys  = []string{"message", "comment", "text", "note"}
	amountKeys   = []string{"amount", "sum", "total", "value", "price"}
	currencyKeys = []string{"currency", "currency_code", "ccy"}
	typeKeys     = []string{"type", "what", "kind"}
	createdKeys  = []string{"created_at", "createdAt", "date", "time", "timestamp", "sent_at"}
)

// normalizeMap вытаскивает донат-событие из произвольного JSON-объекта
// провайдера. Поля ищутся по набору известных имён — форматы сервисов
// отличаются, поэтому парсинг пермиссивный.
func normalizeMap(data map[string]any) (Event, bool) {
	event := Event{
		ID:       pickString(data, idKeys),
		Username: pickString(data, usernameKeys),
		Message:  pickString(data, messageKeys),
		Amount:   pickFloat(data, amountKeys),
		Currency: pickString(data, currencyKeys),
		Type:     pickString(data, typeKeys),
	}

	if event.Type == "" {
		event.Type = "donation"
	}

	if raw := pickRaw(data, createdKeys); raw != nil {
		event.CreatedAt = parseTime(raw)
	}

	if event.Amount <= 0 && event.Username == "" {
		return Event{}, false
	}

	normalizeType(&event)

	return event, true
}

func normalizeType(event *Event) {
	switch strings.ToLower(event.Type) {
	case "donation", "donate", "tip", "donationalerts", "donationalert":
		event.Type = "donation"
	case "subscription", "sub", "resub":
		event.Type = "subscription"
	case "merch", "merchandise", "shop_order":
		event.Type = "merch"
	default:
		event.Type = "donation"
	}
}

func pickRaw(data map[string]any, keys []string) any {
	for _, key := range keys {
		if value, ok := data[key]; ok && value != nil {
			return value
		}
	}

	return nil
}

func pickString(data map[string]any, keys []string) string {
	for _, key := range keys {
		value, ok := data[key].(string)

		if !ok || value == "" {
			continue
		}

		if decoded, err := strconv.Unquote(`"` + value + `"`); err == nil {
			value = decoded
		}

		return strings.TrimSpace(value)
	}

	// Числовые id/username не редкость: kofi_transaction_id приходит строкой,
	// но некоторые сервисы шлют number.
	for _, key := range keys {
		if number, ok := data[key].(float64); ok {
			return strconv.FormatFloat(number, 'f', -1, 64)
		}
	}

	return ""
}

func pickFloat(data map[string]any, keys []string) float64 {
	for _, key := range keys {
		switch value := data[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case int64:
			return float64(value)
		case json.Number:
			if parsed, err := value.Float64(); err == nil {
				return parsed
			}
		case string:
			cleaned := strings.ReplaceAll(strings.TrimSpace(value), ",", ".")

			if parsed, err := strconv.ParseFloat(cleaned, 64); err == nil {
				return parsed
			}
		}
	}

	return 0
}

func parseTime(value any) time.Time {
	switch typed := value.(type) {
	case float64:
		return time.Unix(int64(typed), 0).UTC()
	case int64:
		return time.Unix(typed, 0).UTC()
	case string:
		formats := []string{
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05Z",
		}

		for _, format := range formats {
			if parsed, err := time.Parse(format, typed); err == nil {
				return parsed.UTC()
			}
		}
	}

	return time.Time{}
}

// unwrapData достаёт полезную нагрузку из обёрток вида {"data": {...}}.
func unwrapData(data map[string]any) map[string]any {
	nested, ok := data["data"].(map[string]any)

	if !ok {
		return data
	}

	return nested
}
