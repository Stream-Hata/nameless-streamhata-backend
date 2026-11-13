package netcfg

import (
	"net/http"
	"testing"
)

func TestProxyFuncUsesCurrentProvider(t *testing.T) {
	previous := defaultProvider
	defer func() {
		defaultProvider = previous
	}()

	request, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/", nil)

	defaultProvider = &Provider{settings: Settings{Enabled: true, ProxyURL: "http://127.0.0.1:1111"}}
	proxy := ProxyFunc()
	first, err := proxy(request)
	if err != nil || first == nil || first.Host != "127.0.0.1:1111" {
		t.Fatalf("unexpected first proxy: %v %v", first, err)
	}

	defaultProvider = &Provider{settings: Settings{Enabled: true, ProxyURL: "http://127.0.0.1:2222"}}
	second, err := proxy(request)
	if err != nil || second == nil || second.Host != "127.0.0.1:2222" {
		t.Fatalf("dynamic proxy was not refreshed: %v %v", second, err)
	}
}

func TestProxyFuncDisabled(t *testing.T) {
	previous := defaultProvider
	defer func() {
		defaultProvider = previous
	}()

	defaultProvider = &Provider{settings: Settings{Enabled: false}}
	request, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/", nil)
	proxy, err := ProxyFunc()(request)
	if err != nil || proxy != nil {
		t.Fatalf("expected no proxy, got %v %v", proxy, err)
	}
}
