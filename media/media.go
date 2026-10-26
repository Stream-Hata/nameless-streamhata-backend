package media

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/p7x-dev/nameless-streamhata-backend/netcfg"
)

type Deps interface {
	ServeEndpoint(name string, handler http.HandlerFunc)
}

type result struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

var (
	gifPattern        = regexp.MustCompile(`https://media\.tenor\.com/[^"'\\ ]+\.gif`)
	escapedGifPattern = regexp.MustCompile(`https:\\/\\/media\.tenor\.com\\/[^"'\\ ]+\.gif`)
	imagePattern      = regexp.MustCompile(`(?i)<meta[^>]+(?:property|name)=["'](?:og:image|og:video|twitter:image)["'][^>]+content=["']([^"']+)["']`)
	imagePattern2     = regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+(?:property|name)=["'](?:og:image|og:video|twitter:image)["']`)
)

func RegisterRoutes(srv Deps) {
	srv.ServeEndpoint("/rgg-sync/media/upload", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		request.Body = http.MaxBytesReader(response, request.Body, 25<<20)

		if err := request.ParseMultipartForm(25 << 20); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать файл"})

			return
		}

		file, header, err := request.FormFile("file")
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "файл не выбран"})

			return
		}

		defer func() {
			_ = file.Close()
		}()

		extension := strings.ToLower(filepath.Ext(header.Filename))
		if audioType(extension) == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "поддерживаются mp3, ogg, wav, m4a, aac, flac, opus, webm"})

			return
		}

		directory := os.Getenv("MEDIA_UPLOAD_DIR")
		if directory == "" {
			directory = "uploads"
		}

		if err := os.MkdirAll(directory, 0o755); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось создать хранилище"})

			return
		}

		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось создать имя файла"})

			return
		}

		name := hex.EncodeToString(random) + extension
		target, err := os.Create(filepath.Join(directory, name))
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить файл"})

			return
		}

		_, copyErr := io.Copy(target, file)
		closeErr := target.Close()

		if copyErr != nil || closeErr != nil {
			_ = os.Remove(filepath.Join(directory, name))
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить файл"})

			return
		}

		writeJSON(response, http.StatusOK, result{
			URL:  "/rgg-sync/media/file/" + name,
			Type: strings.TrimPrefix(extension, "."),
		})
	})

	srv.ServeEndpoint("/rgg-sync/media/file/{name}", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		name := request.PathValue("name")
		if name == "" || strings.ContainsAny(name, `/\`) || name != filepath.Base(name) {
			http.NotFound(response, request)

			return
		}

		directory := os.Getenv("MEDIA_UPLOAD_DIR")
		if directory == "" {
			directory = "uploads"
		}

		http.ServeFile(response, request, filepath.Join(directory, name))
	})

	srv.ServeEndpoint("/rgg-sync/media/play", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		raw := strings.TrimSpace(request.URL.Query().Get("url"))
		if raw == "" {
			http.Error(response, "url is required", http.StatusBadRequest)

			return
		}

		resolved, err := ResolveAudio(request.Context(), raw)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadGateway)

			return
		}

		http.Redirect(response, request, resolved.URL, http.StatusFound)
	})

	srv.ServeEndpoint("/rgg-sync/media/audio", func(response http.ResponseWriter, request *http.Request) {
		raw := strings.TrimSpace(request.URL.Query().Get("url"))
		if raw == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "url is required"})

			return
		}

		resolved, err := ResolveAudio(request.Context(), raw)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, resolved)
	})

	srv.ServeEndpoint("/rgg-sync/media/resolve", func(response http.ResponseWriter, request *http.Request) {
		raw := strings.TrimSpace(request.URL.Query().Get("url"))
		if raw == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "url is required"})

			return
		}

		resolved, err := Resolve(request.Context(), raw)
		if err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": err.Error()})

			return
		}

		writeJSON(response, http.StatusOK, resolved)
	})
}

func ResolveAudio(ctx context.Context, raw string) (result, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return result{}, fmt.Errorf("некорректная ссылка")
	}

	if mediaType := audioType(parsed.Path); mediaType != "" {
		return result{URL: raw, Type: mediaType}, nil
	}

	host := strings.ToLower(parsed.Hostname())

	if strings.Contains(host, "youtube.") || host == "youtu.be" || strings.Contains(host, "soundcloud.com") {
		resolved, err := resolveWithYTDLP(ctx, raw)
		if err == nil {
			return resolved, nil
		}

		return result{}, err
	}

	body, err := fetch(ctx, raw)
	if err != nil {
		return result{}, err
	}

	content := string(body)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)<meta[^>]+(?:property|name)=["'](?:og:audio|twitter:player:stream)["'][^>]+content=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+(?:property|name)=["'](?:og:audio|twitter:player:stream)["']`),
		regexp.MustCompile(`(?i)<audio[^>]+src=["']([^"']+)["']`),
		regexp.MustCompile(`(?i)<source[^>]+src=["']([^"']+\.(?:mp3|ogg|oga|wav|m4a|aac|flac|opus|webm)[^"']*)["']`),
	}

	for _, pattern := range patterns {
		if match := pattern.FindStringSubmatch(content); len(match) > 1 {
			return result{URL: match[1], Type: audioType(match[1])}, nil
		}
	}

	return result{}, fmt.Errorf("не удалось найти аудио на странице")
}

