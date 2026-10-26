// Package credentials loads secrets from systemd credentials.
package credentials

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const fileName = "streamhata-secrets"

// Load подставляет значения из зашифрованного systemd-credential в окружение.
// Уже заданные переменные (например, из unit-файла) не перезаписываются.
func Load() error {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return nil
	}

	body, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	var values map[string]string
	if err := json.Unmarshal(body, &values); err != nil {
		return err
	}

	for key, value := range values {
		if key == "" || os.Getenv(key) != "" {
			continue
		}

		_ = os.Setenv(key, value)
	}

	return nil
}
