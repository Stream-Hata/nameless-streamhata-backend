package mods

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testService(serverURL string) *Service {
	return &Service{client: http.DefaultClient, baseURL: serverURL}
}

func TestVerifyModrinthFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/version_file/"):
			_, _ = response.Write([]byte(`{
				"id":"ver-1","project_id":"proj-1","name":"JEI","version_number":"15.2.0",
				"game_versions":["1.20.1"],"loaders":["fabric"],
				"files":[{"hashes":{"sha512":"` + testHash + `"},"url":"https://example.com/jei.jar","filename":"jei.jar","primary":true,"size":123}]
			}`))
		case request.URL.Path == "/project/proj-1":
			_, _ = response.Write([]byte(`{"id":"proj-1","slug":"jei","title":"Just Enough Items"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).Verify(context.Background(), testHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictVerified || result.Title != "Just Enough Items" || result.FileName != "jei.jar" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyUnknownModrinthFile(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	result, err := testService(server.URL).Verify(context.Background(), testHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictUnknown)
	}
}

func TestVerifyTamperedModrinthFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"ver-1","project_id":"proj-1","name":"JEI","version_number":"15.2.0",
			"files":[{"hashes":{"sha512":"` + strings.Repeat("b", 128) + `"},"url":"https://example.com/jei.jar","filename":"jei.jar"}]
		}`))
	}))
	defer server.Close()

	result, err := testService(server.URL).Verify(context.Background(), testHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictTampered {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictTampered)
	}
}

func TestVerifyCurseForgeFallback(t *testing.T) {
	modrinth := httptest.NewServer(http.NotFoundHandler())
	defer modrinth.Close()

	curseforge := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"data":{"exactMatches":[{"id":123,"file":{"id":456,"modId":789,"fileName":"jei.jar","displayName":"JEI","downloadUrl":"https://example.com/jei.jar","gameVersions":["1.20.1"]}}]}}`))
	}))
	defer curseforge.Close()

	service := testService(modrinth.URL)
	service.curseForge = &HTTPCurseForge{URL: curseforge.URL, Client: http.DefaultClient}
	result, err := service.Verify(context.Background(), testHash, []uint32{123})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictVerified || result.Source != "curseforge" || result.FileName != "jei.jar" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestFingerprints(t *testing.T) {
	values := Fingerprints([]byte("hello world"))
	if len(values) != 2 {
		t.Fatalf("expected raw and normalized fingerprints, got %d", len(values))
	}
	if values[0] == values[1] {
		t.Fatal("raw and normalized fingerprints should differ")
	}
}

func TestVerifyRejectsInvalidSHA512(t *testing.T) {
	_, err := (&Service{}).Verify(context.Background(), "abc", nil)
	if err == nil {
		t.Fatal("expected invalid sha512 error")
	}
}

func TestVerifyModrinthServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := testService(server.URL).Verify(context.Background(), testHash, nil)
	if err == nil {
		t.Fatal("expected modrinth error")
	}
}

func TestVerifyProjectLookupFailureStillVerifies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/version_file/"):
			_, _ = response.Write([]byte(`{
				"id":"ver-1","project_id":"proj-1","name":"JEI","version_number":"15.2.0",
				"files":[{"hashes":{"sha512":"` + testHash + `"},"url":"https://example.com/jei.jar","filename":"jei.jar"}]
			}`))
		default:
			http.Error(response, "project unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	result, err := testService(server.URL).Verify(context.Background(), testHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictVerified || result.Title != "JEI" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestVerifyCurseForgeNoMatch(t *testing.T) {
	modrinth := httptest.NewServer(http.NotFoundHandler())
	defer modrinth.Close()
	curseforge := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"data":{"exactMatches":[]}}`))
	}))
	defer curseforge.Close()

	service := testService(modrinth.URL)
	service.curseForge = &HTTPCurseForge{URL: curseforge.URL, Client: http.DefaultClient}
	result, err := service.Verify(context.Background(), testHash, []uint32{123})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictUnknown)
	}
}

func TestVerifyCurseForgeErrorFallsBackToUnknown(t *testing.T) {
	modrinth := httptest.NewServer(http.NotFoundHandler())
	defer modrinth.Close()
	curseforge := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "proxy unavailable", http.StatusBadGateway)
	}))
	defer curseforge.Close()

	service := testService(modrinth.URL)
	service.curseForge = &HTTPCurseForge{URL: curseforge.URL, Client: http.DefaultClient}
	result, err := service.Verify(context.Background(), testHash, []uint32{123})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictUnknown {
		t.Fatalf("verdict = %q, want %q", result.Verdict, VerdictUnknown)
	}
}

func TestFingerprintsWhitespaceOnly(t *testing.T) {
	values := Fingerprints([]byte(" \t\r\n"))
	if len(values) != 2 {
		t.Fatalf("expected raw and normalized fingerprints, got %d", len(values))
	}
	if values[0] == values[1] {
		t.Fatal("whitespace-only raw and normalized fingerprints should differ")
	}
}
