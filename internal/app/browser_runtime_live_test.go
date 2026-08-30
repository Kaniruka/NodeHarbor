package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaywrightBrowserRuntimeRendersLocalFixture(t *testing.T) {
	if os.Getenv("NODEHARBOR_LIVE_BROWSER_SMOKE") != "1" {
		t.Skip("set NODEHARBOR_LIVE_BROWSER_SMOKE=1 to run the opt-in Chromium smoke test")
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/html")
		_, _ = response.Write([]byte(`<!doctype html><html><body><span id="state">loading</span><script>setTimeout(() => document.querySelector('#state').textContent = 'rendered fixture', 50)</script></body></html>`))
	}))
	defer proxy.Close()

	runtimeRoot := filepath.Join("..", "..", "bin", "browser-runtime")
	runtime := NewPlaywrightBrowserRuntime(BrowserRuntimeConfig{
		DriverDirectory: filepath.Join(runtimeRoot, "driver"),
		ExecutablePath:  FindBundledChromium(filepath.Join(runtimeRoot, "browsers")),
		Headless:        true,
	})
	defer runtime.Close()

	pages, err := runtime.Fetch(context.Background(), proxy.URL, []string{"http://fixture.test/page"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || !strings.Contains(pages[0].Text, "rendered fixture") {
		t.Fatalf("pages = %+v, want rendered fixture", pages)
	}
}
