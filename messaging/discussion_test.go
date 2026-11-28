package messaging

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListGameDiscussionRequiresTwitchSession(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/rgg-sync/games/example/discussion", nil)
	request.SetPathValue("slug", "example")
	response := httptest.NewRecorder()

	(&Service{}).gameDiscussion(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated discussion request to return 401, got %d", response.Code)
	}
}
