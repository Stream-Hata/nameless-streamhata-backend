package feedback

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
)

type fakeUsers struct {
	user *data_base.User
}

func (f *fakeUsers) Insert(data_base.User) (*data_base.User, error) { return f.user, nil }
func (f *fakeUsers) Update(data_base.User) (*data_base.User, error) { return f.user, nil }
func (f *fakeUsers) Delete(data_base.User) error                    { return nil }
func (f *fakeUsers) First() (*data_base.User, error)                { return f.user, nil }
func (f *fakeUsers) Find(data_base.Where) (*data_base.User, error) {
	if f.user == nil {
		return nil, errors.New("not found")
	}

	return f.user, nil
}

type fakeTokens struct {
	token *data_base.Token
}

func (f *fakeTokens) Insert(data_base.Token) (*data_base.Token, error) { return f.token, nil }
func (f *fakeTokens) Update(data_base.Token) (*data_base.Token, error) { return f.token, nil }
func (f *fakeTokens) Delete(data_base.Token) error                     { return nil }
func (f *fakeTokens) First() (*data_base.Token, error)                 { return f.token, nil }
func (f *fakeTokens) Find(data_base.Where) (*data_base.Token, error) {
	if f.token == nil {
		return nil, errors.New("not found")
	}

	return f.token, nil
}

func TestSessionUserRequiresRefreshCookie(t *testing.T) {
	service := &Service{
		users:  &fakeUsers{user: &data_base.User{ID: 1, Username: "viewer"}},
		tokens: &fakeTokens{token: &data_base.Token{UserID: 1, RefreshToken: "refresh"}},
	}

	request := httptest.NewRequest("POST", "/rgg-sync/feedback", nil)
	if _, err := service.sessionUser(request); err == nil {
		t.Fatal("expected unauthorized without cookie")
	}

	request.AddCookie(&http.Cookie{Name: "e-hub-refresh-token", Value: "refresh"})
	user, err := service.sessionUser(request)
	if err != nil || user == nil || user.Username != "viewer" {
		t.Fatalf("expected viewer, got %#v, %v", user, err)
	}
}
