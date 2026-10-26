package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/playwright-community/playwright-go"
)

type mobyBrowser struct {
	once    sync.Once
	pw      *playwright.Playwright
	browser playwright.Browser
	err     error
}

func newMobyBrowser() *mobyBrowser {
	if strings.TrimSpace(os.Getenv("MOBY_BROWSER")) != "1" {
		return nil
	}

	return &mobyBrowser{}
}

func (b *mobyBrowser) start() error {
	b.once.Do(func() {
		b.pw, b.err = playwright.Run()
		if b.err != nil {
			return
		}

		executable := strings.TrimSpace(os.Getenv("CHROME_BIN"))
		if executable == "" {
			executable = "/usr/bin/google-chrome-stable"
		}

		b.browser, b.err = b.pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
			Headless:       playwright.Bool(true),
			ExecutablePath: playwright.String(executable),
			Args: []string{
				"--no-sandbox",
				"--disable-dev-shm-usage",
				"--disable-blink-features=AutomationControlled",
			},
		})
	})

	return b.err
}

func (b *mobyBrowser) Search(ctx context.Context, title string) (*Enrichment, error) {
	if err := b.start(); err != nil {
		return nil, err
	}

	page, err := b.browser.NewPage(playwright.BrowserNewPageOptions{
		UserAgent: playwright.String(
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36",
		),
	})
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = page.Close()
	}()

	searchURL := "https://www.mobygames.com/search/?q=" + url.QueryEscape(title)

	if _, err := page.Goto(searchURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	}); err != nil {
		return nil, err
	}

	time.Sleep(6 * time.Second)

	link, err := page.Locator(`a[href^="/game/"]`).First().GetAttribute("href")
	if err != nil || link == "" {
		return nil, errors.New("mobygames browser: game not found")
	}

	gameURL := "https://www.mobygames.com" + link

	if _, err := page.Goto(gameURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(30000),
	}); err != nil {
		return nil, err
	}

	time.Sleep(3 * time.Second)

	content, err := page.Content()
	if err != nil {
		return nil, err
	}

	enrichment := parseMobyHTML([]byte(content), gameURL)
	if enrichment == nil {
		return nil, errors.New("mobygames browser: empty page")
	}

	if screenshot := b.screenshot(page); screenshot != "" {
		enrichment.Screenshots = append([]string{screenshot}, enrichment.Screenshots...)
	}

	return enrichment, nil
}

func (b *mobyBrowser) screenshot(page playwright.Page) string {
	directory := strings.TrimSpace(os.Getenv("MEDIA_UPLOAD_DIR"))
	if directory == "" {
		directory = "uploads"
	}

	if err := os.MkdirAll(directory, 0o755); err != nil {
		return ""
	}

	name := randomName() + ".png"
	target := filepath.Join(directory, name)

	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(target),
		FullPage: playwright.Bool(true),
	}); err != nil {
		return ""
	}

	return "/rgg-sync/media/file/" + name
}

func randomName() string {
	buffer := make([]byte, 12)

	if _, err := rand.Read(buffer); err != nil {
		return "moby"
	}

	return hex.EncodeToString(buffer)
}
