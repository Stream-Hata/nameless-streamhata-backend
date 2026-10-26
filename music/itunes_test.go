package music

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnrichWithITunes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"resultCount": 1,
			"results": [{
				"trackName": "Never Gonna Give You Up",
				"artistName": "Rick Astley",
				"collectionName": "Whenever You Need Somebody",
				"releaseDate": "1987-11-12T08:00:00Z"
			}]
		}`))
	}))
	defer server.Close()

	previous := iTunesBaseURL
	iTunesBaseURL = server.URL
	defer func() {
		iTunesBaseURL = previous
	}()

	album, year := enrichWithITunes(context.Background(), "Never Gonna Give You Up", "Rick Astley")

	if album != "Whenever You Need Somebody" {
		t.Fatalf("unexpected album: %q", album)
	}

	if year != 1987 {
		t.Fatalf("unexpected year: %d", year)
	}
}

func TestEnrichWithITunesNoMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"resultCount":0,"results":[]}`))
	}))
	defer server.Close()

	previous := iTunesBaseURL
	iTunesBaseURL = server.URL
	defer func() {
		iTunesBaseURL = previous
	}()

	album, year := enrichWithITunes(context.Background(), "Unknown Track", "Unknown Artist")

	if album != "" || year != 0 {
		t.Fatalf("expected empty result, got %q / %d", album, year)
	}
}
