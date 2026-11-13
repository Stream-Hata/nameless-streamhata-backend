package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const (
	LinkSafe       = "link_safe"
	LinkUnknown    = "link_unknown"
	LinkSuspicious = "link_suspicious"
	LinkMalicious  = "link_malicious"
	LinkDownload   = "link_download"
)

type LinkReport struct {
	URL       string    `json:"url"`
	Domain    string    `json:"domain"`
	Verdict   string    `json:"verdict"`
	Severity  string    `json:"severity"`
	Reason    string    `json:"reason"`
	Source    string    `json:"source"`
	CheckedAt time.Time `json:"checkedAt"`
}

type LinkReputation interface {
	Lookup(ctx context.Context, rawURL string) (LinkReport, error)
}

type LinkChecker struct {
	reputation LinkReputation
	resolver   *net.Resolver
}

func NewLinkChecker() *LinkChecker {
	checker := &LinkChecker{resolver: net.DefaultResolver}
	if endpoint := strings.TrimSpace(os.Getenv("SECURITY_LINK_REPUTATION_URL")); endpoint != "" {
		checker.reputation = &HTTPLinkReputation{URL: endpoint, Client: &http.Client{Timeout: 10 * time.Second}}
	}

	return checker
}

func (c *LinkChecker) Check(ctx context.Context, rawURL string) (LinkReport, error) {
	rawURL = strings.TrimSpace(rawURL)
	report := LinkReport{URL: rawURL, CheckedAt: time.Now(), Source: "local"}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		report.Verdict = LinkMalicious
		report.Severity = "critical"
		report.Reason = "invalid URL"
		return report, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		report.Verdict = LinkMalicious
		report.Severity = "critical"
		report.Reason = "unsupported URL scheme"
		return report, nil
	}
	if parsed.User != nil {
		report.Verdict = LinkMalicious
		report.Severity = "critical"
		report.Reason = "URL contains credentials"
		return report, nil
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	report.Domain = host
	if isPrivateHost(host) {
		report.Verdict = LinkMalicious
		report.Severity = "critical"
		report.Reason = "private or local address"
		return report, nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		report.Verdict = LinkSuspicious
		report.Severity = "warning"
		report.Reason = "invalid internationalized domain"
		return report, nil
	}
	if ascii != host || strings.Contains(host, "xn--") {
		report.Verdict = LinkSuspicious
		report.Severity = "warning"
		report.Reason = "internationalized or punycode domain"
		return report, nil
	}

	if c.resolver != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		addresses, err := c.resolver.LookupIPAddr(lookupCtx, host)
		cancel()
		if err == nil {
			for _, address := range addresses {
				if isPrivateIP(address.IP) {
					report.Verdict = LinkMalicious
					report.Severity = "critical"
					report.Reason = "domain resolves to private address"
					return report, nil
				}
			}
		}
	}

	if isShortener(host) {
		report.Verdict = LinkSuspicious
		report.Severity = "warning"
		report.Reason = "URL shortener"
		return report, nil
	}
	if isDownloadPath(parsed.Path) {
		report.Verdict = LinkDownload
		report.Severity = "warning"
		report.Reason = "link points to an executable or archive"
		return report, nil
	}

	if c.reputation != nil {
		external, err := c.reputation.Lookup(ctx, rawURL)
		if err != nil {
			report.Verdict = LinkUnknown
			report.Severity = "warning"
			report.Reason = "external reputation is unavailable"
			return report, nil
		}
		if external.CheckedAt.IsZero() {
			external.CheckedAt = report.CheckedAt
		}
		return external, nil
	}

	report.Verdict = LinkUnknown
	report.Severity = "warning"
	report.Reason = "no reputation data"

	return report, nil
}

func isPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}

	return false
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func isShortener(host string) bool {
	switch host {
	case "bit.ly", "tinyurl.com", "t.co", "goo.gl", "cutt.ly", "is.gd", "shorturl.at", "clck.ru", "vk.cc":
		return true
	default:
		return false
	}
}

func isDownloadPath(value string) bool {
	switch strings.ToLower(path.Ext(value)) {
	case ".apk", ".apks", ".xapk", ".exe", ".msi", ".msix", ".dmg", ".pkg", ".ipa",
		".zip", ".rar", ".7z", ".tar", ".gz", ".deb", ".rpm", ".appimage":
		return true
	default:
		return false
	}
}

type HTTPLinkReputation struct {
	URL    string
	Client *http.Client
}

func (s *HTTPLinkReputation) Lookup(ctx context.Context, rawURL string) (LinkReport, error) {
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	body, _ := json.Marshal(map[string]string{"url": rawURL})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return LinkReport{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return LinkReport{}, err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return LinkReport{}, fmt.Errorf("link reputation returned %d", response.StatusCode)
	}
	var report LinkReport
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report); err != nil {
		return LinkReport{}, err
	}

	return report, nil
}
