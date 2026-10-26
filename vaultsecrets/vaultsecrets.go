// Package vaultsecrets загружает секреты из OpenBao/Vault в окружение.
package vaultsecrets

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const roleFile = "openbao-approle"

type approle struct {
	RoleID   string `json:"role_id"`
	SecretID string `json:"secret_id"`
}

func Load() error {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	addr := os.Getenv("OPENBAO_ADDR")
	if dir == "" || addr == "" {
		return nil
	}

	body, err := os.ReadFile(filepath.Join(dir, roleFile))
	if err != nil {
		return nil
	}

	var role approle
	if err := json.Unmarshal(body, &role); err != nil {
		return err
	}

	client, err := client()
	if err != nil {
		return err
	}

	loginBody, _ := json.Marshal(map[string]string{
		"role_id":   role.RoleID,
		"secret_id": role.SecretID,
	})
	login, err := request(client, http.MethodPost, addr+"/v1/auth/approle/login", loginBody, "")
	if err != nil {
		return err
	}

	var loginResponse struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(login, &loginResponse); err != nil {
		return err
	}
	if loginResponse.Auth.ClientToken == "" {
		return fmt.Errorf("openbao: empty client token")
	}

	payload, err := request(client, http.MethodGet, addr+"/v1/secret/data/streamhata", nil, loginResponse.Auth.ClientToken)
	if err != nil {
		return err
	}

	var response struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return err
	}

	for key, value := range response.Data.Data {
		if key == "" || value == "" {
			continue
		}

		_ = os.Setenv(key, value)
	}

	return nil
}

func client() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	if caPath := os.Getenv("OPENBAO_CACERT"); caPath != "" {
		body, err := os.ReadFile(caPath)
		if err != nil {
			return nil, err
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(body) {
			return nil, fmt.Errorf("openbao: invalid CA certificate")
		}

		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	return &http.Client{Transport: transport, Timeout: 15 * time.Second}, nil
}

func request(client *http.Client, method, target string, body []byte, token string) ([]byte, error) {
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("X-Vault-Token", token)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openbao: status %d", response.StatusCode)
	}

	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(response.Body); err != nil {
		return nil, err
	}

	return buffer.Bytes(), nil
}
