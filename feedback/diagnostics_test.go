package feedback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeDiagnosticsRemovesSecrets(t *testing.T) {
	input := strings.Join([]string{
		`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`,
		`Cookie: e-hub-refresh-token=secret-cookie`,
		`X-Vault-Token: hvs.secret`,
		`{"access_token":"secret-access","refresh_token":"secret-refresh"}`,
		`token=secret-token&client_secret=secret-client`,
		`https://example.test/callback?code=secret-code&state=ok`,
		`eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature123456`,
		`github_pat_abcdefghijklmnopqrstuvwxyz0123456789`,
		`sk-abcdefghijklmnopqrstuvwxyz0123456789`,
	}, "\n")

	output := sanitizeDiagnostics(input)
	for _, secret := range []string{
		"abcdefghijklmnopqrstuvwxyz",
		"secret-cookie",
		"hvs.secret",
		"secret-access",
		"secret-refresh",
		"secret-token",
		"secret-client",
		"secret-code",
		"signature123456",
		"github_pat_",
		"sk-",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q survived sanitization:\n%s", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("expected redaction marker:\n%s", output)
	}
}

func TestDiagnosticsPeriodsAreBounded(t *testing.T) {
	if diagnosticsPeriods["24h"] > 24*60*60*1e9 {
		t.Fatal("24h period must not exceed one day")
	}
	for _, source := range diagnosticsSources {
		if !diagnosticsSourceAllowed(source.Key) {
			t.Fatalf("source %q must be allowed", source.Key)
		}
	}
}

func TestCollectDiagnosticsReadsLokiAndPrometheus(t *testing.T) {
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("unexpected Loki path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"stream":{"host":"streamhata-main","unit":"nsh-backend.service"},"values":[["1793350000000000000","request failed token=secret-token"]]}]}}`))
	}))
	defer loki.Close()
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			t.Fatalf("unexpected Prometheus path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"host":"streamhata-main"},"values":[[1793350000,"42"]]}]}}`))
	}))
	defer prometheus.Close()

	service := &Service{
		http:          http.DefaultClient,
		lokiURL:       loki.URL,
		prometheusURL: prometheus.URL,
	}
	output := service.collectDiagnostics(context.Background(), DiagnosticsRequest{
		Enabled: true,
		Period:  "15m",
		Sources: []string{"backend", "metrics"},
	}, "viewer")
	if !strings.Contains(output, "request failed") || !strings.Contains(output, "min=42") {
		t.Fatalf("missing collected diagnostics:\n%s", output)
	}
	if strings.Contains(output, "secret-token") {
		t.Fatalf("token leaked into diagnostics:\n%s", output)
	}
}
