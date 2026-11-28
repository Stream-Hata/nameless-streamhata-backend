package blog

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"
)

type linkPreviewData struct {
	URL         string `json:"url"`
	Domain      string `json:"domain"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
}

type cachedLinkPreview struct {
	data      linkPreviewData
	expiresAt time.Time
}

var linkPreviewCache = struct {
	sync.Mutex
	entries map[string]cachedLinkPreview
}{entries: make(map[string]cachedLinkPreview)}

var linkPreviewSlots = make(chan struct{}, 6)

func (s *Service) linkPreview(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	rawURL := strings.TrimSpace(request.URL.Query().Get("url"))
	if rawURL == "" || len(rawURL) > 2048 {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid url"})
		return
	}
	target, err := url.Parse(rawURL)
	if err != nil || validatePreviewURL(request.Context(), target) != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid url"})
		return
	}
	target.Fragment = ""
	key := target.String()
	if cached, ok := getCachedLinkPreview(key); ok {
		writeLinkPreview(response, cached)
		return
	}

	select {
	case linkPreviewSlots <- struct{}{}:
		defer func() { <-linkPreviewSlots }()
	case <-request.Context().Done():
		writeJSON(response, http.StatusRequestTimeout, map[string]string{"error": "request timeout"})
		return
	default:
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "preview is busy"})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), 9*time.Second)
	defer cancel()
	preview, err := fetchLinkPreview(ctx, target)
	if err != nil {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{"error": "preview unavailable"})
		return
	}
	setCachedLinkPreview(key, preview)
	writeLinkPreview(response, preview)
}

func writeLinkPreview(response http.ResponseWriter, preview linkPreviewData) {
	response.Header().Set("Cache-Control", "public, max-age=600")
	writeJSON(response, http.StatusOK, preview)
}

func getCachedLinkPreview(key string) (linkPreviewData, bool) {
	linkPreviewCache.Lock()
	defer linkPreviewCache.Unlock()
	item, ok := linkPreviewCache.entries[key]
	if !ok {
		return linkPreviewData{}, false
	}
	if time.Now().After(item.expiresAt) {
		delete(linkPreviewCache.entries, key)
		return linkPreviewData{}, false
	}
	return item.data, true
}

func setCachedLinkPreview(key string, data linkPreviewData) {
	linkPreviewCache.Lock()
	defer linkPreviewCache.Unlock()
	now := time.Now()
	for existing, item := range linkPreviewCache.entries {
		if now.After(item.expiresAt) {
			delete(linkPreviewCache.entries, existing)
		}
	}
	if len(linkPreviewCache.entries) >= 512 {
		for existing := range linkPreviewCache.entries {
			delete(linkPreviewCache.entries, existing)
			break
		}
	}
	linkPreviewCache.entries[key] = cachedLinkPreview{data: data, expiresAt: now.Add(10 * time.Minute)}
}

func validatePreviewURL(ctx context.Context, target *url.URL) error {
	if target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil {
		return errors.New("unsupported url")
	}
	port := target.Port()
	if port != "" && !((target.Scheme == "http" && port == "80") || (target.Scheme == "https" && port == "443")) {
		return errors.New("unsupported port")
	}
	_, err := publicIPs(ctx, target.Hostname())
	return err
}

func publicIPs(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return nil, errors.New("non-public address")
		}
		return []net.IP{ip}, nil
	}
	lowerHost := strings.ToLower(host)
	if lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost") || strings.HasSuffix(lowerHost, ".local") {
		return nil, errors.New("local hostname")
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(resolved) == 0 {
		return nil, errors.New("host resolution failed")
	}
	ips := make([]net.IP, 0, len(resolved))
	for _, item := range resolved {
		if !isPublicIP(item.IP) {
			return nil, errors.New("non-public address")
		}
		ips = append(ips, item.IP)
	}
	return ips, nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func fetchLinkPreview(ctx context.Context, target *url.URL) (linkPreviewData, error) {
	transport := &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := publicIPs(ctx, host)
			if err != nil {
				return nil, err
			}
			dialer := &net.Dialer{Timeout: 3 * time.Second}
			var lastErr error
			for _, ip := range ips {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(next *http.Request, previous []*http.Request) error {
			if len(previous) >= 4 {
				return errors.New("too many redirects")
			}
			return validatePreviewURL(next.Context(), next.URL)
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return linkPreviewData{}, err
	}
	request.Header.Set("User-Agent", "StreamHata-LinkPreview/1.0 (+https://streamhata.rdpctd.dev)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	response, err := client.Do(request)
	if err != nil {
		return linkPreviewData{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return linkPreviewData{}, errors.New("upstream returned an error")
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return linkPreviewData{}, errors.New("upstream is not html")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return linkPreviewData{}, err
	}
	document, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return linkPreviewData{}, err
	}

	meta := make(map[string]string)
	title := ""
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "title") && title == "" {
			title = strings.TrimSpace(nodeText(node))
		}
		if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "meta") {
			name, property, content := "", "", ""
			for _, attr := range node.Attr {
				switch strings.ToLower(attr.Key) {
				case "name":
					name = strings.ToLower(strings.TrimSpace(attr.Val))
				case "property":
					property = strings.ToLower(strings.TrimSpace(attr.Val))
				case "content":
					content = strings.TrimSpace(attr.Val)
				}
			}
			if property != "" {
				meta[property] = content
			}
			if name != "" {
				meta[name] = content
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)

	finalURL := response.Request.URL
	domain := finalURL.Hostname()
	title = firstPreviewValue(meta["og:title"], meta["twitter:title"], title, domain)
	description := firstPreviewValue(meta["og:description"], meta["description"], meta["twitter:description"])
	image := firstPreviewValue(meta["og:image"], meta["og:image:url"], meta["twitter:image"])
	if image != "" {
		imageURL, parseErr := finalURL.Parse(image)
		if parseErr != nil || validatePreviewURL(ctx, imageURL) != nil {
			image = ""
		} else {
			image = imageURL.String()
		}
	}
	return linkPreviewData{
		URL:         finalURL.String(),
		Domain:      strings.TrimPrefix(domain, "www."),
		Title:       limitPreviewText(title, 240),
		Description: limitPreviewText(description, 500),
		Image:       image,
	}, nil
}

func nodeText(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(nodeText(child))
	}
	return builder.String()
}

func firstPreviewValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func limitPreviewText(value string, maxRunes int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
