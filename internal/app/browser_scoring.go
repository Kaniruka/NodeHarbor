package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

func (provider IPLarkProvider) ScoreWithBrowser(ctx context.Context, exitIdentity, proxyEndpoint string, runtime BrowserRuntime) (float64, error) {
	if runtime == nil {
		return 0, fmt.Errorf("%w: IPLark requires a Managed Browser Runtime", errBrowserRuntimeUnavailable)
	}
	scoreURL, err := providerURLWithIP(provider.Endpoint, exitIdentity)
	if err != nil {
		return 0, providerUnavailable("IPLark provider endpoint is invalid")
	}
	pages, err := fetchIPLarkPages(ctx, provider, exitIdentity, proxyEndpoint, scoreURL, runtime)
	if err != nil {
		return 0, err
	}
	if err := verifyBrowserExitIdentity(pages, exitIdentity); err != nil {
		return 0, providerUnavailable("IPLark browser Exit Identity verification failed: " + err.Error())
	}
	page := pages[len(pages)-1]
	body := browserPageBody(page)
	if score, ok := parseIPLarkJSON(body); ok {
		return score, nil
	}
	if score, ok := parseIPLarkHTML(body); ok {
		return score, nil
	}
	if score, ok := parseIPLarkHTML([]byte(page.HTML)); ok {
		return score, nil
	}
	if score, ok := parseIPLarkRenderedText([]byte(page.Text)); ok {
		return score, nil
	}
	if isIPLarkChallenge(body) || isIPLarkChallenge([]byte(page.HTML)) || isIPLarkChallenge([]byte(page.Text)) {
		return 0, providerUnavailable("IPLark provider unavailable: challenge response")
	}
	if page.Status < 200 || page.Status >= 300 {
		return 0, providerUnavailable(fmt.Sprintf("IPLark provider unavailable: HTTP %d", page.Status))
	}
	return 0, providerUnavailable("IPLark provider unavailable: rendered score was not found")
}

func fetchIPLarkPages(ctx context.Context, provider IPLarkProvider, exitIdentity, proxyEndpoint, scoreURL string, runtime BrowserRuntime) ([]BrowserPage, error) {
	if searchRuntime, ok := runtime.(BrowserRuntimeSearch); ok && isIPLarkStandardEndpoint(provider.Endpoint) {
		searchURL, err := providerSearchURL(provider.Endpoint, "/search")
		if err != nil {
			return nil, providerUnavailable("IPLark provider endpoint is invalid")
		}
		return searchRuntime.FetchWithInput(ctx, proxyEndpoint, provider.identityURL(exitIdentity), searchURL, exitIdentity)
	}
	return runtime.Fetch(ctx, proxyEndpoint, []string{provider.identityURL(exitIdentity), scoreURL})
}

func (provider IPSuperProvider) ScoreWithBrowser(ctx context.Context, exitIdentity, proxyEndpoint string, runtime BrowserRuntime) (float64, error) {
	if runtime == nil {
		return 0, fmt.Errorf("%w: IPSuper requires a Managed Browser Runtime", errBrowserRuntimeUnavailable)
	}
	scoreURL, err := providerURLWithIP(provider.Endpoint, exitIdentity)
	if err != nil {
		return 0, providerUnavailable("IPSuper provider endpoint is invalid")
	}
	pages, err := fetchIPSuperPages(ctx, provider, exitIdentity, proxyEndpoint, scoreURL, runtime)
	if err != nil {
		return 0, err
	}
	if err := verifyBrowserExitIdentity(pages, exitIdentity); err != nil {
		return 0, providerUnavailable("IPSuper browser Exit Identity verification failed: " + err.Error())
	}
	page := pages[len(pages)-1]
	body := browserPageBody(page)
	if page.Status < 200 || page.Status >= 300 {
		return 0, providerUnavailable(fmt.Sprintf("IPSuper provider unavailable: HTTP %d", page.Status))
	}
	if score, ok := parseIPSuperAggregateScore(body); ok {
		return score, nil
	}
	return 0, providerUnavailable("IPSuper provider unavailable: rendered aggregate security score was not found")
}

func fetchIPSuperPages(ctx context.Context, provider IPSuperProvider, exitIdentity, proxyEndpoint, scoreURL string, runtime BrowserRuntime) ([]BrowserPage, error) {
	if textRuntime, ok := runtime.(BrowserRuntimeTextWait); ok {
		return textRuntime.FetchUntilText(ctx, proxyEndpoint, []string{provider.identityURL(exitIdentity), scoreURL}, []string{"综合安全分", "aggregate security score"})
	}
	return runtime.Fetch(ctx, proxyEndpoint, []string{provider.identityURL(exitIdentity), scoreURL})
}

func providerURLWithIP(endpoint, exitIdentity string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid endpoint")
	}
	if strings.EqualFold(parsed.Hostname(), "iplark.com") && (parsed.Path == "" || parsed.Path == "/" || parsed.Path == "/ipscore") {
		parsed.Path = "/" + url.PathEscape(exitIdentity)
		parsed.RawQuery = ""
		return parsed.String(), nil
	}
	query := parsed.Query()
	query.Set("ip", exitIdentity)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func isIPLarkStandardEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(parsed.Hostname(), "iplark.com")
}

func providerSearchURL(endpoint, path string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid endpoint")
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (provider IPLarkProvider) identityURL(exitIdentity string) string {
	return identityURL(exitIdentity, provider.IPv4IdentityEndpoint, provider.IPv6IdentityEndpoint)
}

func (provider IPSuperProvider) identityURL(exitIdentity string) string {
	return identityURL(exitIdentity, provider.IPv4IdentityEndpoint, provider.IPv6IdentityEndpoint)
}

func identityURL(exitIdentity, ipv4Endpoint, ipv6Endpoint string) string {
	if net.ParseIP(exitIdentity).To4() != nil {
		if ipv4Endpoint != "" {
			return ipv4Endpoint
		}
		return "https://api4.ipify.org"
	}
	if ipv6Endpoint != "" {
		return ipv6Endpoint
	}
	return "https://api6.ipify.org"
}

func verifyBrowserExitIdentity(pages []BrowserPage, expected string) error {
	if len(pages) < 2 {
		return errors.New("identity and score pages were not both returned")
	}
	identity := strings.TrimSpace(string(browserPageBody(pages[0])))
	if parsed := net.ParseIP(identity); parsed == nil || parsed.String() != net.ParseIP(expected).String() {
		return fmt.Errorf("browser observed %q, expected %q", identity, expected)
	}
	return nil
}

func browserPageBody(page BrowserPage) []byte {
	text := strings.TrimSpace(page.Text)
	if text != "" {
		return []byte(text)
	}
	return []byte(page.HTML)
}
