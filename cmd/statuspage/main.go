package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/credentials"
	data_base "github.com/p7x-dev/nameless-streamhata-backend/data-base"
	"github.com/p7x-dev/nameless-streamhata-backend/statuspage"
	"github.com/p7x-dev/nameless-streamhata-backend/vaultsecrets"
)

//go:embed web/*
var webFiles embed.FS

type muxAdapter struct {
	mux *http.ServeMux
}

func (m muxAdapter) ServeEndpoint(name string, handler http.HandlerFunc) {
	m.mux.HandleFunc(name, handler)
}

func main() {
	if os.Getenv("OPENBAO_ADDR") == "" {
		_ = os.Setenv("OPENBAO_ADDR", "https://10.66.0.2:8200")
	}
	if os.Getenv("OPENBAO_CACERT") == "" {
		_ = os.Setenv("OPENBAO_CACERT", "/etc/nsh/openbao-ca.crt")
	}
	if err := credentials.Load(); err != nil {
		log.Printf("[statuspage] credentials unavailable: %v", err)
	}
	if err := vaultsecrets.Load(); err != nil {
		log.Printf("[statuspage] OpenBao unavailable: %v", err)
	}
	ctx := context.Background()
	db, err := data_base.NewConnection(ctx)
	if err != nil {
		log.Printf("[statuspage] database unavailable: %v", err)
		db = nil
	}

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	service := statuspage.New(db)
	statuspage.RegisterRoutes(muxAdapter{mux: mux}, service)
	files := http.FileServer(http.FS(webRoot))
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			token := service.AdminToken()
			user, password, ok := request.BasicAuth()

			if token == "" || !ok || user != "admin" || subtle.ConstantTimeCompare([]byte(token), []byte(password)) != 1 {
				response.Header().Set("WWW-Authenticate", `Basic realm="StreamHata status admin"`)
				http.Error(response, "unauthorized", http.StatusUnauthorized)

				return
			}
			next.ServeHTTP(response, request)
		})
	}
	mux.Handle("/admin.html", admin(files))
	mux.Handle("/admin.js", admin(files))
	mux.Handle("/admin", admin(files))
	mux.Handle("/", files)
	mux.HandleFunc("/favicon.ico", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = response.Write([]byte("ok\n"))
	})

	address := strings.TrimSpace(os.Getenv("STATUS_LISTEN_ADDR"))
	if address == "" {
		address = "0.0.0.0:8080"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("[statuspage] listening on %s", address)
	log.Fatal(server.ListenAndServe())
}
