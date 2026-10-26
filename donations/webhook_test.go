package donations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeMapPermissive(t *testing.T) {
	event, ok := normalizeMap(map[string]any{
		"id":       float64(7),
		"username": "ivan",
		"message":  "на бочку",
		"amount":   "150.50",
		"currency": "RUB",
		"type":     "donate",
	})

	if !ok {
		t.Fatal("expected event to be normalized")
	}

	if event.ID != "7" {
		t.Fatalf("expected id 7, got %q", event.ID)
	}

	if event.Amount != 150.5 {
		t.Fatalf("expected amount 150.5, got %v", event.Amount)
	}

	if event.Type != "donation" {
		t.Fatalf("expected normalized type donation, got %q", event.Type)
	}
}

func TestKoFiWebhookParsesFormData(t *testing.T) {
	provider := NewWebhookProvider("kofi", "secret-token")

	body := "data=" + `%7B%22verification_token%22%3A%22secret-token%22%2C%22` +
		`from_name%22%3A%22ivan%22%2C%22amount%22%3A%22%22%2C%22` +
		`message%22%3A%22hello%22%2C%22kofi_transaction_id%22%3A%22abc%22%2C%22` +
		`type%22%3A%22Donation%22%7D`

	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	events, err := provider.parse(request)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Username != "ivan" || events[0].ID != "abc" {
		t.Fatalf("unexpected event %+v", events[0])
	}
}

func TestKoFiWebhookRejectsWrongToken(t *testing.T) {
	provider := NewWebhookProvider("kofi", "expected")

	body := `data={"verification_token":"wrong","from_name":"ivan","amount":"10"}`

	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))

	if _, err := provider.parse(request); err == nil {
		t.Fatal("expected verification error")
	}
}

func TestWebhookProviderSecretCheck(t *testing.T) {
	provider := NewWebhookProvider("generic", "topsecret")

	ok := provider.verifyRequest(httptest.NewRequest(http.MethodPost, "/webhook?secret=topsecret", nil))
	if !ok {
		t.Fatal("expected secret from query to pass")
	}

	bad := provider.verifyRequest(httptest.NewRequest(http.MethodPost, "/webhook?secret=nope", nil))
	if bad {
		t.Fatal("expected wrong secret to fail")
	}
}

func TestJSONPayloadWithDataWrapper(t *testing.T) {
	events := parseJSONPayload([]byte(`{
		"notification_id": "5",
		"type": "donation",
		"data": {"id": "77", "sum": "300", "currency": "RUB", "comment": "за бочку", "username": "mary"}
	}`))

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Username != "mary" || events[0].Amount != 300 || events[0].Message != "за бочку" {
		t.Fatalf("unexpected event %+v", events[0])
	}
}
