package catalog

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

func (s *Service) RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/catalog/image", func(response http.ResponseWriter, request *http.Request) {
		target := request.URL.Query().Get("url")
		parsed, err := url.Parse(target)

		if err != nil || !allowedImageHost(parsed.Hostname()) {
			http.Error(response, "image host is not allowed", http.StatusBadRequest)

			return
		}

		client := s.enricher.client

		if strings.Contains(parsed.Hostname(), "rawg.io") && strings.TrimSpace(os.Getenv("RAWG_PROXY_URL")) != "" {
			client = rawgHTTPClient()
		}

		upstream, err := http.NewRequestWithContext(request.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(response, "invalid image url", http.StatusBadRequest)

			return
		}

		upstream.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")

		result, err := client.Do(upstream)
		if err != nil {
			http.Error(response, "image fetch failed", http.StatusBadGateway)

			return
		}

		defer func() {
			_ = result.Body.Close()
		}()

		if result.StatusCode != http.StatusOK {
			response.WriteHeader(result.StatusCode)

			return
		}

		contentType := result.Header.Get("Content-Type")

		if contentType == "" {
			contentType = "image/jpeg"
		}

		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		_, _ = io.Copy(response, io.LimitReader(result.Body, 20<<20))
	})

	srv.ServeEndpoint("/rgg-sync/catalog/games", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, s.List(parseFilters(request)))
	})

	srv.ServeEndpoint("/rgg-sync/catalog/games/search", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))

		writeJSON(response, http.StatusOK, map[string]any{
			"items": s.Search(request.URL.Query().Get("q"), limit),
		})
	})

	srv.ServeEndpoint("/rgg-sync/catalog/games/refresh", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if err := s.Refresh(request.Context()); err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{
			"updated": s.Updated(),
			"total":   s.Total(),
		})
	})

	srv.ServeEndpoint("/rgg-sync/catalog/games/{slug}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			var patch map[string]any

			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			game, ok := s.UpdateGame(request.PathValue("slug"), patch)
			if !ok {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "game not found"})

				return
			}

			writeJSON(response, http.StatusOK, game)

			return
		}

		if request.Method == http.MethodDelete {
			if !s.DeleteGame(request.PathValue("slug")) {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "game not found"})

				return
			}

			writeJSON(response, http.StatusOK, map[string]bool{"deleted": true})

			return
		}

		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		game, ok := s.Get(request.PathValue("slug"))
		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "game not found"})

			return
		}

		if request.URL.Query().Get("refresh") == "1" {
			game = s.enricher.Enrich(request.Context(), game, true, false)
		} else {
			if !s.enricher.Has(game.Slug) {
				gameForEnrich := game

				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					defer cancel()

					s.enricher.Enrich(ctx, gameForEnrich, true, false)
				}()
			}

			game = s.enricher.Enrich(request.Context(), game, false, false)
		}

		log.Printf("[translate] detail %s desc=%d ru=%d", game.Slug, len(game.Description), len(game.DescriptionRu))
		if game.DescriptionRu == "" && game.Description != "" && !containsCyrillic(game.Description) {
			game.DescriptionRu = s.enricher.translateRu(request.Context(), game.Description)
			log.Printf("[translate] assigned len=%d", len(game.DescriptionRu))
		}

		writeJSON(response, http.StatusOK, game)
	})

	srv.ServeEndpoint("/rgg-sync/catalog/import/status", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, s.ImportStatus())
	})

	srv.ServeEndpoint("/rgg-sync/catalog/import/rawg/resume", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"quota": resumeRAWGQuota()})
	})

	srv.ServeEndpoint("/rgg-sync/catalog/import/search", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Query  string `json:"query"`
			Source string `json:"source"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		var result ImportResult
		var err error

		switch {
		case strings.EqualFold(strings.TrimSpace(body.Source), "igdb"):
			result, err = s.ImportIGDBByQuery(request.Context(), body.Query)
		case strings.EqualFold(strings.TrimSpace(body.Source), "steam"):
			result, err = s.ImportSteamByQuery(request.Context(), body.Query)
		default:
			result, err = s.ImportByQuery(request.Context(), body.Query)

			if err != nil {
				result, err = s.ImportSteamByQuery(request.Context(), body.Query)
			}
		}

		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, result)
	})

	srv.ServeEndpoint("/rgg-sync/admin/users", func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			id, _ := strconv.ParseInt(request.URL.Query().Get("id"), 10, 64)

			if !s.DeleteUser(id) {
				writeJSON(response, http.StatusNotFound, map[string]string{"error": "user not found"})

				return
			}

			writeJSON(response, http.StatusOK, map[string]bool{"deleted": true})

			return
		}

		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		items, err := s.AdminUsers()
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"items": items})
	})

	srv.ServeEndpoint("/rgg-sync/admin/users/{id}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
		item, ok := s.AdminUser(id)
		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "user not found"})

			return
		}

		writeJSON(response, http.StatusOK, item)
	})

	srv.ServeEndpoint("/rgg-sync/admin/users/{id}/ban", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body struct {
			Banned bool   `json:"banned"`
			Reason string `json:"reason"`
		}

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		id, _ := strconv.ParseInt(request.PathValue("id"), 10, 64)
		item, ok := s.SetUserBan(id, body.Banned, strings.TrimSpace(body.Reason))
		if !ok {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "user not found"})

			return
		}

		writeJSON(response, http.StatusOK, item)
	})

	srv.ServeEndpoint("/rgg-sync/catalog/search-aliases", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			items, err := s.SearchAliases(request.URL.Query().Get("gameSlug"))
			if err != nil {
				writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, map[string]any{"items": items})
		case http.MethodPost:
			var body struct {
				Term        string `json:"term"`
				Replacement string `json:"replacement"`
				GameSlug    string `json:"gameSlug"`
			}

			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

				return
			}

			if err := s.SaveSearchAlias(body.Term, body.Replacement, body.GameSlug); err != nil {
				writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		case http.MethodDelete:
			term := request.URL.Query().Get("term")

			if err := s.DeleteSearchAlias(term, request.URL.Query().Get("gameSlug")); err != nil {
				writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

				return
			}

			writeJSON(response, http.StatusOK, map[string]bool{"ok": true})
		default:
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv.ServeEndpoint("/rgg-sync/catalog/import", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		var body ImportOptions

		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid json"})

			return
		}

		result, err := s.Import(request.Context(), body)
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, result)
	})

	srv.ServeEndpoint("/rgg-sync/catalog/platforms", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"items": s.Platforms()})
	})

	srv.ServeEndpoint("/rgg-sync/catalog/emulators", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(response, http.StatusOK, map[string]any{"items": emulators})
	})
}

func parseFilters(request *http.Request) Filters {
	query := request.URL.Query()

	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))
	yearFrom, _ := strconv.Atoi(query.Get("yearFrom"))
	yearTo, _ := strconv.Atoi(query.Get("yearTo"))

	var emulator *bool

	if value := query.Get("emulator"); value != "" {
		parsed := value == "true" || value == "1"
		emulator = &parsed
	}

	var verified *bool

	if value := query.Get("verified"); value != "" {
		parsed := value == "true" || value == "1"
		verified = &parsed
	}

	return Filters{
		Query:      query.Get("q"),
		Platform:   query.Get("platform"),
		Tier:       query.Get("tier"),
		Age:        query.Get("age"),
		Emulator:   emulator,
		Verified:   verified,
		YearFrom:   yearFrom,
		YearTo:     yearTo,
		Status:     strings.TrimSpace(query.Get("status")),
		Page:       page,
		PageSize:   pageSize,
		Sort:       query.Get("sort"),
		Order:      query.Get("order"),
		SkipFacets: query.Get("facets") == "0" || query.Get("facets") == "false",
		SkipEnrich: query.Get("enrich") == "0" || query.Get("enrich") == "false",
	}
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}

func allowedImageHost(host string) bool {
	allowed := []string{
		"media.rawg.io",
		"rawg.io",
		"images.igdb.com",
		"mobygames.com",
		"old-games.ru",
		"wikimedia.org",
		"steamstatic.com",
		"akamaihd.net",
		"thegamesdb.net",
		"giantbomb.com",
		"vndb.org",
		"myabandonware.com",
		"launchbox-app.com",
		"screenscraper.fr",
		"rgg.land",
		"itch.zone",
		"gog-statics.com",
		"nexusmods.com",
		"gamebanana.com",
		"images.gamebanana.com",
		"staticdelivery.nexusmods.com",
	}

	lower := strings.ToLower(host)

	for _, domain := range allowed {
		if lower == domain || strings.HasSuffix(lower, "."+domain) {
			return true
		}
	}

	return false
}
