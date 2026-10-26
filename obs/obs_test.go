package obs

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// Проверка по официальному алгоритму OBS WebSocket 5:
// base64(sha256(base64(sha256(password + salt)) + challenge)).
func TestAuthString(t *testing.T) {
	secret := sha256.Sum256([]byte("supersecret" + "salt123"))
	secretB64 := base64.StdEncoding.EncodeToString(secret[:])

	expectedHash := sha256.Sum256([]byte(secretB64 + "challenge123"))
	expected := base64.StdEncoding.EncodeToString(expectedHash[:])

	if got := authString("supersecret", "salt123", "challenge123"); got != expected {
		t.Fatalf("unexpected auth: %s", got)
	}
}

func TestStatusDefaults(t *testing.T) {
	client := New()
	status := client.GetStatus()

	if status.Connected {
		t.Fatal("client must start disconnected")
	}
}
