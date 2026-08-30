package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

const defaultBrowserRuntimeTimeout = 15 * time.Second

type BrowserRuntimeConfig struct {
	DriverDirectory      string
	ExecutablePath       string
	DiagnosticsDirectory string
	Headless             bool
	Timeout              time.Duration
}

type PlaywrightBrowserRuntime struct {
	config     BrowserRuntimeConfig
	mu         sync.Mutex
	playwright *playwright.Playwright
	browser    playwright.Browser
	closed     bool
}

func NewPlaywrightBrowserRuntime(config BrowserRuntimeConfig) *PlaywrightBrowserRuntime {
	if config.Timeout <= 0 {
		config.Timeout = defaultBrowserRuntimeTimeout
	}
	return &PlaywrightBrowserRuntime{config: config}
}

func (runtime *PlaywrightBrowserRuntime) DiagnosticsMode() string {
	if !runtime.config.Headless {
		return "headed"
	}
	return "headless"
}

func (runtime *PlaywrightBrowserRuntime) Fetch(ctx context.Context, proxyEndpoint string, targets []string) ([]BrowserPage, error) {
	return runtime.fetchUntilText(ctx, proxyEndpoint, targets, nil)
}

func (runtime *PlaywrightBrowserRuntime) FetchUntilText(ctx context.Context, proxyEndpoint string, targets, markers []string) ([]BrowserPage, error) {
	return runtime.fetchUntilText(ctx, proxyEndpoint, targets, markers)
}

func (runtime *PlaywrightBrowserRuntime) fetchUntilText(ctx context.Context, proxyEndpoint string, targets, markers []string) ([]BrowserPage, error) {
	if proxyEndpoint == "" {
		return nil, fmt.Errorf("%w: Browser Proxy Endpoint is empty", errBrowserRuntimeUnavailable)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: no browser targets were supplied", errBrowserRuntimeUnavailable)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.closed {
		return nil, fmt.Errorf("%w: runtime is closed", errBrowserRuntimeUnavailable)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		pages, err := runtime.fetchOnceUntilText(ctx, proxyEndpoint, targets, markers)
		if err == nil {
			return pages, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		runtime.resetBrowser()
	}
	return nil, lastErr
}

func (runtime *PlaywrightBrowserRuntime) fetchOnce(ctx context.Context, proxyEndpoint string, targets []string) ([]BrowserPage, error) {
	return runtime.fetchOnceUntilText(ctx, proxyEndpoint, targets, nil)
}

func (runtime *PlaywrightBrowserRuntime) fetchOnceUntilText(ctx context.Context, proxyEndpoint string, targets, markers []string) ([]BrowserPage, error) {
	if err := runtime.ensureStarted(); err != nil {
		return nil, err
	}

	proxy := &playwright.Proxy{Server: proxyEndpoint}
	contextOptions := playwright.BrowserNewContextOptions{
		Proxy:             proxy,
		JavaScriptEnabled: boolPtr(true),
	}
	browserContext, err := runtime.browser.NewContext(contextOptions)
	if err != nil {
		return nil, fmt.Errorf("%w: create Browser Context: %v", errBrowserRuntimeUnavailable, err)
	}
	_ = browserContext.AddInitScript(playwright.Script{Content: stringPtr("Object.defineProperty(navigator, 'webdriver', {get: () => undefined});")})
	defer browserContext.Close()
	timeoutMS := float64(runtime.config.Timeout / time.Millisecond)
	browserContext.SetDefaultTimeout(timeoutMS)
	browserContext.SetDefaultNavigationTimeout(timeoutMS)

	pages := make([]BrowserPage, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := browserContext.NewPage()
		if err != nil {
			return nil, fmt.Errorf("%w: create browser page: %v", errBrowserRuntimeUnavailable, err)
		}
		response, gotoErr := page.Goto(target, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded, Timeout: &timeoutMS})
		browserPage := BrowserPage{URL: page.URL()}
		if response != nil {
			browserPage.Status = response.Status()
		}
		browserPage.URL = page.URL()
		if gotoErr == nil {
			_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{State: playwright.LoadStateLoad, Timeout: &timeoutMS})
			_, _ = page.WaitForFunction("document.body && document.body.innerText.trim().length > 0", nil, playwright.PageWaitForFunctionOptions{Timeout: &timeoutMS})
			if len(markers) > 0 && isLastTarget(target, targets) {
				waitExpression := browserTextWaitExpression(markers)
				_, _ = page.WaitForFunction(waitExpression, nil, playwright.PageWaitForFunctionOptions{Timeout: &timeoutMS})
			}
		}
		browserPage.Title, _ = page.Title()
		// Use rendered text for provider parsing. TextContent includes hidden
		// templates and script contents, which can contain unrelated score
		// examples such as "100/100".
		browserPage.Text, _ = page.InnerText("body")
		browserPage.HTML, _ = page.Content()
		if gotoErr != nil {
			browserPage.Screenshot, _ = page.Screenshot(playwright.PageScreenshotOptions{Type: screenshotTypePNG()})
			runtime.saveDiagnostic(target, browserPage)
			_ = page.Close()
			return pages, fmt.Errorf("%w: navigate %s: %v", errBrowserRuntimeUnavailable, target, gotoErr)
		}
		pages = append(pages, browserPage)
		_ = page.Close()
	}
	return pages, nil
}

