package data_base

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

const tokenCipherPrefix = "shenc:v1:"

func tokenKey() ([]byte, error) {
	encoded := strings.TrimSpace(os.Getenv("TWITCH_TOKEN_ENCRYPTION_KEY"))
	if encoded == "" {
		return nil, fmt.Errorf("TWITCH_TOKEN_ENCRYPTION_KEY is not configured")
	}

	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("TWITCH_TOKEN_ENCRYPTION_KEY must be base64-encoded 32-byte key")
	}

	return key, nil
}

func encryptToken(value string) (string, error) {
	if value == "" || strings.HasPrefix(value, tokenCipherPrefix) {
		return value, nil
	}

	key, err := tokenKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(value), []byte(tokenCipherPrefix))

	return tokenCipherPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decryptToken(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, tokenCipherPrefix) {
		return value, nil
	}

	key, err := tokenKey()
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, tokenCipherPrefix))
	if err != nil {
		return "", fmt.Errorf("invalid encrypted Twitch token: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", fmt.Errorf("invalid encrypted Twitch token: truncated payload")
	}

	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(tokenCipherPrefix))
	if err != nil {
		return "", fmt.Errorf("cannot decrypt Twitch token: %w", err)
	}

	return string(plaintext), nil
}

func refreshTokenDigest(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	key, err := tokenKey()
	if err != nil {
		return "", err
	}
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte("refresh-token-lookup:v1:"))
	_, _ = digest.Write([]byte(value))

	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil)), nil
}

// RefreshTokenDigest returns a keyed lookup fingerprint for a refresh token.
func RefreshTokenDigest(value string) (string, error) {
	return refreshTokenDigest(value)
}

// EncryptForStorage encrypts OAuth credentials before they enter the generic repository.
func (token *Token) EncryptForStorage() error {
	digest, err := refreshTokenDigest(token.RefreshToken)
	if err != nil {
		return err
	}
	token.RefreshTokenHash = digest

	token.AccessToken, err = encryptToken(token.AccessToken)
	if err != nil {
		return err
	}
	token.RefreshToken, err = encryptToken(token.RefreshToken)
	return err
}

// DecryptAfterLoad decrypts credentials after reading them from the generic repository.
func (token *Token) DecryptAfterLoad() error {
	var err error
	token.AccessToken, err = decryptToken(token.AccessToken)
	if err != nil {
		return err
	}
	token.RefreshToken, err = decryptToken(token.RefreshToken)
	if err != nil {
		return err
	}
	if token.RefreshTokenHash == "" {
		token.RefreshTokenHash, err = refreshTokenDigest(token.RefreshToken)
	}

	return err
}

// EncryptExistingTokens migrates legacy plaintext token rows in-place.
func EncryptExistingTokens(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`SELECT id, access_token, refresh_token, refresh_token_hash FROM tokens`)
	if err != nil {
		return err
	}
	type row struct {
		id                 int
		accessToken        string
		refreshToken       string
		refreshTokenDigest string
	}
	var records []row
	for rows.Next() {
		var record row
		if err := rows.Scan(&record.id, &record.accessToken, &record.refreshToken, &record.refreshTokenDigest); err != nil {
			_ = rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(records) == 0 {
		return tx.Commit()
	}
	for _, record := range records {
		if record.accessToken != "" || record.refreshToken != "" {
			if _, err := tokenKey(); err != nil {
				return err
			}
			break
		}
	}

	for _, record := range records {
		accessPlaintext, err := decryptToken(record.accessToken)
		if err != nil {
			return err
		}
		refreshPlaintext, err := decryptToken(record.refreshToken)
		if err != nil {
			return err
		}
		accessToken, err := encryptToken(accessPlaintext)
		if err != nil {
			return err
		}
		refreshToken, err := encryptToken(refreshPlaintext)
		if err != nil {
			return err
		}
		digest, err := refreshTokenDigest(refreshPlaintext)
		if err != nil {
			return err
		}
		if accessToken == record.accessToken && refreshToken == record.refreshToken && digest == record.refreshTokenDigest {
			continue
		}
		if _, err := tx.Exec(`UPDATE tokens SET access_token = $1, refresh_token = $2, refresh_token_hash = $3 WHERE id = $4`, accessToken, refreshToken, digest, record.id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// EncryptExistingTokensWithNewConnection upgrades token rows during application startup.
func EncryptExistingTokensWithNewConnection(ctx context.Context) error {
	db, err := NewConnection(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	return EncryptExistingTokens(db)
}
