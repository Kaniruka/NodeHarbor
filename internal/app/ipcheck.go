package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

// IPCheckProvider uses the same provider-neutral contract as IPLark. The
// endpoint remains configurable because the public site is an unstable web
// integration rather than a guaranteed API.
type IPCheckProvider struct {
	Client    *http.Client
	Endpoint  string
	UserAgent string
}

func NewIPCheckProvider(client *http.Client) IPCheckProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return IPCheckProvider{Client: client, Endpoint: "https://ipcheck.ing/api/ipscore", UserAgent: "NodeHarbor/1.0"}
}

func (provider IPCheckProvider) Name() string { return "ipcheck" }
func (provider IPCheckProvider) Score(ctx context.Context, exitIdentity string) (float64, error) {
	return provider.score(ctx, exitIdentity, provider.Client)
}
func (provider IPCheckProvider) ScoreWithClient(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	return provider.score(ctx, exitIdentity, client)
}
func (provider IPCheckProvider) score(ctx context.Context, exitIdentity string, client *http.Client) (float64, error) {
	if client == nil || provider.Endpoint == "" {
		return 0, providerUnavailable("IPCheck.ing provider is not configured")
	}
	endpoint, err := url.Parse(provider.Endpoint)
	if err != nil {
		return 0, providerUnavailable("IPCheck.ing provider endpoint is invalid")
	}
	query := endpoint.Query()
	query.Set("ip", exitIdentity)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, providerUnavailable("IPCheck.ing request could not be created")
	}
	request.Header.Set("Accept", "application/json, text/html")
	if provider.UserAgent != "" {
		request.Header.Set("User-Agent", provider.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, providerUnavailable("IPCheck.ing request failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if readErr != nil {
		return 0, providerUnavailable("IPCheck.ing response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, providerUnavailable("IPCheck.ing provider unavailable")
	}
	if score, ok := parseIPLarkJSON(body); ok {
		return score, nil
	}
	if score, ok := parseIPLarkHTML(body); ok {
		return score, nil
	}
	return 0, providerUnavailable("IPCheck.ing score could not be parsed")
}

type providerUnavailableError struct{ message string }

func (err providerUnavailableError) Error() string { return err.message }
func providerUnavailable(message string) error     { return providerUnavailableError{message: message} }