func resolveWithYTDLP(ctx context.Context, raw string) (result, error) {
	path := strings.TrimSpace(os.Getenv("YTDLP_PATH"))

	if path == "" {
		if discovered, err := exec.LookPath("yt-dlp"); err == nil {
			path = discovered
		}
	}

	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidates := []string{
				filepath.Join(home, ".local", "bin", "yt-dlp"),
				filepath.Join(home, "bin", "yt-dlp"),
				filepath.Join(".", "bin", "yt-dlp"),
			}

			for _, candidate := range candidates {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					path = candidate

					break
				}
			}
		}
	}

	if path == "" {
		return result{}, fmt.Errorf("для YouTube/SoundCloud нужен yt-dlp или прокси в настройках сети")
	}

	args := []string{"--no-playlist", "-f", "bestaudio", "-g"}

	if proxy := mediaProxyURL(); proxy != "" {
		args = append(args, "--proxy", proxy)
	}

	args = append(args, raw)

	command := exec.CommandContext(ctx, path, args...)
	output, err := command.Output()
	if err != nil {
		return result{}, fmt.Errorf("не удалось извлечь аудио: %w", err)
	}

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return result{URL: line, Type: "audio"}, nil
		}
	}

	return result{}, fmt.Errorf("yt-dlp не вернул аудио")
}

func mediaProxyURL() string {
	if settings := netcfg.Default().Get(); settings.Enabled && settings.ProxyURL != "" {
		return settings.ProxyURL
	}

	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}

	for _, candidate := range []string{"127.0.0.1:1080", "127.0.0.1:10808", "127.0.0.1:7890", "127.0.0.1:8080"} {
		connection, err := net.DialTimeout("tcp", candidate, 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()

			if strings.HasSuffix(candidate, ":1080") || strings.HasSuffix(candidate, ":7890") {
				return "socks5h://" + candidate
			}

			return "http://" + candidate
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".config", "kioslaverc")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "HttpsProxy=") || strings.HasPrefix(line, "HttpProxy=") {
					if index := strings.Index(line, "="); index >= 0 {
						if value := strings.TrimSpace(line[index+1:]); value != "" {
							return value
						}
					}
				}
			}
		}
	}

	return ""
}

func audioType(path string) string {
	lower := strings.ToLower(path)

	for _, extension := range []string{".mp3", ".ogg", ".oga", ".wav", ".m4a", ".aac", ".flac", ".opus", ".webm"} {
		if strings.HasSuffix(lower, extension) {
			return strings.TrimPrefix(extension, ".")
		}
	}

	return ""
}

