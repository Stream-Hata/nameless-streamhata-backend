package server

import (
	"context"
	"net/http"
	"os"
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type Server interface {
	ServeEndpoint(name string, h http.HandlerFunc)
	Shutdown() error
	Listen() error
}

type server struct {
	ctx context.Context
	srv *http.Server
	mux *http.ServeMux
}

// withCORS разрешает запросы панели (dev-proxy и прод-домен) — как в TS-бэкенде.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)

			return
		}

		next.ServeHTTP(w, r)
	})
}

func NewServer(ctx context.Context) Server {
	mux := http.NewServeMux()

	addr := "127.0.0.1:3000"

	if value := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); value != "" {
		addr = value
	}

	srv := http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(withCORS(mux), &http2.Server{}),
	}

	return &server{ctx: ctx, srv: &srv, mux: mux}
}

func (s *server) Shutdown() error {
	return s.srv.Shutdown(s.ctx)
}

func (s *server) ServeEndpoint(name string, h http.HandlerFunc) {
	s.mux.Handle(name, h)
}

func (s *server) Listen() error {
	return s.srv.ListenAndServe()
}