func isLastTarget(target string, targets []string) bool {
	return len(targets) > 0 && target == targets[len(targets)-1]
}

func browserTextWaitExpression(markers []string) string {
	quoted := make([]string, 0, len(markers))
	for _, marker := range markers {
		quoted = append(quoted, fmt.Sprintf("%q", marker))
	}
	// IPSuper renders a default 100/100 card while its tasks are still
	// running. Require the task summary to explicitly report zero running
	// tasks so the placeholder cannot become a cached score.
	return "() => { const text = document.body ? document.body.innerText : ''; return [" + strings.Join(quoted, ",") + "].some(marker => text.includes(marker)) && /\\b0\\s+running\\b/i.test(text) && !/[1-9]\\d*\\s*运行中/.test(text); }"
}

func (runtime *PlaywrightBrowserRuntime) resetBrowser() {
	if runtime.browser != nil {
		_ = runtime.browser.Close()
		runtime.browser = nil
	}
	if runtime.playwright != nil {
		_ = runtime.playwright.Stop()
		runtime.playwright = nil
	}
}

func (runtime *PlaywrightBrowserRuntime) saveDiagnostic(target string, page BrowserPage) {
	if runtime.config.DiagnosticsDirectory == "" || len(page.Screenshot) == 0 {
		return
	}
	if os.MkdirAll(runtime.config.DiagnosticsDirectory, 0o700) != nil {
		return
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + sanitizeDiagnosticName(target) + ".png"
	_ = os.WriteFile(filepath.Join(runtime.config.DiagnosticsDirectory, name), page.Screenshot, 0o600)
	entries, _ := os.ReadDir(runtime.config.DiagnosticsDirectory)
	cutoff := time.Now().Add(-24 * time.Hour)
	for index, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err == nil && (info.ModTime().Before(cutoff) || index < len(entries)-20) {
			_ = os.Remove(filepath.Join(runtime.config.DiagnosticsDirectory, entry.Name()))
		}
	}
}

func sanitizeDiagnosticName(target string) string {
	name := strings.NewReplacer("https://", "", "http://", "", "/", "_", "?", "_", "&", "_", "=", "_").Replace(target)
	name = strings.Trim(name, "._-")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		return "browser-failure"
	}
	return name
}

func (runtime *PlaywrightBrowserRuntime) ensureStarted() error {
	if runtime.browser != nil && runtime.browser.IsConnected() {
		return nil
	}
	if runtime.playwright != nil || runtime.browser != nil {
		runtime.resetBrowser()
	}
	options := &playwright.RunOptions{DriverDirectory: runtime.config.DriverDirectory, SkipInstallBrowsers: true, Verbose: false}
	instance, err := playwright.Run(options)
	if err != nil {
		return fmt.Errorf("%w: start Playwright driver: %v", errBrowserRuntimeUnavailable, err)
	}
	launch := playwright.BrowserTypeLaunchOptions{
		Headless:          &runtime.config.Headless,
		Args:              []string{"--disable-blink-features=AutomationControlled"},
		IgnoreDefaultArgs: []string{"--enable-automation"},
	}
	if runtime.config.ExecutablePath != "" {
		launch.ExecutablePath = &runtime.config.ExecutablePath
	}
	browser, err := instance.Chromium.Launch(launch)
	if err != nil {
		_ = instance.Stop()
		return fmt.Errorf("%w: launch Chromium: %v", errBrowserRuntimeUnavailable, err)
	}
	runtime.playwright = instance
	runtime.browser = browser
	return nil
}

func (runtime *PlaywrightBrowserRuntime) Close() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.closed = true
	var closeErr error
	if runtime.browser != nil {
		closeErr = errors.Join(closeErr, runtime.browser.Close())
		runtime.browser = nil
	}
	if runtime.playwright != nil {
		closeErr = errors.Join(closeErr, runtime.playwright.Stop())
		runtime.playwright = nil
	}
	return closeErr
}

func boolPtr(value bool) *bool { return &value }

func stringPtr(value string) *string { return &value }

func screenshotTypePNG() *playwright.ScreenshotType {
	value := playwright.ScreenshotTypePng
	return value
}

func FindBundledChromium(root string) string {
	if root == "" {
		return ""
	}
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" || info == nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), "chrome.exe") {
			found = path
		}
		return nil
	})
	return found
}