func Resolve(ctx context.Context, raw string) (result, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return result{}, fmt.Errorf("некорректная ссылка")
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return result{}, fmt.Errorf("локальные адреса запрещены")
	}

	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return result{}, fmt.Errorf("локальные адреса запрещены")
	}

	if mediaType := directType(parsed.Path); mediaType != "" {
		return result{URL: raw, Type: mediaType}, nil
	}

	if strings.Contains(host, "tenor.com") {
		if resolved, err := resolveTenor(ctx, raw); err == nil {
			return resolved, nil
		}
	}

	if strings.Contains(host, "giphy.com") {
		if resolved, err := resolveGiphy(ctx, raw); err == nil {
			return resolved, nil
		}
	}

	body, err := fetch(ctx, raw)
	if err != nil {
		return result{}, err
	}

	if match := gifPattern.FindString(string(body)); match != "" {
		return result{URL: match, Type: "gif"}, nil
	}

	content := string(body)
	if match := imagePattern.FindStringSubmatch(content); len(match) > 1 {
		return result{URL: match[1], Type: directType(match[1])}, nil
	}

	if match := imagePattern2.FindStringSubmatch(content); len(match) > 1 {
		return result{URL: match[1], Type: directType(match[1])}, nil
	}

	return result{}, fmt.Errorf("не удалось найти гифку на странице")
}

func resolveTenor(ctx context.Context, raw string) (result, error) {
	body, err := fetch(ctx, "https://tenor.com/oembed?url="+url.QueryEscape(raw))
	if err == nil {
		var payload struct {
			Thumbnail string `json:"thumbnail_url"`
			HTML      string `json:"html"`
		}

		if json.Unmarshal(body, &payload) == nil {
			if page, pageErr := fetch(ctx, raw); pageErr == nil {
				if match := gifPattern.FindString(string(page)); match != "" {
					return result{URL: match, Type: "gif"}, nil
				}
			}

			if payload.Thumbnail != "" {
				gif := strings.Replace(payload.Thumbnail, "AAAAN", "AAAAM", 1)
				gif = strings.TrimSuffix(gif, ".png") + ".gif"

				if gif != payload.Thumbnail {
					return result{URL: gif, Type: "gif"}, nil
				}

				mediaType := directType(payload.Thumbnail)
				if mediaType == "" {
					mediaType = "image"
				}

				return result{URL: payload.Thumbnail, Type: mediaType}, nil
			}
		}
	}

	page, err := fetch(ctx, raw)
	if err != nil {
		return result{}, err
	}

	if match := gifPattern.FindString(string(page)); match != "" {
		return result{URL: match, Type: "gif"}, nil
	}

	if match := escapedGifPattern.FindString(string(page)); match != "" {
		match = strings.ReplaceAll(match, `\/`, `/`)

		return result{URL: match, Type: "gif"}, nil
	}

	return result{}, fmt.Errorf("гифка Tenor не найдена")
}

func resolveGiphy(ctx context.Context, raw string) (result, error) {
	body, err := fetch(ctx, "https://giphy.com/services/oembed?url="+url.QueryEscape(raw))
	if err != nil {
		return result{}, err
	}

	var payload struct {
		URL string `json:"url"`
	}

	if err := json.Unmarshal(body, &payload); err != nil || payload.URL == "" {
		return result{}, fmt.Errorf("гифка Giphy не найдена")
	}

	mediaType := directType(payload.URL)
	if mediaType == "" {
		mediaType = "gif"
	}

	return result{URL: payload.URL, Type: mediaType}, nil
}

func fetch(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; NamelessStreamHata/1.0)")
	request.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")

	response, err := netcfg.NewHTTPClient(12 * time.Second).Do(request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("источник вернул %d", response.StatusCode)
	}

	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}

func directType(path string) string {
	lower := strings.ToLower(path)
	for _, extension := range []string{".gif", ".webp", ".png", ".jpg", ".jpeg", ".mp4", ".webm"} {
		if strings.HasSuffix(lower, extension) {
			return strings.TrimPrefix(extension, ".")
		}
	}

	return ""
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)

	_ = json.NewEncoder(response).Encode(payload)
}
